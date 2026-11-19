// Package service coordinates subscriptions, refreshes, link resolution,
// transcript retrieval and library reads over the store.
//
// The service is independent of LazyCat identity headers and paths: callers
// pass an owner string and a context. Network access always goes through the
// safehttp client, so only public HTTP(S) endpoints are reachable.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"boai/internal/ai"
	"boai/internal/feed"
	"boai/internal/safehttp"
	"boai/internal/store"
	"boai/internal/transcript"
)

const maxFeedEpisodes = 1000

// Options configures the service.
type Options struct {
	UpdateInterval time.Duration
	HTTPTimeout    time.Duration
	MaxFeedBytes   int64
	UserAgent      string
	AllowPrivate   bool
	AllowFakeIP    bool
	ITunesBase     string
	// Secret protects user-supplied API keys at rest. It must come from the
	// deployment environment or a protected per-data-directory file.
	Secret string
	// AllowInsecureAI permits loopback/private AI endpoints. It exists for
	// local fake-service acceptance and must stay false in production.
	AllowInsecureAI bool
	AITimeout       time.Duration
	// DownloadDir stores user-downloaded episode audio. Empty disables local
	// downloads; the deployment points it inside the per-user data directory.
	DownloadDir string
}

// Service is the application core.
type Service struct {
	store          *store.Store
	client         *safehttp.Client
	resolver       *feed.Resolver
	updateInterval time.Duration
	httpTimeout    time.Duration
	maxFeedBytes   int64
	now            func() time.Time

	text            ai.TextProvider
	speech          ai.SpeechProvider
	secret          string
	allowInsecureAI bool
	aiTimeout       time.Duration
	aiClient        *safehttp.Client
	downloadDir     string
}

// New builds a service around a store.
func New(st *store.Store, opts Options) *Service {
	if opts.UpdateInterval <= 0 {
		opts.UpdateInterval = 30 * time.Minute
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 30 * time.Second
	}
	if opts.MaxFeedBytes <= 0 {
		opts.MaxFeedBytes = 16 << 20
	}
	if opts.AITimeout <= 0 {
		opts.AITimeout = ai.Timeout
	}
	client := safehttp.New(safehttp.Options{
		AllowPrivate: opts.AllowPrivate,
		AllowFakeIP:  opts.AllowFakeIP,
		Timeout:      opts.HTTPTimeout,
		UserAgent:    opts.UserAgent,
	})
	aiClient := safehttp.New(safehttp.Options{
		AllowPrivate: opts.AllowInsecureAI,
		AllowFakeIP:  opts.AllowFakeIP,
		Timeout:      2 * time.Minute,
		UserAgent:    opts.UserAgent,
	})
	provider := ai.NewOpenAI(aiClient)
	svc := &Service{
		store:           st,
		client:          client,
		resolver:        feed.NewResolver(client, opts.ITunesBase),
		updateInterval:  opts.UpdateInterval,
		httpTimeout:     opts.HTTPTimeout,
		maxFeedBytes:    opts.MaxFeedBytes,
		now:             time.Now,
		text:            provider,
		speech:          provider,
		secret:          opts.Secret,
		allowInsecureAI: opts.AllowInsecureAI,
		aiTimeout:       opts.AITimeout,
		aiClient:        aiClient,
		downloadDir:     opts.DownloadDir,
	}
	// Any job that was queued or running when the process stopped is marked
	// failed and remains retryable. This prevents permanently stuck rows.
	recoverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = st.RecoverInterruptedTasks(recoverCtx, time.Now().Unix())
	cancel()
	return svc
}

// Store exposes the store to the HTTP layer for simple reads.
func (s *Service) Store() *store.Store { return s.store }

// ResolveFailure reports a link that could not be confirmed as a feed.
type ResolveFailure struct {
	Result feed.Resolution
}

func (e *ResolveFailure) Error() string { return e.Result.Message }

// Resolve interprets a pasted link without subscribing.
func (s *Service) Resolve(ctx context.Context, raw string) feed.Resolution {
	return s.resolver.Resolve(ctx, raw)
}

// SearchDirectory searches the public podcast directory.
func (s *Service) SearchDirectory(ctx context.Context, term string) ([]feed.DirectoryItem, error) {
	return s.resolver.SearchDirectory(ctx, term)
}

// Discover returns the public chart snapshot and latest episodes of its top
// shows. It is read-only and does not touch the owner's library.
func (s *Service) Discover(ctx context.Context) (feed.DiscoverBoard, error) {
	return s.resolver.Discover(ctx)
}

// PreviewShow fetches a public show without adding it to the visible library.
// New previews are stored as hidden feeds so their episodes can be opened,
// played and remembered; subscribing later simply unhides the same feed.
// Already-subscribed shows are refreshed and reported as subscribed.
func (s *Service) PreviewShow(ctx context.Context, owner, rawURL string) (store.Feed, []store.Episode, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return store.Feed{}, nil, false, errors.New("service: missing owner")
	}
	canonical, err := feed.CanonicalURL(strings.TrimSpace(rawURL))
	if err != nil {
		return store.Feed{}, nil, false, errors.New("节目地址必须是公开的 HTTP(S) 链接")
	}
	exists, hidden, err := s.store.FeedVisibilityByURL(ctx, owner, canonical)
	if err != nil {
		return store.Feed{}, nil, false, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 4*s.httpTimeout)
	defer cancel()
	result, err := s.fetchFeed(fetchCtx, canonical, "", "")
	if err != nil {
		return store.Feed{}, nil, false, err
	}
	parsed, err := feed.Parse(result.body, result.finalURL)
	if err != nil {
		return store.Feed{}, nil, false, errors.New("该地址不是可解析的 RSS／Atom 订阅源")
	}
	if !parsed.Playable() {
		return store.Feed{}, nil, false, errors.New("该订阅源没有可播放的音频单集")
	}
	feedID, _, err := s.store.UpsertFeed(ctx, owner, toFeedInput(parsed, result))
	if err != nil {
		return store.Feed{}, nil, false, err
	}
	if !exists || hidden {
		if err := s.store.SetFeedHidden(ctx, owner, feedID, true); err != nil {
			return store.Feed{}, nil, false, err
		}
	}
	if err := s.store.RecordFeedResult(ctx, owner, feedID, true, result.etag, result.lastModified,
		s.now().Add(s.updateInterval), ""); err != nil {
		return store.Feed{}, nil, false, err
	}
	feedRow, err := s.store.GetFeed(ctx, owner, feedID)
	if err != nil {
		return store.Feed{}, nil, false, err
	}
	episodes, err := s.store.ListEpisodes(ctx, owner, store.EpisodeFilter{
		Kind: "all", FeedID: feedID, Limit: 120,
	})
	if err != nil {
		return store.Feed{}, nil, false, err
	}
	return feedRow, episodes, exists && !hidden, nil
}

// Subscribe resolves (or confirms) a feed, stores it and queues its episodes.
// It only reports success after parsing the feed document.
func (s *Service) Subscribe(ctx context.Context, owner, input, feedURL string, categoryIDs []int64) (store.Feed, int, error) {
	if strings.TrimSpace(owner) == "" {
		return store.Feed{}, 0, errors.New("service: missing owner")
	}
	feedURL = strings.TrimSpace(feedURL)
	if feedURL == "" {
		if strings.TrimSpace(input) == "" {
			return store.Feed{}, 0, errors.New("请输入节目名或订阅链接")
		}
		res := s.resolver.Resolve(ctx, input)
		if res.Status != "subscribable" || res.FeedURL == "" {
			return store.Feed{}, 0, &ResolveFailure{Result: res}
		}
		feedURL = res.FeedURL
	}
	canonical, err := feed.CanonicalURL(feedURL)
	if err != nil {
		return store.Feed{}, 0, errors.New("订阅地址必须是公开的 HTTP(S) 链接")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 4*s.httpTimeout)
	defer cancel()
	result, err := s.fetchFeed(fetchCtx, canonical, "", "")
	if err != nil {
		return store.Feed{}, 0, err
	}
	parsed, err := feed.Parse(result.body, result.finalURL)
	if err != nil {
		return store.Feed{}, 0, errors.New("该地址不是可解析的 RSS／Atom 订阅源，未建立订阅")
	}
	if !parsed.Playable() {
		return store.Feed{}, 0, errors.New("该订阅源没有可播放的音频单集，未建立订阅")
	}
	feedID, newCount, err := s.store.UpsertFeed(ctx, owner, toFeedInput(parsed, result))
	if err != nil {
		return store.Feed{}, 0, err
	}
	// A hidden show preview becomes a real subscription when the user
	// explicitly subscribes; its episodes and progress stay intact.
	if err := s.store.SetFeedHidden(ctx, owner, feedID, false); err != nil {
		return store.Feed{}, 0, err
	}
	if len(categoryIDs) > 0 {
		if err := s.store.SetFeedCategories(ctx, owner, feedID, categoryIDs); err != nil {
			return store.Feed{}, 0, err
		}
	}
	if err := s.store.RecordFeedResult(ctx, owner, feedID, true, result.etag, result.lastModified,
		s.now().Add(s.updateInterval), ""); err != nil {
		return store.Feed{}, 0, err
	}
	subscribed, err := s.store.GetFeed(ctx, owner, feedID)
	if err != nil {
		return store.Feed{}, 0, err
	}
	return subscribed, newCount, nil
}

// RefreshResult reports one feed refresh.
type RefreshResult struct {
	Feed        store.Feed `json:"feed"`
	NewEpisodes int        `json:"new_episodes"`
	NotModified bool       `json:"not_modified"`
}

// RefreshFeed conditionally fetches one subscription and stores new episodes.
func (s *Service) RefreshFeed(ctx context.Context, owner string, feedID int64) (RefreshResult, error) {
	state, err := s.store.FeedFetchState(ctx, owner, feedID)
	if err != nil {
		return RefreshResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*s.httpTimeout)
	defer cancel()
	result, err := s.fetchFeed(ctx, state.URL, state.ETag, state.LastModified)
	if err != nil {
		s.recordFailure(context.Background(), state, err)
		return RefreshResult{}, err
	}
	if result.notModified {
		if err := s.store.RecordFeedResult(ctx, owner, feedID, true, "", "",
			s.now().Add(s.updateInterval), ""); err != nil {
			return RefreshResult{}, err
		}
		refreshed, err := s.store.GetFeed(ctx, owner, feedID)
		return RefreshResult{Feed: refreshed, NotModified: true}, err
	}
	parsed, err := feed.Parse(result.body, result.finalURL)
	if err != nil {
		failure := errors.New("订阅源返回的内容无法解析，已保留旧单集")
		s.recordFailure(context.Background(), state, failure)
		return RefreshResult{}, failure
	}
	_, newCount, err := s.store.UpsertFeed(ctx, owner, toFeedInput(parsed, result))
	if err != nil {
		return RefreshResult{}, err
	}
	if err := s.store.RecordFeedResult(ctx, owner, feedID, true, result.etag, result.lastModified,
		s.now().Add(s.updateInterval), ""); err != nil {
		return RefreshResult{}, err
	}
	refreshed, err := s.store.GetFeed(ctx, owner, feedID)
	if err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{Feed: refreshed, NewEpisodes: newCount}, nil
}

// RefreshAll refreshes every subscription for one owner with bounded
// concurrency. Individual failures do not abort the batch.
func (s *Service) RefreshAll(ctx context.Context, owner string) (int, int, int) {
	feeds, err := s.store.ListFeeds(ctx, owner)
	if err != nil {
		return 0, 0, 0
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		updated int
		added   int
		failed  int
		sem     = make(chan struct{}, 4)
	)
	for _, item := range feeds {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(feedID int64) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			result, refreshErr := s.RefreshFeed(ctx, owner, feedID)
			mu.Lock()
			defer mu.Unlock()
			if refreshErr != nil {
				failed++
				return
			}
			updated++
			added += result.NewEpisodes
		}(item.ID)
	}
	wg.Wait()
	return updated, added, failed
}

func (s *Service) recordFailure(ctx context.Context, state store.DueFeed, cause error) {
	next := s.now().Add(backoff(state.ConsecutiveFailures+1, s.updateInterval))
	_ = s.store.RecordFeedResult(ctx, state.Owner, state.ID, false, "", "", next, cause.Error())
}

func backoff(failures int, interval time.Duration) time.Duration {
	base := 5 * time.Minute
	if failures < 1 {
		failures = 1
	}
	if failures > 5 {
		failures = 5
	}
	delay := base << (failures - 1)
	if delay > time.Hour {
		delay = time.Hour
	}
	if interval > 0 && delay > interval && interval >= time.Minute {
		delay = interval
	}
	return delay
}

// Scheduler refreshes due subscriptions until the context is cancelled.
func (s *Service) Scheduler(ctx context.Context, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			due, err := s.store.DueFeeds(ctx, s.now(), 10)
			if err != nil {
				logf("scheduler: list due feeds: %v", err)
				continue
			}
			for _, item := range due {
				if ctx.Err() != nil {
					return
				}
				refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				_, err := s.RefreshFeed(refreshCtx, item.Owner, item.ID)
				cancel()
				if err != nil {
					logf("scheduler: refresh feed %d: %v", item.ID, err)
				}
			}
		}
	}
}

type fetchedFeed struct {
	body         []byte
	finalURL     string
	contentType  string
	status       int
	etag         string
	lastModified string
	notModified  bool
}

func (s *Service) fetchFeed(ctx context.Context, rawURL, etag, lastModified string) (fetchedFeed, error) {
	canonical, err := feed.CanonicalURL(rawURL)
	if err != nil {
		return fetchedFeed{}, errors.New("订阅地址必须是公开的 HTTP(S) 链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return fetchedFeed{}, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fetchedFeed{}, fmt.Errorf("无法获取订阅源：%w", err)
	}
	defer resp.Body.Close()
	finalURL := canonical
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if resp.StatusCode == http.StatusNotModified {
		return fetchedFeed{finalURL: finalURL, status: resp.StatusCode, contentType: contentType,
			etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified"), notModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fetchedFeed{}, fmt.Errorf("订阅源返回 HTTP %d", resp.StatusCode)
	}
	body, err := safehttp.ReadAll(resp.Body, s.maxFeedBytes)
	if err != nil {
		if errors.Is(err, safehttp.ErrTooLarge) {
			return fetchedFeed{}, errors.New("订阅源文件超过大小限制")
		}
		return fetchedFeed{}, fmt.Errorf("无法读取订阅源：%w", err)
	}
	return fetchedFeed{
		body:         body,
		finalURL:     finalURL,
		contentType:  contentType,
		status:       resp.StatusCode,
		etag:         resp.Header.Get("ETag"),
		lastModified: resp.Header.Get("Last-Modified"),
	}, nil
}

func toFeedInput(parsed *feed.Feed, result fetchedFeed) store.FeedInput {
	episodes := make([]store.EpisodeInput, 0, len(parsed.Episodes))
	for i, ep := range parsed.Episodes {
		if i >= maxFeedEpisodes {
			break
		}
		published := int64(0)
		if ep.PublishedAt != nil {
			published = ep.PublishedAt.Unix()
		}
		refs, err := json.Marshal(ep.Transcripts)
		if err != nil {
			refs = []byte("[]")
		}
		chapters, err := json.Marshal(ep.Chapters)
		if err != nil {
			chapters = []byte("[]")
		}
		episodes = append(episodes, store.EpisodeInput{
			GUID:            ep.GUID,
			Title:           ep.Title,
			Description:     ep.Description,
			Link:            ep.Link,
			AudioURL:        ep.AudioURL,
			AudioType:       ep.AudioType,
			ImageURL:        ep.ImageURL,
			DurationSeconds: ep.DurationSeconds,
			PublishedAt:     published,
			TranscriptRefs:  string(refs),
			Chapters:        string(chapters),
		})
	}
	return store.FeedInput{
		URL:          result.finalURL,
		SiteURL:      parsed.SiteURL,
		Title:        parsed.Title,
		Description:  parsed.Description,
		ImageURL:     parsed.ImageURL,
		ETag:         result.etag,
		LastModified: result.lastModified,
		Episodes:     episodes,
	}
}

// FetchTranscript downloads the best publisher-provided transcript for an
// episode and stores it. It returns store.ErrNotFound with a message when the
// feed advertised nothing usable.
func (s *Service) FetchTranscript(ctx context.Context, owner string, episodeID int64) (store.Transcript, error) {
	existing, err := s.store.GetTranscript(ctx, owner, episodeID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Transcript{}, err
	}
	rawRefs, err := s.store.TranscriptRefs(ctx, owner, episodeID)
	if err != nil {
		return store.Transcript{}, err
	}
	var refs []feed.TranscriptRef
	if err := json.Unmarshal([]byte(rawRefs), &refs); err != nil {
		refs = nil
	}
	refs = rankTranscripts(refs)
	if len(refs) == 0 {
		return store.Transcript{}, errors.New("该节目来源没有提供逐字稿；直接收听不受影响")
	}
	attempts := 0
	var lastErr error
	for _, ref := range refs {
		if attempts >= 4 || ctx.Err() != nil {
			break
		}
		attempts++
		body, contentType, finalURL, status, err := s.fetchDocument(ctx, ref.URL, 8<<20)
		if err != nil {
			lastErr = err
			continue
		}
		if status < 200 || status >= 300 {
			lastErr = fmt.Errorf("逐字稿返回 HTTP %d", status)
			continue
		}
		segments, err := transcript.Parse(body, contentType, finalURL)
		if err != nil {
			lastErr = err
			continue
		}
		language := ref.Language
		if language == "" {
			language = guessLanguage(ref.URL)
		}
		stored := make([]store.Segment, 0, len(segments))
		for _, segment := range segments {
			stored = append(stored, store.Segment{
				Index: segment.Index, Start: segment.Start, End: segment.End,
				Speaker: segment.Speaker, Text: segment.Text,
			})
		}
		if err := s.store.SaveTranscript(ctx, owner, episodeID, "feed", language, "",
			transcript.Timed(segments), stored); err != nil {
			return store.Transcript{}, err
		}
		return s.store.GetTranscript(ctx, owner, episodeID)
	}
	if lastErr == nil {
		lastErr = errors.New("来源提供的逐字稿无法读取")
	}
	return store.Transcript{}, fmt.Errorf("未能获取来源逐字稿：%v", lastErr)
}

func (s *Service) fetchDocument(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, string, int, error) {
	canonical, err := feed.CanonicalURL(rawURL)
	if err != nil {
		return nil, "", rawURL, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, "", canonical, 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", canonical, 0, err
	}
	defer resp.Body.Close()
	finalURL := canonical
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, contentType, finalURL, resp.StatusCode, nil
	}
	body, err := safehttp.ReadAll(resp.Body, maxBytes)
	if err != nil {
		return nil, contentType, finalURL, resp.StatusCode, err
	}
	return body, contentType, finalURL, resp.StatusCode, nil
}

func rankTranscripts(refs []feed.TranscriptRef) []feed.TranscriptRef {
	clean := make([]feed.TranscriptRef, 0, len(refs))
	for _, ref := range refs {
		if _, err := feed.CanonicalURL(ref.URL); err == nil {
			clean = append(clean, ref)
		}
	}
	score := func(ref feed.TranscriptRef) int {
		value := 0
		typ := strings.ToLower(ref.Type)
		lowerURL := strings.ToLower(ref.URL)
		switch {
		case strings.Contains(typ, "vtt") || strings.HasSuffix(lowerURL, ".vtt"):
			value += 6
		case strings.Contains(typ, "srt") || strings.HasSuffix(lowerURL, ".srt"):
			value += 5
		case strings.Contains(typ, "json") || strings.HasSuffix(lowerURL, ".json"):
			value += 4
		default:
			value += 1
		}
		lang := strings.ToLower(ref.Language)
		switch {
		case strings.HasPrefix(lang, "zh"):
			value += 3
		case strings.HasPrefix(lang, "en"):
			value += 2
		}
		return value
	}
	sort.SliceStable(clean, func(i, j int) bool { return score(clean[i]) > score(clean[j]) })
	return clean
}

func guessLanguage(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil {
		name := strings.ToLower(parsed.Path)
		if strings.Contains(name, ".zh") || strings.Contains(name, "zh-") {
			return "zh"
		}
		if strings.Contains(name, ".en") || strings.Contains(name, "en-") {
			return "en"
		}
	}
	return ""
}

// ErrUnsafeAudio is returned when a media URL resolves to content that is not
// recognizable as audio. The proxy must never reflect HTML, SVG or other
// active content under an audio response.
var ErrUnsafeAudio = errors.New("音频源返回了非音频内容")

// audioTransportError keeps signed redirect URLs and other upstream details
// out of API responses.
func audioTransportError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("音频源响应超时")
	case errors.Is(err, safehttp.ErrBlocked):
		return errors.New("音频源不是公开可访问的地址")
	case strings.Contains(err.Error(), "too many redirects"):
		return errors.New("音频源重定向次数过多")
	default:
		return errors.New("无法连接音频源")
	}
}

var allowedAudioMIMEs = map[string]struct{}{
	"audio/3gpp": {}, "audio/3gpp2": {}, "audio/aac": {}, "audio/flac": {},
	"audio/m4a": {}, "audio/mp3": {}, "audio/mp4": {}, "audio/mpeg": {},
	"audio/ogg": {}, "audio/opus": {}, "audio/vnd.wave": {}, "audio/wav": {},
	"audio/wave": {}, "audio/webm": {}, "audio/x-aac": {}, "audio/x-flac": {},
	"audio/x-m4a": {}, "audio/x-wav": {}, "application/ogg": {},
}

var audioExtensionMIMEs = map[string]string{
	".3gp": "audio/3gpp", ".3g2": "audio/3gpp2", ".aac": "audio/aac",
	".flac": "audio/flac", ".m4a": "audio/mp4", ".m4b": "audio/mp4",
	".mp3": "audio/mpeg", ".mp4": "audio/mp4", ".oga": "audio/ogg",
	".ogg": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav",
	".wave": "audio/wav", ".webm": "audio/webm",
}

func normalizeMediaType(raw string) string {
	value := strings.TrimSpace(strings.SplitN(raw, ";", 2)[0])
	return strings.ToLower(value)
}

func safeAudioMIME(value string) bool {
	_, ok := allowedAudioMIMEs[normalizeMediaType(value)]
	return ok
}

func genericMIME(value string) bool {
	switch normalizeMediaType(value) {
	case "", "application/octet-stream", "binary/octet-stream":
		return true
	}
	return false
}

func audioExtensionMIME(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return audioExtensionMIMEs[strings.ToLower(path.Ext(u.Path))]
}

// audioBody prepends the sniffed bytes without hiding the upstream closer.
type audioBody struct {
	io.Reader
	io.Closer
}

// sniffAudio reads a small prefix and rejects content that is not a media
// container. When generic is true, the sniffed type itself must be audio
// (MP4/WebM video containers are treated as audio containers for podcasts).
func sniffAudio(resp *http.Response, generic bool) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	sniffed := normalizeMediaType(http.DetectContentType(head[:n]))
	resp.Body = audioBody{Reader: io.MultiReader(bytes.NewReader(head[:n]), resp.Body), Closer: resp.Body}
	if generic {
		switch sniffed {
		case "video/mp4":
			return "audio/mp4", nil
		case "video/webm":
			return "audio/webm", nil
		}
		if safeAudioMIME(sniffed) {
			return sniffed, nil
		}
		return "", ErrUnsafeAudio
	}
	switch sniffed {
	case "video/mp4", "video/webm":
		return sniffed, nil
	}
	if safeAudioMIME(sniffed) || genericMIME(sniffed) {
		return sniffed, nil
	}
	return "", ErrUnsafeAudio
}

// OpenAudio starts a streamed audio request. The caller must close the body.
// Only allowlisted audio types leave this boundary; a generic upstream type
// is accepted only after the first bytes identify a media container.
func (s *Service) OpenAudio(ctx context.Context, owner string, episodeID int64, rangeHeader string) (*http.Response, string, error) {
	audioURL, audioType, err := s.store.EpisodeAudio(ctx, owner, episodeID)
	if err != nil {
		return nil, "", err
	}
	canonical, err := feed.CanonicalURL(audioURL)
	if err != nil {
		return nil, "", errors.New("音频地址不是公开的 HTTP(S) 链接")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", audioType)
	if audioType == "" {
		req.Header.Set("Accept", "audio/*,application/ogg;q=0.9,*/*;q=0.5")
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", audioTransportError(err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, "", fmt.Errorf("音频源返回 HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return resp, "", nil
	}
	declared := normalizeMediaType(resp.Header.Get("Content-Type"))
	if safeAudioMIME(declared) {
		if rangeHeader == "" {
			if _, err := sniffAudio(resp, false); err != nil {
				resp.Body.Close()
				return nil, "", err
			}
		}
		return resp, declared, nil
	}
	if genericMIME(declared) {
		if rangeHeader == "" {
			contentType, err := sniffAudio(resp, true)
			if err != nil {
				resp.Body.Close()
				return nil, "", err
			}
			return resp, contentType, nil
		}
		fallback := audioExtensionMIME(canonical)
		if fallback == "" && safeAudioMIME(audioType) {
			fallback = normalizeMediaType(audioType)
		}
		if fallback == "" {
			resp.Body.Close()
			return nil, "", ErrUnsafeAudio
		}
		return resp, fallback, nil
	}
	resp.Body.Close()
	return nil, "", ErrUnsafeAudio
}
