package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/html"

	"boai/internal/safehttp"
)

// Resolution is the honest outcome of interpreting a pasted link. Only a
// confirmed, parseable feed is "subscribable".
type Resolution struct {
	Input   string `json:"input"`
	Kind    string `json:"kind"`   // feed, show, episode, unsupported, error
	Status  string `json:"status"` // subscribable, needs_feed, unsupported, error
	FeedURL string `json:"feed_url,omitempty"`
	Title   string `json:"title,omitempty"`
	SiteURL string `json:"site_url,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

// DirectoryItem is one result from the public podcast directory.
type DirectoryItem struct {
	Name         string `json:"name"`
	Artist       string `json:"artist"`
	FeedURL      string `json:"feed_url"`
	ArtworkURL   string `json:"artwork_url,omitempty"`
	EpisodeCount int    `json:"episode_count"`
	Source       string `json:"source"`
}

// Resolver turns links into confirmed feeds and searches the public
// directory. Platform coverage is intentionally narrow and reported as such.
// DiscoverShow is one entry from the public podcast chart.
type DiscoverShow struct {
	Rank         int    `json:"rank"`
	Name         string `json:"name"`
	Artist       string `json:"artist"`
	FeedURL      string `json:"feed_url"`
	ArtworkURL   string `json:"artwork_url,omitempty"`
	EpisodeCount int    `json:"episode_count"`
	Source       string `json:"source"`
	CollectionID int64  `json:"-"`
	Summary      string `json:"-"`
	Topic        string `json:"-"`
}

// DiscoverEpisode is the latest episode of a chart show. It is not a separate
// episode chart; the rank follows the show chart position.
type DiscoverEpisode struct {
	Rank        int    `json:"rank"`
	Title       string `json:"title"`
	ShowTitle   string `json:"show_title"`
	FeedURL     string `json:"feed_url"`
	ArtworkURL  string `json:"artwork_url,omitempty"`
	PublishedAt int64  `json:"published_at"`
	Source      string `json:"source"`
}

// DiscoverBoard carries the public chart snapshot shown on the subscription
// page. Source fields are exposed so the UI never implies listener counts.
type DiscoverBoard struct {
	Country                 string            `json:"country"`
	Source                  string            `json:"source"`
	EpisodeSource           string            `json:"episode_source"`
	EnglishSource           string            `json:"english_source"`
	EnglishError            string            `json:"english_error,omitempty"`
	UpdatedAt               int64             `json:"updated_at"`
	Shows                   []DiscoverShow    `json:"shows"`
	EnglishTech             []DiscoverShow    `json:"english_tech"`
	EnglishBusiness         []DiscoverShow    `json:"english_business"`
	EnglishTechEpisodes     []DiscoverEpisode `json:"english_tech_episodes"`
	EnglishBusinessEpisodes []DiscoverEpisode `json:"english_business_episodes"`
	EnglishEpisodeError     string            `json:"english_episode_error,omitempty"`
	Episodes                []DiscoverEpisode `json:"episodes"`
}

// Resolver turns links into confirmed feeds and searches the public
// directory. Platform coverage is intentionally narrow and reported as such.
type Resolver struct {
	Client     *safehttp.Client
	ITunesBase string
	Timeout    time.Duration
	MaxBytes   int64

	discoverMu   sync.Mutex
	discoverAt   time.Time
	discoverData DiscoverBoard
}

// NewResolver builds a resolver around a safe HTTP client.
func NewResolver(client *safehttp.Client, itunesBase string) *Resolver {
	return &Resolver{
		Client:     client,
		ITunesBase: strings.TrimRight(itunesBase, "/"),
		Timeout:    25 * time.Second,
		MaxBytes:   5 << 20,
	}
}

var idPattern = regexp.MustCompile(`(?i)/id(\d+)`)

// Resolve interprets a pasted URL. It never reports success without parsing
// the candidate feed.
func (r *Resolver) Resolve(ctx context.Context, raw string) Resolution {
	input := strings.TrimSpace(raw)
	u, err := safehttp.ValidateURL(input, r.Client.AllowPrivate())
	if err != nil {
		return Resolution{Input: input, Kind: "error", Status: "error", Message: "只支持公开的 HTTP(S) 链接"}
	}
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)

	switch {
	case isHost(host, "podcasts.apple.com"):
		return r.resolveApple(ctx, u)
	case isHost(host, "open.spotify.com"):
		return Resolution{Input: input, Kind: "unsupported", Status: "unsupported", Source: "spotify",
			Message: "Spotify 的公开链接无法确认可更新的订阅源，首版不建立订阅"}
	case isHost(host, "youtube.com"), isHost(host, "youtu.be"), isHost(host, "music.youtube.com"):
		return Resolution{Input: input, Kind: "unsupported", Status: "unsupported", Source: "youtube",
			Message: "YouTube 链接无法确认播客订阅源，首版不建立订阅"}
	}

	source := "site"
	if isHost(host, "xiaoyuzhoufm.com") {
		source = "xiaoyuzhou"
	}
	res := r.tryPageAndFeeds(ctx, u, source)
	if looksLikeEpisodePath(path) || u.Query().Get("i") != "" {
		res.Input = input
		switch res.Status {
		case "subscribable":
			res.Kind = "feed"
			res.Message = "这是单集链接；已确认节目级订阅源，可按节目订阅"
		case "needs_feed":
			res.Kind = "episode"
			if source == "xiaoyuzhou" {
				res.Message = "这看起来是小宇宙的单集链接。小宇宙网页不暴露节目级 RSS；如主播公开了订阅源，请直接粘贴该 RSS 地址"
			} else {
				res.Message = "这看起来是单集链接。未能确认节目级订阅源，因此不会建立订阅"
			}
		}
	} else if source == "xiaoyuzhou" && res.Status == "needs_feed" {
		res.Message = "小宇宙网页不提供可确认的公开 RSS；如主播公开了订阅源（常见于 feed.xyzfm.space），请直接粘贴该 RSS 地址"
	}
	res.Input = input
	return res
}

// tryPageAndFeeds always returns a resolution; only Status=="subscribable"
// means the feed was confirmed.
func (r *Resolver) tryPageAndFeeds(ctx context.Context, u *url.URL, source string) Resolution {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	body, contentType, finalURL, status, err := r.fetchPage(ctx, u.String())
	if err != nil {
		return Resolution{Input: u.String(), Kind: "error", Status: "error", Source: source,
			Message: "无法读取该链接；请检查网络或改用公开的订阅源地址"}
	}
	if status >= 400 {
		return Resolution{Input: u.String(), Kind: "error", Status: "error", Source: source,
			Message: fmt.Sprintf("链接返回 HTTP %d，未建立订阅", status)}
	}
	if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return Resolution{Input: u.String(), Kind: "unsupported", Status: "unsupported", Source: source,
			Message: "这是音频或视频文件，不是可更新的订阅源"}
	}
	if parsed, parseErr := Parse(body, finalURL); parseErr == nil {
		if !parsed.Playable() {
			return Resolution{Input: u.String(), Kind: "unsupported", Status: "unsupported", Source: source,
				Title: parsed.Title, Message: "这是可解析的文章订阅源，但没有音频单集；首版只订阅播客音频"}
		}
		return Resolution{Input: u.String(), Kind: "feed", Status: "subscribable", FeedURL: finalURL,
			Title: parsed.Title, SiteURL: parsed.SiteURL, Source: source,
			Message: "已确认订阅源，可读取更新"}
	}

	candidates, pageTitle := extractCandidates(body, finalURL)
	for _, candidate := range candidates {
		if feedURL, feedTitle, err := r.confirmFeed(ctx, candidate); err == nil {
			return Resolution{Input: u.String(), Kind: "feed", Status: "subscribable", FeedURL: feedURL,
				Title: feedTitle, SiteURL: finalURL, Source: source,
				Message: "已在页面中发现并确认订阅源"}
		}
	}
	// Self-hosted sites often expose a conventional path without advertising
	// it in markup. Try a small, bounded list.
	if origin := originOf(u); origin != "" {
		for _, path := range []string{"/feed", "/feed.xml", "/rss.xml", "/atom.xml", "/index.xml"} {
			if feedURL, feedTitle, err := r.confirmFeed(ctx, origin+path); err == nil {
				return Resolution{Input: u.String(), Kind: "feed", Status: "subscribable", FeedURL: feedURL,
					Title: feedTitle, SiteURL: finalURL, Source: source,
					Message: "已在该站点常见的订阅地址上确认订阅源"}
			}
		}
	}
	if pageTitle == "" {
		pageTitle = u.Hostname()
	}
	return Resolution{Input: u.String(), Kind: "show", Status: "needs_feed", Source: source,
		Message: fmt.Sprintf("页面「%s」未提供可确认的订阅源；请改用 RSS／Atom 地址", truncate(pageTitle, 80))}
}

func (r *Resolver) confirmFeed(ctx context.Context, rawURL string) (string, string, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return "", "", err
	}
	body, _, finalURL, status, err := r.fetchPage(ctx, canonical)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusOK {
		return "", "", fmt.Errorf("feed returned HTTP %d", status)
	}
	parsed, err := Parse(body, finalURL)
	if err != nil {
		return "", "", err
	}
	if !parsed.Playable() {
		return "", "", fmt.Errorf("feed has no audio episodes")
	}
	return finalURL, parsed.Title, nil
}

func (r *Resolver) fetchPage(ctx context.Context, rawURL string) (body []byte, contentType, finalURL string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", rawURL, 0, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, text/html;q=0.8, */*;q=0.1")
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, "", rawURL, 0, err
	}
	defer resp.Body.Close()
	finalURL = rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if resp.StatusCode >= 300 || strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return nil, contentType, finalURL, resp.StatusCode, nil
	}
	maxBytes := r.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}
	body, err = safehttp.ReadAll(resp.Body, maxBytes)
	if err != nil {
		return nil, contentType, finalURL, resp.StatusCode, err
	}
	return body, contentType, finalURL, resp.StatusCode, nil
}

func (r *Resolver) resolveApple(ctx context.Context, u *url.URL) Resolution {
	ctx, cancel := context.WithTimeout(ctx, 2*r.Timeout)
	defer cancel()
	out := Resolution{Input: u.String(), Kind: "show", Status: "needs_feed", Source: "apple_podcasts"}
	episodeID := strings.TrimSpace(u.Query().Get("i"))
	if episodeID != "" {
		out.Kind = "episode"
	}
	match := idPattern.FindStringSubmatch(u.Path)
	collectionID := ""
	if len(match) == 2 {
		collectionID = match[1]
	}
	if collectionID == "" {
		if episodeID != "" {
			out.Message = "该链接只带单集 ID，公开目录无法据此反查节目；请改用含 /id 的节目链接或直接粘贴 RSS"
		} else {
			out.Message = "无法从 Apple Podcasts 链接中识别节目 ID"
		}
		return out
	}
	results, err := r.itunesLookup(ctx, collectionID, "podcast")
	if err != nil {
		out.Kind, out.Status = "error", "error"
		out.Message = "Apple Podcasts 公开目录不可用（网络或服务失败），稍后重试或直接粘贴 RSS"
		return out
	}
	if len(results) == 0 {
		out.Message = "Apple Podcasts 公开目录没有返回该节目；可能地区不匹配或链接有误"
		return out
	}
	item := results[0]
	feedURL := item.FeedURL
	title := item.CollectionName
	if title == "" {
		title = item.TrackName
	}
	episodeTitle, episodeConfirmed := "", false
	if episodeID != "" {
		// Episode IDs are not directly lookup-able; scan the show's episode
		// results instead. The window is bounded by the directory, so a miss
		// means "not confirmed", never "does not exist".
		if episodes, epsErr := r.itunesLookup(ctx, collectionID, "podcastEpisode"); epsErr == nil {
			for _, episode := range episodes {
				if strconv.FormatInt(episode.TrackID, 10) == episodeID {
					episodeTitle, episodeConfirmed = episode.TrackName, true
					break
				}
			}
		}
	}
	if feedURL == "" {
		out.Message = "Apple Podcasts 目录返回了节目，但没有公开订阅源地址；首版不会用私有接口补全"
		return out
	}
	confirmed, confirmedTitle, confirmErr := r.confirmFeed(ctx, feedURL)
	if confirmErr != nil {
		out.Message = "已在 Apple Podcasts 目录找到该节目，但它的公开订阅源当前无法读取或解析；请检查网络后重试，或直接粘贴 RSS"
		return out
	}
	if confirmedTitle != "" {
		title = confirmedTitle
	}
	out.Kind = "feed"
	out.Status = "subscribable"
	out.FeedURL = confirmed
	out.Title = title
	switch {
	case episodeID == "":
		out.Message = "已通过 Apple Podcasts 公开目录确认订阅源"
	case episodeConfirmed:
		out.Message = fmt.Sprintf("已确认单集《%s》所属节目和订阅源，可按节目订阅", truncate(episodeTitle, 60))
	default:
		out.Message = "已确认单集所属节目的订阅源；该单集未出现在公开目录返回的单集列表中，节目订阅仍可用"
	}
	return out
}

type itunesResult struct {
	TrackID        int64  `json:"trackId"`
	CollectionID   int64  `json:"collectionId"`
	CollectionName string `json:"collectionName"`
	TrackName      string `json:"trackName"`
	ArtistName     string `json:"artistName"`
	FeedURL        string `json:"feedUrl"`
	ArtworkURL600  string `json:"artworkUrl600"`
	ArtworkURL100  string `json:"artworkUrl100"`
	TrackCount     int    `json:"trackCount"`
	WrapperType    string `json:"wrapperType"`
	Kind           string `json:"kind"`
	ReleaseDate    string `json:"releaseDate"`
}

type itunesResponse struct {
	ResultCount int            `json:"resultCount"`
	Results     []itunesResult `json:"results"`
}

func (r *Resolver) itunesLookup(ctx context.Context, id, entity string) ([]itunesResult, error) {
	return r.itunesLookupLimited(ctx, id, entity, 0)
}

func (r *Resolver) itunesLookupLimited(ctx context.Context, id, entity string, limit int) ([]itunesResult, error) {
	return r.itunesLookupCountry(ctx, id, entity, limit, "cn")
}

func (r *Resolver) itunesLookupCountry(ctx context.Context, id, entity string, limit int, country string) ([]itunesResult, error) {
	values := url.Values{}
	values.Set("id", id)
	values.Set("entity", entity)
	if country == "" {
		country = "cn"
	}
	values.Set("country", country)
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	return r.itunes(ctx, "/lookup", values)
}

// SearchDirectory queries the public iTunes podcast directory. Failures are
// returned to the caller so library search can still work.
func (r *Resolver) SearchDirectory(ctx context.Context, term string) ([]DirectoryItem, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	values := url.Values{}
	values.Set("term", term)
	values.Set("media", "podcast")
	values.Set("entity", "podcast")
	values.Set("country", "cn")
	values.Set("limit", "12")
	results, err := r.itunes(ctx, "/search", values)
	if err != nil {
		return nil, err
	}
	items := make([]DirectoryItem, 0, len(results))
	for _, item := range results {
		feedURL, err := CanonicalURL(item.FeedURL)
		if err != nil {
			continue
		}
		artwork := item.ArtworkURL600
		if artwork == "" {
			artwork = item.ArtworkURL100
		}
		items = append(items, DirectoryItem{
			Name:         item.CollectionName,
			Artist:       item.ArtistName,
			FeedURL:      feedURL,
			ArtworkURL:   artwork,
			EpisodeCount: item.TrackCount,
			Source:       "itunes",
		})
	}
	return items, nil
}

func (r *Resolver) itunes(ctx context.Context, path string, values url.Values) ([]itunesResult, error) {
	raw := r.ITunesBase + path + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	status, _, body, err := r.Client.Fetch(req, 2<<20)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("directory returned HTTP %d", status)
	}
	var payload itunesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Results, nil
}

const discoverTTL = 15 * time.Minute

type chartEntry struct {
	Name    struct{ Label string } `json:"im:name"`
	Summary struct{ Label string } `json:"summary"`
	Images  []struct {
		Label string `json:"label"`
	} `json:"im:image"`
	Artist struct{ Label string } `json:"im:artist"`
	ID     struct {
		Attributes struct {
			IMID string `json:"im:id"`
		} `json:"attributes"`
	} `json:"id"`
}

type chartPayload struct {
	Feed struct {
		Entry []chartEntry `json:"entry"`
	} `json:"feed"`
}

// Discover returns the Apple Podcasts China chart and the latest episodes of
// its top shows. Apple exposes chart position, not listener counts, so these
// are labeled as ranks. A short cache keeps opening the page cheap.
func (r *Resolver) Discover(ctx context.Context) (DiscoverBoard, error) {
	r.discoverMu.Lock()
	defer r.discoverMu.Unlock()
	if !r.discoverAt.IsZero() && time.Since(r.discoverAt) < discoverTTL {
		return r.discoverData, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	chart, err := r.fetchChart(ctx)
	if err != nil {
		return DiscoverBoard{}, err
	}
	ids := make([]string, 0, len(chart))
	order := make([]string, 0, len(chart))
	seen := make(map[string]bool, len(chart))
	for _, entry := range chart {
		id := strings.TrimSpace(entry.ID.Attributes.IMID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		order = append(order, id)
	}
	if len(ids) == 0 {
		return DiscoverBoard{}, errors.New("directory returned an empty chart")
	}
	lookup, err := r.itunesLookup(ctx, strings.Join(ids, ","), "podcast")
	if err != nil {
		return DiscoverBoard{}, err
	}
	byID := make(map[int64]itunesResult, len(lookup))
	for _, item := range lookup {
		if item.CollectionID != 0 && item.FeedURL != "" {
			byID[item.CollectionID] = item
		}
	}
	board := DiscoverBoard{
		Country:                 "cn",
		Source:                  "Apple Podcasts 中国区热门播客榜",
		EpisodeSource:           "热门节目最新一期（按节目榜位排序）",
		EnglishSource:           "Apple Podcasts 美国区科技榜 / 商业榜（榜位，不是收听人数）",
		UpdatedAt:               time.Now().Unix(),
		Shows:                   []DiscoverShow{},
		EnglishTech:             []DiscoverShow{},
		EnglishBusiness:         []DiscoverShow{},
		EnglishTechEpisodes:     []DiscoverEpisode{},
		EnglishBusinessEpisodes: []DiscoverEpisode{},
		Episodes:                []DiscoverEpisode{},
	}
	rank := 0
	episodeIDs := make([]string, 0, 10)
	showByCollection := make(map[int64]DiscoverShow, 10)
	for _, rawID := range order {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			continue
		}
		item, ok := byID[id]
		if !ok {
			continue
		}
		feedURL, err := CanonicalURL(item.FeedURL)
		if err != nil || feedURL == "" {
			continue
		}
		rank++
		artwork := item.ArtworkURL600
		if artwork == "" {
			artwork = item.ArtworkURL100
		}
		show := DiscoverShow{
			Rank:         rank,
			Name:         item.CollectionName,
			Artist:       item.ArtistName,
			FeedURL:      feedURL,
			ArtworkURL:   artwork,
			EpisodeCount: item.TrackCount,
			Source:       "itunes",
		}
		board.Shows = append(board.Shows, show)
		if len(episodeIDs) < 10 {
			episodeIDs = append(episodeIDs, strconv.FormatInt(id, 10))
			showByCollection[id] = show
		}
		if len(board.Shows) >= 12 {
			break
		}
	}
	if len(board.Shows) == 0 {
		return DiscoverBoard{}, errors.New("directory chart entries have no feeds")
	}
	if len(episodeIDs) > 0 {
		episodes, err := r.itunesLookupLimited(ctx, strings.Join(episodeIDs, ","), "podcastEpisode", 1)
		if err == nil {
			taken := make(map[int64]bool, len(episodeIDs))
			for _, item := range episodes {
				if item.WrapperType != "podcastEpisode" || item.TrackName == "" {
					continue
				}
				show, ok := showByCollection[item.CollectionID]
				if !ok || taken[item.CollectionID] {
					continue
				}
				taken[item.CollectionID] = true
				published := int64(0)
				if parsed, err := time.Parse(time.RFC3339, item.ReleaseDate); err == nil {
					published = parsed.Unix()
				}
				board.Episodes = append(board.Episodes, DiscoverEpisode{
					Rank:        show.Rank,
					Title:       item.TrackName,
					ShowTitle:   show.Name,
					FeedURL:     show.FeedURL,
					ArtworkURL:  show.ArtworkURL,
					PublishedAt: published,
					Source:      "itunes",
				})
			}
		}
	}
	tech, business, englishErr := r.discoverEnglish(ctx)
	if englishErr != nil {
		board.EnglishError = "海外英文榜单暂时不可用，中文榜单和搜索不受影响"
	} else {
		board.EnglishTech = tech
		board.EnglishBusiness = business
		techEpisodes, techErr := r.englishLatestEpisodes(ctx, tech)
		businessEpisodes, businessErr := r.englishLatestEpisodes(ctx, business)
		if techErr != nil && businessErr != nil {
			board.EnglishEpisodeError = "英文单集暂时不可用，可以先进入节目详情查看"
		} else {
			board.EnglishTechEpisodes = techEpisodes
			board.EnglishBusinessEpisodes = businessEpisodes
		}
	}
	r.discoverAt = time.Now()
	r.discoverData = board
	return board, nil
}

// Apple genre IDs: 1318 Technology, 1321 Business.
const (
	appleTechGenre     = 1318
	appleBusinessGenre = 1321
)

// discoverEnglish collects the US Technology and Business charts. Apple only
// exposes chart position, so the UI must not present these as listener counts.
// The two sections are de-duplicated in chart order.
func (r *Resolver) discoverEnglish(ctx context.Context) ([]DiscoverShow, []DiscoverShow, error) {
	techChart, techErr := r.fetchChartFor(ctx, "us", appleTechGenre)
	businessChart, businessErr := r.fetchChartFor(ctx, "us", appleBusinessGenre)
	if techErr != nil && businessErr != nil {
		return nil, nil, errors.New("english charts unavailable")
	}
	ids := make([]string, 0, len(techChart)+len(businessChart))
	seenID := make(map[string]bool, cap(ids))
	for _, chart := range [][]chartEntry{techChart, businessChart} {
		for _, entry := range chart {
			id := strings.TrimSpace(entry.ID.Attributes.IMID)
			if id == "" || seenID[id] {
				continue
			}
			seenID[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil, errors.New("english charts are empty")
	}
	lookup, err := r.itunesLookupCountry(ctx, strings.Join(ids, ","), "podcast", 0, "us")
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]itunesResult, len(lookup))
	for _, item := range lookup {
		if item.CollectionID != 0 && item.FeedURL != "" {
			byID[item.CollectionID] = item
		}
	}
	used := make(map[int64]bool, len(byID))
	// Keep more candidates than we display so the topic filter can skip
	// general-interest shows without leaving the English panel short.
	tech := buildEnglishShows(techChart, byID, used, 16, "tech")
	business := buildEnglishShows(businessChart, byID, used, 16, "business")
	if len(tech) == 0 && len(business) == 0 {
		return nil, nil, errors.New("english chart entries have no feeds")
	}
	return tech, business, nil
}

func buildEnglishShows(chart []chartEntry, byID map[int64]itunesResult, used map[int64]bool, limit int, topic string) []DiscoverShow {
	shows := make([]DiscoverShow, 0, limit)
	rank := 0
	for _, entry := range chart {
		id, err := strconv.ParseInt(strings.TrimSpace(entry.ID.Attributes.IMID), 10, 64)
		if err != nil || used[id] {
			continue
		}
		item, ok := byID[id]
		if !ok {
			continue
		}
		feedURL, err := CanonicalURL(item.FeedURL)
		if err != nil || feedURL == "" {
			continue
		}
		used[id] = true
		rank++
		artwork := item.ArtworkURL600
		if artwork == "" {
			artwork = item.ArtworkURL100
		}
		shows = append(shows, DiscoverShow{
			Rank:         rank,
			Name:         item.CollectionName,
			Artist:       item.ArtistName,
			FeedURL:      feedURL,
			ArtworkURL:   artwork,
			EpisodeCount: item.TrackCount,
			Source:       "itunes_us",
			CollectionID: id,
			Summary:      strings.TrimSpace(entry.Summary.Label),
			Topic:        topic,
		})
		if len(shows) >= limit {
			break
		}
	}
	return shows
}

// englishLatestEpisodes fetches the latest episode of each ranked show. The
// result is still ordered by the show chart position, not by episode plays.
func (r *Resolver) englishLatestEpisodes(ctx context.Context, shows []DiscoverShow) ([]DiscoverEpisode, error) {
	if len(shows) == 0 {
		return []DiscoverEpisode{}, nil
	}
	ids := make([]string, 0, len(shows))
	byID := make(map[int64]DiscoverShow, len(shows))
	for _, show := range shows {
		if show.CollectionID == 0 {
			continue
		}
		ids = append(ids, strconv.FormatInt(show.CollectionID, 10))
		byID[show.CollectionID] = show
	}
	if len(ids) == 0 {
		return []DiscoverEpisode{}, nil
	}
	results, err := r.itunesLookupCountry(ctx, strings.Join(ids, ","), "podcastEpisode", 1, "us")
	if err != nil {
		return nil, err
	}
	episodes := make([]DiscoverEpisode, 0, 10)
	used := make(map[int64]bool, len(shows))
	for _, item := range results {
		// Apple returns the podcast itself ("track") before its latest
		// episode. Accepting the track would show the show name as the
		// episode title and open the show page instead of the episode.
		if item.WrapperType != "podcastEpisode" || item.TrackName == "" {
			continue
		}
		show, ok := byID[item.CollectionID]
		if !ok || used[item.CollectionID] {
			continue
		}
		if !englishTopicRelevant(show, item.TrackName) {
			continue
		}
		used[item.CollectionID] = true
		published := int64(0)
		if parsed, err := time.Parse(time.RFC3339, item.ReleaseDate); err == nil {
			published = parsed.Unix()
		}
		episodes = append(episodes, DiscoverEpisode{
			Rank:        show.Rank,
			Title:       item.TrackName,
			ShowTitle:   show.Name,
			FeedURL:     show.FeedURL,
			ArtworkURL:  show.ArtworkURL,
			PublishedAt: published,
			Source:      "itunes_us",
		})
		if len(episodes) >= 10 {
			break
		}
	}
	return episodes, nil
}

// englishTopicRelevant keeps the English panel focused on AI, technology,
// internet and finance/business shows. The US Technology chart also carries
// general-interest shows, so the chart category alone is not enough. The show
// summary, directory genres and latest episode title are all considered.
func englishTopicRelevant(show DiscoverShow, episodeTitle string) bool {
	// Genres are intentionally not used here: every show in the US Technology
	// chart carries the "Technology" genre, so it cannot distinguish a tech
	// show from a general-interest show placed on the same chart.
	haystack := strings.Join([]string{
		show.Name,
		show.Artist,
		show.Summary,
		episodeTitle,
	}, " ")
	topic := show.Topic
	if topic == "" {
		topic = "tech"
	}
	return hasTopicWord(haystack, englishTopicWords(topic))
}

var englishTechWords = map[string]struct{}{}
var englishBusinessWords = map[string]struct{}{}

func init() {
	for _, word := range []string{
		"ai", "ais", "artificial", "intelligence", "tech", "technology",
		"technologies", "internet", "software", "hardware", "computer",
		"computers", "computing", "chip", "chips", "semiconductor",
		"semiconductors", "gpu", "gpus", "llm", "llms", "algorithm",
		"algorithms", "data", "cloud", "cyber", "security", "robot",
		"robots", "robotics", "automation", "automated", "gadget",
		"gadgets", "device", "devices", "smartphone", "platform",
		"platforms", "online", "digital", "silicon", "startup", "startups",
		"venture", "capital", "crypto", "blockchain", "nvidia", "openai",
		"anthropic", "databricks", "microsoft", "google", "amazon",
		"jensen", "huang", "company", "companies", "business", "businesses",
		"economy", "economic", "finance", "financial", "market", "markets",
		"invest", "investing", "investment", "investor", "investors",
		"stock", "stocks", "trade", "trading", "industry", "industries",
		"founder", "founders", "product", "products", "growth", "revenue",
		"profit", "earnings", "money", "bank", "banking", "fund", "funds",
		"fintech", "ipo", "merger", "acquisition", "wealth", "ceo",
	} {
		englishTechWords[word] = struct{}{}
	}
	// The US Business chart also carries general interview shows. Require a
	// finance/markets signal so the "finance" section stays on topic.
	for _, word := range []string{
		"finance", "financial", "invest", "investing", "investment",
		"investor", "investors", "market", "markets", "stock", "stocks",
		"economy", "economic", "economics", "money", "wealth", "bank",
		"banking", "fintech", "crypto", "blockchain", "fund", "funds",
		"revenue", "profit", "earnings", "trading", "trade", "trader",
		"traders", "capital", "venture", "ipo", "merger", "acquisition",
		"deal", "deals", "dividend", "dividends", "portfolio", "bond",
		"bonds", "interest", "inflation", "fed", "equity", "equities",
		"commodity", "commodities", "tariff", "tariffs", "gdp",
		"recession", "cash", "debt", "loan", "loans", "mortgage", "tax",
		"taxes", "retirement", "savings", "salary", "wage", "wages",
		"price", "prices", "pricing", "startup", "startups",
	} {
		englishBusinessWords[word] = struct{}{}
	}
}

func englishTopicWords(topic string) map[string]struct{} {
	if topic == "business" {
		return englishBusinessWords
	}
	return englishTechWords
}

func hasTopicWord(value string, words map[string]struct{}) bool {
	tokens := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, token := range tokens {
		if _, ok := words[token]; ok {
			return true
		}
	}
	return false
}

func (r *Resolver) fetchChart(ctx context.Context) ([]chartEntry, error) {
	return r.fetchChartFor(ctx, "cn", 0)
}

func (r *Resolver) fetchChartFor(ctx context.Context, country string, genre int) ([]chartEntry, error) {
	country = strings.ToLower(strings.TrimSpace(country))
	if country == "" {
		return nil, errors.New("chart country is empty")
	}
	raw := fmt.Sprintf("%s/%s/rss/toppodcasts/limit=20/json", r.ITunesBase, url.PathEscape(country))
	if genre > 0 {
		raw = fmt.Sprintf("%s/%s/rss/toppodcasts/limit=20/genre=%d/json", r.ITunesBase, url.PathEscape(country), genre)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	status, _, body, err := r.Client.Fetch(req, r.MaxBytes)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("chart returned HTTP %d", status)
	}
	var payload chartPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Feed.Entry, nil
}

var feedMIMETypes = map[string]bool{
	"application/rss+xml":  true,
	"application/atom+xml": true,
	"application/rdf+xml":  true,
	"application/xml":      true,
	"text/xml":             true,
}

func extractCandidates(body []byte, pageURL string) ([]string, string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, ""
	}
	base, _ := url.Parse(pageURL)
	seen := make(map[string]bool)
	var candidates []string
	title := ""
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || len(candidates) >= 8 {
			return
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "title":
				if title == "" && node.FirstChild != nil {
					title = strings.TrimSpace(node.FirstChild.Data)
				}
			case "link":
				rel := strings.ToLower(attrOf(node, "rel"))
				typ := strings.ToLower(strings.TrimSpace(strings.Split(attrOf(node, "type"), ";")[0]))
				href := attrOf(node, "href")
				if href != "" && strings.Contains(rel, "alternate") && feedMIMETypes[typ] {
					if resolved := resolveAgainst(base, href); resolved != "" && !seen[resolved] {
						seen[resolved] = true
						candidates = append(candidates, resolved)
					}
				}
			case "a":
				if len(candidates) < 6 {
					href := attrOf(node, "href")
					if href != "" && looksLikeFeedHref(href) {
						if resolved := resolveAgainst(base, href); resolved != "" && !seen[resolved] {
							seen[resolved] = true
							candidates = append(candidates, resolved)
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return candidates, title
}

func attrOf(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

func resolveAgainst(base *url.URL, href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func looksLikeFeedHref(href string) bool {
	lower := strings.ToLower(href)
	if strings.Contains(lower, ".mp3") || strings.Contains(lower, ".m4a") || strings.Contains(lower, ".mp4") {
		return false
	}
	return strings.Contains(lower, ".xml") || strings.Contains(lower, ".rss") || strings.Contains(lower, ".atom") ||
		strings.Contains(lower, "/feed") || strings.Contains(lower, "/rss") || strings.Contains(lower, "format=rss") ||
		strings.Contains(lower, "feed=")
}

func originOf(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func isHost(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// looksLikeEpisodePath reports whether a page path points at one episode.
// A bare listing such as /episodes/ is not an episode.
func looksLikeEpisodePath(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range segments {
		switch segment {
		case "episode", "episodes", "ep", "shorts", "live":
			if i < len(segments)-1 && segments[i+1] != "" {
				return true
			}
		case "watch":
			return true
		}
	}
	return false
}
