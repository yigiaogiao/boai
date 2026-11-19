package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boai/internal/api"
	"boai/internal/service"
	"boai/internal/store"
)

const feedTemplate = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"
	xmlns:podcast="https://podcastindex.org/namespace/1.0">
<channel>
	<title>接口测试台</title>
	<link>http://%[1]s/show</link>
	<description>用于 API 测试的订阅源</description>
	<item>
		<title>第一集：倾听</title>
		<guid>api-episode-1</guid>
		<description>关于倾听的一集</description>
		<pubDate>Mon, 17 Nov 2025 09:00:00 +0000</pubDate>
		<enclosure url="http://%[1]s/episode.wav" type="audio/wav" length="2880044"/>
		<itunes:duration>03:00</itunes:duration>
		<podcast:transcript url="http://%[1]s/episode.vtt" type="text/vtt" language="zh-CN"/>
	</item>
	<item>
		<title>第二集：选择</title>
		<guid>api-episode-2</guid>
		<description>没有逐字稿的一集</description>
		<pubDate>Sun, 16 Nov 2025 09:00:00 +0000</pubDate>
		<enclosure url="http://%[1]s/episode.wav" type="audio/wav" length="2880044"/>
		<itunes:duration>01:30</itunes:duration>
	</item>
</channel>
</rss>`

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

func newAPIRig(t *testing.T) (http.Handler, string) {
	t.Helper()
	handler, feedURL, _ := newAPIRigWithDir(t)
	return handler, feedURL
}

func newAPIRigWithDir(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			if r.Header.Get("If-None-Match") == `"api-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"api-v1"`)
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
			_, _ = fmt.Fprintf(w, feedTemplate, r.Host)
		case "/episode.wav":
			http.ServeFile(w, r, fixturePath("episode.wav"))
		case "/episode.vtt":
			data, err := os.ReadFile(fixturePath("episode.vtt"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(feedServer.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "api-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	downloadDir := filepath.Join(t.TempDir(), "downloads")
	svc := service.New(st, service.Options{
		UpdateInterval: time.Hour,
		HTTPTimeout:    5 * time.Second,
		AllowPrivate:   true,
		DownloadDir:    downloadDir,
	})
	handler := api.New(svc, api.Options{
		WebDir:          t.TempDir(),
		UserHeader:      "X-HC-User-ID",
		TrustUserHeader: true,
		Version:         "test-version",
	}).Handler()
	return handler, feedServer.URL, downloadDir
}

func do(t *testing.T, handler http.Handler, method, path, owner string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if owner != "" {
		req.Header.Set("X-HC-User-ID", owner)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return value
}

func TestPreviewShowStaysHiddenUntilSubscribed(t *testing.T) {
	handler, feedURL := newAPIRig(t)
	owner := "preview-owner"

	body, _ := json.Marshal(map[string]any{"feed_url": feedURL + "/feed.xml"})
	preview := decode[struct {
		Feed       store.Feed      `json:"feed"`
		Episodes   []store.Episode `json:"episodes"`
		Subscribed bool            `json:"subscribed"`
	}](t, do(t, handler, http.MethodPost, "/api/discover/show", owner, body, nil), http.StatusOK)
	if preview.Subscribed || len(preview.Episodes) != 2 || preview.Feed.Title != "接口测试台" {
		t.Fatalf("preview = %#v", preview)
	}
	library := decode[struct {
		Library struct {
			Feeds       int `json:"feeds"`
			Episodes    int `json:"episodes"`
			NewEpisodes int `json:"new_episodes"`
		} `json:"library"`
	}](t, do(t, handler, http.MethodGet, "/api/status", owner, nil, nil), http.StatusOK).Library
	if library.Feeds != 0 || library.Episodes != 0 || library.NewEpisodes != 0 {
		t.Fatalf("hidden preview inflated library status: %#v", library)
	}

	feeds := decode[struct {
		Feeds []store.Feed `json:"feeds"`
	}](t, do(t, handler, http.MethodGet, "/api/feeds", owner, nil, nil), http.StatusOK)
	if len(feeds.Feeds) != 0 {
		t.Fatalf("hidden preview leaked into subscriptions: %#v", feeds.Feeds)
	}
	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=all", owner, nil, nil), http.StatusOK)
	if len(episodes.Episodes) != 0 {
		t.Fatalf("hidden preview leaked into inbox: %#v", episodes.Episodes)
	}
	detail := decode[struct {
		Episode store.Episode `json:"episode"`
	}](t, do(t, handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d", preview.Episodes[0].ID), owner, nil, nil), http.StatusOK)
	if detail.Episode.Title == "" {
		t.Fatalf("preview episode is not readable: %#v", detail)
	}

	subscribed := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, body, nil), http.StatusCreated)
	if subscribed.Feed.ID != preview.Feed.ID {
		t.Fatalf("subscription id = %d, want preview id %d", subscribed.Feed.ID, preview.Feed.ID)
	}
	feeds = decode[struct {
		Feeds []store.Feed `json:"feeds"`
	}](t, do(t, handler, http.MethodGet, "/api/feeds", owner, nil, nil), http.StatusOK)
	if len(feeds.Feeds) != 1 {
		t.Fatalf("visible subscriptions = %d, want 1", len(feeds.Feeds))
	}
	episodes = decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=all", owner, nil, nil), http.StatusOK)
	if len(episodes.Episodes) != 2 {
		t.Fatalf("inbox episodes after subscribing = %d, want 2", len(episodes.Episodes))
	}
}

func TestAPISubscriptionPlaybackAndIsolation(t *testing.T) {
	handler, feedURL := newAPIRig(t)
	owner := "lazycat-user-a"

	if recorder := do(t, handler, http.MethodGet, "/api/feeds", "", nil, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing user header status = %d, want 401", recorder.Code)
	}

	body, _ := json.Marshal(map[string]any{"feed_url": feedURL + "/feed.xml"})
	subscribed := decode[struct {
		Feed        store.Feed `json:"feed"`
		NewEpisodes int        `json:"new_episodes"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, body, nil), http.StatusCreated)
	if subscribed.NewEpisodes != 2 || subscribed.Feed.EpisodeCount != 2 || subscribed.Feed.Title != "接口测试台" {
		t.Fatalf("subscribe = %#v", subscribed)
	}

	status := decode[struct {
		Version string `json:"version"`
		Library struct {
			Feeds       int `json:"feeds"`
			Episodes    int `json:"episodes"`
			NewEpisodes int `json:"new_episodes"`
		} `json:"library"`
		AI struct {
			Configured bool `json:"configured"`
		} `json:"ai"`
	}](t, do(t, handler, http.MethodGet, "/api/status", owner, nil, nil), http.StatusOK)
	if status.Version != "test-version" || status.Library.Feeds != 1 || status.Library.Episodes != 2 ||
		status.Library.NewEpisodes != 2 || status.AI.Configured {
		t.Fatalf("status = %#v", status)
	}

	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=new", owner, nil, nil), http.StatusOK)
	if len(episodes.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes.Episodes))
	}
	limited := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=new&limit=1", owner, nil, nil), http.StatusOK)
	if len(limited.Episodes) != 1 {
		t.Fatalf("limited episodes = %d, want 1", len(limited.Episodes))
	}
	if recorder := do(t, handler, http.MethodGet, "/api/episodes?filter=new&limit=0", owner, nil, nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400", recorder.Code)
	}
	var episode store.Episode
	for _, item := range episodes.Episodes {
		if item.TranscriptRefsAvailable {
			episode = item
			break
		}
	}
	if episode.ID == 0 || episode.DurationSeconds != 180 || len(episode.Chapters) != 0 {
		t.Fatalf("transcript episode = %#v", episode)
	}

	if foreign := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes", "lazycat-user-b", nil, nil), http.StatusOK); len(foreign.Episodes) != 0 {
		t.Fatalf("other owner sees %#v", foreign.Episodes)
	}
	episodePath := fmt.Sprintf("/api/episodes/%d", episode.ID)
	if recorder := do(t, handler, http.MethodGet, episodePath, "lazycat-user-b", nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign episode status = %d, want 404", recorder.Code)
	}
	foreignProgress, _ := json.Marshal(map[string]any{"position_seconds": 10, "duration_seconds": 180})
	if recorder := do(t, handler, http.MethodPut, episodePath+"/progress", "lazycat-user-b", foreignProgress, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign progress status = %d, want 404", recorder.Code)
	}

	progressBody, _ := json.Marshal(map[string]any{"position_seconds": 30, "duration_seconds": 180})
	progress := decode[store.Progress](t, do(t, handler, http.MethodPut, episodePath+"/progress", owner, progressBody, nil), http.StatusOK)
	if progress.Completed || progress.PositionSeconds != 30 {
		t.Fatalf("progress = %#v", progress)
	}

	before := decode[struct {
		Transcript *store.Transcript `json:"transcript"`
	}](t, do(t, handler, http.MethodGet, episodePath+"/transcript", owner, nil, nil), http.StatusOK)
	if before.Transcript != nil {
		t.Fatalf("transcript before fetch = %#v", before.Transcript)
	}
	fetched := decode[struct {
		Transcript store.Transcript `json:"transcript"`
	}](t, do(t, handler, http.MethodPost, episodePath+"/transcript", owner, nil, nil), http.StatusOK)
	if !fetched.Transcript.Timed || len(fetched.Transcript.Segments) != 4 {
		t.Fatalf("transcript = %#v", fetched.Transcript)
	}
	search := decode[struct {
		Results []store.SearchResult `json:"results"`
	}](t, do(t, handler, http.MethodGet, "/api/library/search?q=%E5%80%BE%E5%90%AC", owner, nil, nil), http.StatusOK)
	hit := false
	for _, result := range search.Results {
		if result.Kind == "transcript" && result.EpisodeID == episode.ID {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("search results = %#v", search.Results)
	}

	audio := do(t, handler, http.MethodGet, episodePath+"/audio", owner, nil, map[string]string{"Range": "bytes=0-99"})
	if audio.Code != http.StatusPartialContent || audio.Body.Len() != 100 {
		t.Fatalf("audio = %d, %d bytes", audio.Code, audio.Body.Len())
	}
	if !strings.HasPrefix(audio.Header().Get("Content-Range"), "bytes 0-99/") {
		t.Fatalf("content-range = %q", audio.Header().Get("Content-Range"))
	}
	if !strings.Contains(audio.Header().Get("Content-Type"), "audio") {
		t.Fatalf("content type = %q", audio.Header().Get("Content-Type"))
	}

	refresh := decode[struct {
		NotModified bool `json:"not_modified"`
		NewEpisodes int  `json:"new_episodes"`
	}](t, do(t, handler, http.MethodPost, fmt.Sprintf("/api/feeds/%d/refresh", subscribed.Feed.ID), owner, nil, nil), http.StatusOK)
	if !refresh.NotModified || refresh.NewEpisodes != 0 {
		t.Fatalf("refresh = %#v", refresh)
	}

	categoryBody, _ := json.Marshal(map[string]any{"name": "接口分类", "color": "#4f4198"})
	category := decode[store.Category](t, do(t, handler, http.MethodPost, "/api/categories", owner, categoryBody, nil), http.StatusCreated)
	if category.Name != "接口分类" {
		t.Fatalf("category = %#v", category)
	}
	if recorder := do(t, handler, http.MethodPost, "/api/categories", owner, categoryBody, nil); recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate category status = %d, want 409", recorder.Code)
	}
	assignBody, _ := json.Marshal(map[string]any{"category_ids": []int64{category.ID}})
	assigned := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPut, fmt.Sprintf("/api/feeds/%d/categories", subscribed.Feed.ID), owner, assignBody, nil), http.StatusOK)
	if len(assigned.Feed.Categories) != 1 {
		t.Fatalf("assigned feed = %#v", assigned.Feed)
	}
	byCategory := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, fmt.Sprintf("/api/episodes?filter=category&category=%d", category.ID), owner, nil, nil), http.StatusOK)
	if len(byCategory.Episodes) != 2 {
		t.Fatalf("category episodes = %#v", byCategory.Episodes)
	}

	opml := fmt.Sprintf(`<?xml version="1.0"?><opml version="2.0"><body>
<outline type="rss" text="接口测试台" xmlUrl="%s/feed.xml"/>
</body></opml>`, feedURL)
	imported := decode[service.OPMLResult](t, do(t, handler, http.MethodPost, "/api/opml", owner, []byte(opml),
		map[string]string{"Content-Type": "text/x-opml"}), http.StatusOK)
	if imported.Added != 0 || imported.Existing != 1 {
		t.Fatalf("opml result = %#v", imported)
	}
}

func TestAPIInboxActionsAndUnsubscribe(t *testing.T) {
	handler, feedURL := newAPIRig(t)
	owner := "lazycat-user-a"
	subscribeBody, _ := json.Marshal(map[string]any{"feed_url": feedURL + "/feed.xml"})
	subscribed := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, subscribeBody, nil), http.StatusCreated)

	list := func(owner, query string) []store.Episode {
		t.Helper()
		value := decode[struct {
			Episodes []store.Episode `json:"episodes"`
		}](t, do(t, handler, http.MethodGet, "/api/episodes"+query, owner, nil, nil), http.StatusOK)
		return value.Episodes
	}
	episodes := list(owner, "?filter=new")
	if len(episodes) != 2 {
		t.Fatalf("initial episodes = %#v", episodes)
	}
	ids := []int64{episodes[0].ID, episodes[1].ID}

	readBody, _ := json.Marshal(map[string]any{"ids": ids, "action": "read"})
	read := decode[struct {
		Updated int `json:"updated"`
	}](t, do(t, handler, http.MethodPost, "/api/episodes/inbox", owner, readBody, nil), http.StatusOK)
	if read.Updated != 2 {
		t.Fatalf("mark read = %#v", read)
	}
	if fresh := list(owner, "?filter=new"); len(fresh) != 0 {
		t.Fatalf("episodes still new: %#v", fresh)
	}
	all := list(owner, "")
	if len(all) != 2 || all[0].InboxState != "completed" || !all[0].Completed {
		t.Fatalf("episodes after read = %#v", all)
	}

	archiveBody, _ := json.Marshal(map[string]any{"ids": []int64{ids[0]}, "action": "archive"})
	if got := decode[struct {
		Updated int `json:"updated"`
	}](t, do(t, handler, http.MethodPost, "/api/episodes/inbox", owner, archiveBody, nil), http.StatusOK); got.Updated != 1 {
		t.Fatalf("archive = %#v", got)
	}
	if inbox := list(owner, ""); len(inbox) != 1 || inbox[0].ID != ids[1] {
		t.Fatalf("inbox after archive = %#v", inbox)
	}
	if show := list(owner, fmt.Sprintf("?feed=%d", subscribed.Feed.ID)); len(show) != 2 {
		t.Fatalf("show page after archive = %#v", show)
	}

	foreignBody, _ := json.Marshal(map[string]any{"ids": []int64{ids[0]}, "action": "unarchive"})
	if recorder := do(t, handler, http.MethodPost, "/api/episodes/inbox", "lazycat-user-b", foreignBody, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign inbox action status = %d, want 404", recorder.Code)
	}
	badBody, _ := json.Marshal(map[string]any{"ids": ids, "action": "explode"})
	if recorder := do(t, handler, http.MethodPost, "/api/episodes/inbox", owner, badBody, nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad inbox action status = %d, want 400", recorder.Code)
	}
	if got := decode[struct {
		Updated int `json:"updated"`
	}](t, do(t, handler, http.MethodPost, "/api/episodes/inbox", owner, foreignBody, nil), http.StatusOK); got.Updated != 1 {
		t.Fatalf("unarchive = %#v", got)
	}
	if inbox := list(owner, ""); len(inbox) != 2 {
		t.Fatalf("inbox after restore = %#v", inbox)
	}

	feedPath := fmt.Sprintf("/api/feeds/%d", subscribed.Feed.ID)
	if recorder := do(t, handler, http.MethodDelete, feedPath, "lazycat-user-b", nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign unsubscribe status = %d, want 404", recorder.Code)
	}
	removed := decode[struct {
		Removed int `json:"removed"`
	}](t, do(t, handler, http.MethodDelete, feedPath, owner, nil, nil), http.StatusOK)
	if removed.Removed != 1 {
		t.Fatalf("unsubscribe = %#v", removed)
	}
	if feeds := decode[struct {
		Feeds []store.Feed `json:"feeds"`
	}](t, do(t, handler, http.MethodGet, "/api/feeds", owner, nil, nil), http.StatusOK); len(feeds.Feeds) != 0 {
		t.Fatalf("feeds after unsubscribe = %#v", feeds.Feeds)
	}
	if inbox := list(owner, ""); len(inbox) != 0 {
		t.Fatalf("episodes after unsubscribe = %#v", inbox)
	}

	resubscribed := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, subscribeBody, nil), http.StatusCreated)
	removeBody, _ := json.Marshal(map[string]any{"ids": []int64{resubscribed.Feed.ID}})
	if got := decode[struct {
		Removed int `json:"removed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds/remove", owner, removeBody, nil), http.StatusOK); got.Removed != 1 {
		t.Fatalf("bulk unsubscribe = %#v", got)
	}
	if recorder := do(t, handler, http.MethodPost, "/api/feeds/remove", owner, []byte(`{"ids":[]}`), nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty bulk unsubscribe status = %d, want 400", recorder.Code)
	}
}

func downloadFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk downloads: %v", err)
	}
	return files
}

func TestAPILocalDownloads(t *testing.T) {
	handler, feedURL, downloadDir := newAPIRigWithDir(t)
	owner := "download-owner-a"
	other := "download-owner-b"

	subscribeBody, _ := json.Marshal(map[string]any{"feed_url": feedURL + "/feed.xml"})
	subscribed := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, subscribeBody, nil), http.StatusCreated)
	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=all", owner, nil, nil), http.StatusOK).Episodes
	if len(episodes) < 2 {
		t.Fatalf("episodes = %d, want at least 2", len(episodes))
	}

	for _, episode := range episodes[:2] {
		path := fmt.Sprintf("/api/episodes/%d/download", episode.ID)
		item := decode[struct {
			Download store.Download `json:"download"`
		}](t, do(t, handler, http.MethodPost, path, owner, nil, nil), http.StatusCreated)
		if item.Download.SizeBytes <= 0 || !strings.Contains(item.Download.ContentType, "audio") ||
			item.Download.CreatedAt == 0 || item.Download.Title != episode.Title {
			t.Fatalf("download = %#v", item.Download)
		}
	}

	listed := decode[struct {
		Downloads []store.Download `json:"downloads"`
	}](t, do(t, handler, http.MethodGet, "/api/downloads", owner, nil, nil), http.StatusOK)
	if len(listed.Downloads) != 2 || listed.Downloads[0].FileName != "" {
		t.Fatalf("downloads = %#v", listed.Downloads)
	}
	foreign := decode[struct {
		Downloads []store.Download `json:"downloads"`
	}](t, do(t, handler, http.MethodGet, "/api/downloads", other, nil, nil), http.StatusOK)
	if len(foreign.Downloads) != 0 {
		t.Fatalf("foreign downloads = %#v", foreign.Downloads)
	}
	if recorder := do(t, handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/download", episodes[0].ID), other, nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign download status = %d, want 404", recorder.Code)
	}

	files := downloadFiles(t, downloadDir)
	if len(files) != 2 {
		t.Fatalf("downloaded files = %v, want 2", files)
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, perm)
		}
	}

	audioPath := fmt.Sprintf("/api/episodes/%d/audio?download=1", episodes[0].ID)
	audio := do(t, handler, http.MethodGet, audioPath, owner, nil, map[string]string{"Range": "bytes=0-99"})
	if audio.Code != http.StatusPartialContent || audio.Body.Len() != 100 {
		t.Fatalf("local audio = %d, %d bytes", audio.Code, audio.Body.Len())
	}
	disposition := audio.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("content-disposition = %q", disposition)
	}

	deleted := decode[struct {
		Removed int `json:"removed"`
	}](t, do(t, handler, http.MethodDelete,
		fmt.Sprintf("/api/episodes/%d/download", episodes[0].ID), owner, nil, nil), http.StatusOK)
	if deleted.Removed != 1 {
		t.Fatalf("delete download = %#v", deleted)
	}
	if files := downloadFiles(t, downloadDir); len(files) != 1 {
		t.Fatalf("files after delete = %v, want 1", files)
	}
	if recorder := do(t, handler, http.MethodDelete,
		fmt.Sprintf("/api/episodes/%d/download", episodes[0].ID), owner, nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("delete missing download status = %d, want 404", recorder.Code)
	}

	// The same endpoint continues to proxy the remote source for episodes that
	// are not downloaded.
	remote := do(t, handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d/audio?download=1", episodes[0].ID), owner, nil,
		map[string]string{"Range": "bytes=0-99"})
	if remote.Code != http.StatusPartialContent || remote.Body.Len() != 100 {
		t.Fatalf("remote audio = %d, %d bytes", remote.Code, remote.Body.Len())
	}
	if disposition := remote.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("remote content-disposition = %q", disposition)
	}

	if recorder := do(t, handler, http.MethodDelete,
		fmt.Sprintf("/api/feeds/%d", subscribed.Feed.ID), owner, nil, nil); recorder.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d", recorder.Code)
	}
	if files := downloadFiles(t, downloadDir); len(files) != 0 {
		t.Fatalf("files after unsubscribe = %v, want none", files)
	}
}
