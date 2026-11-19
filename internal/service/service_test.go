package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boai/internal/store"
)

type fixtureServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	updated bool
}

func (f *fixtureServer) setUpdated(updated bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = updated
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *fixtureServer) hostReplace(data []byte, r *http.Request) []byte {
	return []byte(strings.ReplaceAll(string(data), "http://127.0.0.1:8090", "http://"+r.Host))
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	f := &fixtureServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		updated := f.updated
		f.mu.Unlock()
		name, etag := "feed.xml", `"fixture-v1"`
		if updated {
			name, etag = "feed-updated.xml", `"fixture-v2"`
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write(f.hostReplace(fixtureBytes(t, name), r))
	})
	mux.HandleFunc("/episode.wav", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("..", "..", "testdata", "fixtures", "episode.wav"))
	})
	for _, name := range []string{"episode.vtt", "episode.srt", "episode.json", "episode.txt"} {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", map[string]string{
				"episode.vtt":  "text/vtt",
				"episode.srt":  "application/x-subrip",
				"episode.json": "application/json",
				"episode.txt":  "text/plain",
			}[name])
			_, _ = w.Write(fixtureBytes(t, name))
		})
	}
	mux.HandleFunc("/subscriptions.opml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-opml")
		_, _ = w.Write(f.hostReplace(fixtureBytes(t, "subscriptions.opml"), r))
	})
	mux.HandleFunc("/show", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>潮汐测试台</title>
<link rel="alternate" type="application/rss+xml" title="潮汐测试台" href="http://` + r.Host + `/feed.xml">
</head><body>show page</body></html>`))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body>not a feed</body></html>")
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newTestService(t *testing.T, allowPrivate bool) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "service-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, Options{
		UpdateInterval: time.Hour,
		HTTPTimeout:    5 * time.Second,
		AllowPrivate:   allowPrivate,
	})
	return svc, st
}

func TestSubscribeRefreshTranscriptAndAudio(t *testing.T) {
	fixtures := newFixtureServer(t)
	svc, st := newTestService(t, true)
	ctx := context.Background()
	owner := "owner-a"

	subscribed, added, err := svc.Subscribe(ctx, owner, "", fixtures.server.URL+"/feed.xml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || subscribed.EpisodeCount != 2 || subscribed.Title != "潮汐测试台" {
		t.Fatalf("subscribe = %#v added=%d", subscribed, added)
	}
	if subscribed.LastError != "" || subscribed.ConsecutiveFailures != 0 {
		t.Fatalf("subscribe recorded a failure: %#v", subscribed)
	}

	if _, added, err := svc.Subscribe(ctx, owner, "", fixtures.server.URL+"/feed.xml", nil); err != nil || added != 0 {
		t.Fatalf("duplicate subscribe added=%d err=%v", added, err)
	}

	episodes, err := st.ListEpisodes(ctx, owner, store.EpisodeFilter{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes))
	}
	var withTranscript store.Episode
	for _, episode := range episodes {
		if episode.TranscriptRefsAvailable {
			withTranscript = episode
			break
		}
	}
	if withTranscript.ID == 0 {
		t.Fatal("no episode advertises a transcript")
	}
	transcript, err := svc.FetchTranscript(ctx, owner, withTranscript.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !transcript.Timed || len(transcript.Segments) != 4 || transcript.Segments[0].Speaker != "主持人" {
		t.Fatalf("transcript = %#v", transcript)
	}
	again, err := svc.FetchTranscript(ctx, owner, withTranscript.ID)
	if err != nil || len(again.Segments) != 4 {
		t.Fatalf("cached transcript = %#v, %v", again, err)
	}
	if _, err := svc.FetchTranscript(ctx, "owner-b", withTranscript.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign transcript err = %v", err)
	}

	resp, contentType, err := svc.OpenAudio(ctx, owner, withTranscript.ID, "bytes=0-99")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || len(body) != 100 {
		t.Fatalf("audio response = %d, %d bytes", resp.StatusCode, len(body))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-99/") {
		t.Fatalf("content-range = %q", resp.Header.Get("Content-Range"))
	}
	if !strings.Contains(contentType, "audio") {
		t.Fatalf("content type = %q", contentType)
	}
	if _, _, err := svc.OpenAudio(ctx, "owner-b", withTranscript.ID, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign audio err = %v", err)
	}

	result, err := svc.RefreshFeed(ctx, owner, subscribed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || result.NewEpisodes != 0 {
		t.Fatalf("unchanged refresh = %#v", result)
	}

	fixtures.setUpdated(true)
	result, err = svc.RefreshFeed(ctx, owner, subscribed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified || result.NewEpisodes != 1 || result.Feed.EpisodeCount != 3 {
		t.Fatalf("updated refresh = %#v", result)
	}
	if _, err := svc.RefreshFeed(ctx, "owner-b", subscribed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign refresh err = %v, want ErrNotFound", err)
	}
	episodes, err = st.ListEpisodes(ctx, owner, store.EpisodeFilter{Kind: "all"})
	if err != nil || len(episodes) != 3 {
		t.Fatalf("episodes after refresh = %d, %v", len(episodes), err)
	}
	episode, err := st.GetEpisode(ctx, owner, withTranscript.ID)
	if err != nil || !episode.HasTranscript {
		t.Fatalf("stored transcript lost on refresh: %#v, %v", episode, err)
	}
}

func TestSubscribeRejectsNonFeedAndBlocksPrivate(t *testing.T) {
	fixtures := newFixtureServer(t)
	svc, st := newTestService(t, true)
	ctx := context.Background()
	if _, _, err := svc.Subscribe(ctx, "owner", "", fixtures.server.URL+"/page", nil); err == nil {
		t.Fatal("expected subscribe to reject an HTML page")
	}
	feeds, err := st.ListFeeds(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 0 {
		t.Fatalf("failed subscribe left feeds: %#v", feeds)
	}

	blocked, _ := newTestService(t, false)
	if _, _, err := blocked.Subscribe(ctx, "owner", "", fixtures.server.URL+"/feed.xml", nil); err == nil {
		t.Fatal("expected private address to be blocked without AllowPrivate")
	}
}

func TestResolveDiscoversFeedAndOPMLImport(t *testing.T) {
	fixtures := newFixtureServer(t)
	svc, st := newTestService(t, true)
	ctx := context.Background()

	resolution := svc.Resolve(ctx, fixtures.server.URL+"/show")
	if resolution.Status != "subscribable" || resolution.FeedURL != fixtures.server.URL+"/feed.xml" {
		t.Fatalf("resolution = %#v", resolution)
	}

	opml := fixtures.hostReplace(fixtureBytes(t, "subscriptions.opml"), &http.Request{Host: strings.TrimPrefix(fixtures.server.URL, "http://")})
	result, err := svc.ImportOPML(ctx, "new-owner", opml)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Existing != 0 || result.Skipped != 1 {
		t.Fatalf("opml result = %#v", result)
	}
	again, err := svc.ImportOPML(ctx, "new-owner", opml)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Existing != 1 {
		t.Fatalf("second opml result = %#v", again)
	}
	feeds, err := st.ListFeeds(ctx, "new-owner")
	if err != nil || len(feeds) != 1 {
		t.Fatalf("opml feeds = %#v, %v", feeds, err)
	}
}

func TestImportOPMLFetchesConcurrentlyAndKeepsOrder(t *testing.T) {
	var current, peak atomic.Int64
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		running := current.Add(1)
		defer current.Add(-1)
		for {
			seen := peak.Load()
			if running <= seen || peak.CompareAndSwap(seen, running) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		var index int
		if _, err := fmt.Sscanf(r.URL.Path, "/feed/%d.xml", &index); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel>
<title>节目 %d</title><link>http://%s/</link><description>并发导入测试</description>
<item><title>单集 %d</title><guid>guid-%d</guid><pubDate>Mon, 17 Nov 2025 09:00:00 +0000</pubDate>
<enclosure url="http://%s/audio-%d.mp3" type="audio/mpeg" length="1000"/></item>
</channel></rss>`, index, r.Host, index, index, r.Host, index)
	}))
	defer feedServer.Close()

	const count = 12
	var opml strings.Builder
	opml.WriteString(`<?xml version="1.0"?><opml version="2.0"><body>`)
	for index := 0; index < count; index++ {
		fmt.Fprintf(&opml, `<outline type="rss" text="节目 %d" title="节目 %d" xmlUrl="%s/feed/%d.xml"/>`,
			index, index, feedServer.URL, index)
	}
	opml.WriteString(`</body></opml>`)

	svc, st := newTestService(t, true)
	result, err := svc.ImportOPML(context.Background(), "opml-owner", []byte(opml.String()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != count || result.Existing != 0 || result.Failed != 0 || result.Skipped != 0 {
		t.Fatalf("opml result = %#v", result)
	}
	if got := peak.Load(); got < 2 || got > opmlWorkers {
		t.Fatalf("peak concurrent fetches = %d, want 2..%d", got, opmlWorkers)
	}
	if len(result.Items) != count {
		t.Fatalf("items = %d, want %d", len(result.Items), count)
	}
	for index, item := range result.Items {
		if item.Status != "added" || item.Title != fmt.Sprintf("节目 %d", index) ||
			!strings.HasSuffix(item.FeedURL, fmt.Sprintf("/feed/%d.xml", index)) {
			t.Fatalf("item %d = %#v", index, item)
		}
	}
	feeds, err := st.ListFeeds(context.Background(), "opml-owner")
	if err != nil || len(feeds) != count {
		t.Fatalf("feeds = %d, %v", len(feeds), err)
	}

	again, err := svc.ImportOPML(context.Background(), "opml-owner", []byte(opml.String()))
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Existing != count || again.Failed != 0 {
		t.Fatalf("second import = %#v", again)
	}
	if current.Load() != 0 {
		t.Fatalf("fetch gauge = %d after import", current.Load())
	}
}
