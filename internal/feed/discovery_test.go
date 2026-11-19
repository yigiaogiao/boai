package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boai/internal/safehttp"
)

const testFeedXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<title>测试节目</title>
<link>https://example.com/</link>
<description>desc</description>
<item><title>第一集</title><guid>ep-1</guid><enclosure url="https://example.com/1.mp3" type="audio/mpeg" length="1"/></item>
</channel></rss>`

func TestLooksLikeEpisodePath(t *testing.T) {
	cases := map[string]bool{
		"/":                        false,
		"/episodes/":               false,
		"/episode/abc":             true,
		"/episodes/abc/":           true,
		"/ep/42":                   true,
		"/watch":                   true,
		"/shorts/abc":              true,
		"/feed":                    false,
		"/blog/2026/what-is-a-pod": false,
	}
	for path, want := range cases {
		if got := looksLikeEpisodePath(path); got != want {
			t.Errorf("looksLikeEpisodePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// appleFixture serves an iTunes lookup response for a show and its episodes,
// plus the podcast feed. Lookup by episode ID alone returns nothing, matching
// the real directory.
func appleFixture(t *testing.T, feedPath string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		entity := r.URL.Query().Get("entity")
		results := []itunesResult{}
		if id == "123" {
			results = append(results, itunesResult{
				TrackID: 123, CollectionID: 123, CollectionName: "节目", TrackName: "节目",
				FeedURL: server.URL + feedPath,
			})
		}
		if id == "123" && entity == "podcastEpisode" {
			results = append(results,
				itunesResult{TrackID: 9001, CollectionID: 123, TrackName: "第一集"},
				itunesResult{TrackID: 9002, CollectionID: 123, TrackName: "第二集"},
			)
		}
		_ = json.NewEncoder(w).Encode(itunesResponse{ResultCount: len(results), Results: results})
	})
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(testFeedXML))
	})
	server = httptest.NewServer(mux)
	return server
}

func testResolver(base string) *Resolver {
	client := safehttp.New(safehttp.Options{AllowPrivate: true})
	return NewResolver(client, base)
}

func TestResolveAppleShowAndEpisode(t *testing.T) {
	server := appleFixture(t, "/feed.xml")
	defer server.Close()
	r := testResolver(server.URL)

	show := r.Resolve(context.Background(), "https://podcasts.apple.com/us/podcast/x/id123")
	if show.Status != "subscribable" || show.FeedURL != server.URL+"/feed.xml" {
		t.Fatalf("show resolution = %#v", show)
	}
	if show.Title != "测试节目" {
		t.Fatalf("title = %q, want parsed feed title", show.Title)
	}

	episode := r.Resolve(context.Background(), "https://podcasts.apple.com/us/podcast/x/id123?i=9002")
	if episode.Status != "subscribable" || episode.Kind != "feed" {
		t.Fatalf("episode resolution = %#v", episode)
	}
	if !strings.Contains(episode.Message, "第二集") || !strings.Contains(episode.Message, "订阅源") {
		t.Fatalf("episode message should confirm title and feed: %q", episode.Message)
	}

	missing := r.Resolve(context.Background(), "https://podcasts.apple.com/us/podcast/x/id123?i=7777")
	if missing.Status != "subscribable" || !strings.Contains(missing.Message, "未出现") {
		t.Fatalf("unconfirmed episode should still subscribe at show level: %#v", missing)
	}
}

func TestResolveAppleReportsUnreadableFeed(t *testing.T) {
	server := appleFixture(t, "/missing.xml")
	defer server.Close()
	r := testResolver(server.URL)

	res := r.Resolve(context.Background(), "https://podcasts.apple.com/us/podcast/x/id123")
	if res.Status != "needs_feed" || res.Kind != "show" {
		t.Fatalf("resolution = %#v", res)
	}
	if !strings.Contains(res.Message, "无法读取") || !strings.Contains(res.Message, "RSS") {
		t.Fatalf("failure message should state the reason and suggest RSS: %q", res.Message)
	}
}

func TestDiscoverChartAndLatestEpisodes(t *testing.T) {
	var lookups int
	mux := http.NewServeMux()
	mux.HandleFunc("/cn/rss/toppodcasts/limit=20/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"feed":{"entry":[
			{"im:name":{"label":"榜单一"},"im:image":[{"label":"https://img/1.jpg"}],"im:artist":{"label":"主播一"},"id":{"attributes":{"im:id":"101"}}},
			{"im:name":{"label":"榜单二"},"im:image":[{"label":"https://img/2.jpg"}],"im:artist":{"label":"主播二"},"id":{"attributes":{"im:id":"202"}}}
		]}}`))
	})
	mux.HandleFunc("/us/rss/toppodcasts/limit=20/genre=1318/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"feed":{"entry":[
			{"im:name":{"label":"Tech One"},"summary":{"label":"AI and technology news"},"im:image":[{"label":"https://img/t1.jpg"}],"im:artist":{"label":"Host T1"},"id":{"attributes":{"im:id":"301"}}},
			{"im:name":{"label":"Tech Two"},"summary":{"label":"Internet and software"},"im:image":[{"label":"https://img/t2.jpg"}],"im:artist":{"label":"Host T2"},"id":{"attributes":{"im:id":"302"}}},
			{"im:name":{"label":"Wellness Hour"},"summary":{"label":"Health, habits and mindfulness"},"im:image":[{"label":"https://img/t3.jpg"}],"im:artist":{"label":"Host T3"},"id":{"attributes":{"im:id":"303"}}}
		]}}`))
	})
	mux.HandleFunc("/us/rss/toppodcasts/limit=20/genre=1321/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"feed":{"entry":[
			{"im:name":{"label":"Tech One"},"im:image":[{"label":"https://img/t1.jpg"}],"im:artist":{"label":"Host T1"},"id":{"attributes":{"im:id":"301"}}},
			{"im:name":{"label":"Business One"},"summary":{"label":"Finance and markets"},"im:image":[{"label":"https://img/b1.jpg"}],"im:artist":{"label":"Host B1"},"id":{"attributes":{"im:id":"401"}}},
			{"im:name":{"label":"Business Two"},"summary":{"label":"Investing and business news"},"im:image":[{"label":"https://img/b2.jpg"}],"im:artist":{"label":"Host B2"},"id":{"attributes":{"im:id":"402"}}},
			{"im:name":{"label":"General Talk"},"summary":{"label":"Life and culture"},"im:image":[{"label":"https://img/b3.jpg"}],"im:artist":{"label":"Host B3"},"id":{"attributes":{"im:id":"403"}}}
		]}}`))
	})
	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		lookups++
		entity := r.URL.Query().Get("entity")
		results := []itunesResult{}
		if entity == "podcast" {
			results = []itunesResult{
				{CollectionID: 101, CollectionName: "榜单一", ArtistName: "主播一", FeedURL: "https://feed.example/one.xml", TrackCount: 10, ArtworkURL600: "https://img/1.jpg"},
				{CollectionID: 202, CollectionName: "榜单二", ArtistName: "主播二", FeedURL: "https://feed.example/two.xml", TrackCount: 20, ArtworkURL600: "https://img/2.jpg"},
				{CollectionID: 301, CollectionName: "Tech One", ArtistName: "Host T1", FeedURL: "https://feed.example/tech-one.xml", TrackCount: 30},
				{CollectionID: 302, CollectionName: "Tech Two", ArtistName: "Host T2", FeedURL: "https://feed.example/tech-two.xml", TrackCount: 31},
				{CollectionID: 303, CollectionName: "Wellness Hour", ArtistName: "Host T3", FeedURL: "https://feed.example/wellness.xml", TrackCount: 32},
				{CollectionID: 401, CollectionName: "Business One", ArtistName: "Host B1", FeedURL: "https://feed.example/business-one.xml", TrackCount: 40},
				{CollectionID: 402, CollectionName: "Business Two", ArtistName: "Host B2", FeedURL: "https://feed.example/business-two.xml", TrackCount: 41},
				{CollectionID: 403, CollectionName: "General Talk", ArtistName: "Host B3", FeedURL: "https://feed.example/general-talk.xml", TrackCount: 42},
			}
		} else {
			results = []itunesResult{
				{WrapperType: "podcastEpisode", CollectionID: 101, TrackName: "第一期", ReleaseDate: "2026-11-18T10:00:00Z"},
				{WrapperType: "podcastEpisode", CollectionID: 202, TrackName: "第二期", ReleaseDate: "2026-11-17T10:00:00Z"},
			}
			if r.URL.Query().Get("country") == "us" {
				results = []itunesResult{
					// Apple puts the show's own "track" result before its
					// latest episode. The panel must ignore it.
					{WrapperType: "track", CollectionID: 301, TrackName: "Tech One", ReleaseDate: "2026-11-18T08:00:00Z"},
					{WrapperType: "podcastEpisode", CollectionID: 301, TrackName: "Tech Weekly", ReleaseDate: "2026-11-18T08:00:00Z"},
					{WrapperType: "track", CollectionID: 302, TrackName: "Tech Two"},
					{WrapperType: "podcastEpisode", CollectionID: 302, TrackName: "Internet Roundup", ReleaseDate: "2026-11-17T08:00:00Z"},
					{WrapperType: "track", CollectionID: 303, TrackName: "Wellness Hour"},
					{WrapperType: "podcastEpisode", CollectionID: 303, TrackName: "Better Sleep Habits", ReleaseDate: "2026-11-17T07:00:00Z"},
					{WrapperType: "track", CollectionID: 401, TrackName: "Business One"},
					{WrapperType: "podcastEpisode", CollectionID: 401, TrackName: "Markets This Week", ReleaseDate: "2026-11-17T08:00:00Z"},
					{WrapperType: "track", CollectionID: 402, TrackName: "Business Two"},
					{WrapperType: "podcastEpisode", CollectionID: 402, TrackName: "Founder Notes", ReleaseDate: "2026-11-16T08:00:00Z"},
					{WrapperType: "track", CollectionID: 403, TrackName: "General Talk"},
					{WrapperType: "podcastEpisode", CollectionID: 403, TrackName: "A Chat About Hobbies", ReleaseDate: "2026-11-15T08:00:00Z"},
				}
			}
		}
		_ = json.NewEncoder(w).Encode(itunesResponse{ResultCount: len(results), Results: results})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	r := testResolver(server.URL)
	board, err := r.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Shows) != 2 || board.Shows[0].Rank != 1 || board.Shows[1].Name != "榜单二" {
		t.Fatalf("shows = %#v", board.Shows)
	}
	if len(board.Episodes) != 2 || board.Episodes[0].Title != "第一期" || board.Episodes[0].Rank != 1 {
		t.Fatalf("episodes = %#v", board.Episodes)
	}
	if board.Episodes[0].PublishedAt == 0 || board.Source == "" || board.EpisodeSource == "" {
		t.Fatalf("board metadata = %#v", board)
	}
	if len(board.EnglishTech) != 3 || board.EnglishTech[0].Name != "Tech One" || board.EnglishTech[1].Rank != 2 {
		t.Fatalf("english tech = %#v", board.EnglishTech)
	}
	if len(board.EnglishBusiness) != 3 || board.EnglishBusiness[0].Name != "Business One" || board.EnglishBusiness[1].Name != "Business Two" || board.EnglishBusiness[2].Name != "General Talk" {
		t.Fatalf("english business (deduped) = %#v", board.EnglishBusiness)
	}
	if board.EnglishSource == "" || board.EnglishError != "" {
		t.Fatalf("english metadata = %#v", board)
	}
	if len(board.EnglishTechEpisodes) != 2 || board.EnglishTechEpisodes[0].Title != "Tech Weekly" ||
		board.EnglishTechEpisodes[0].ShowTitle != "Tech One" || board.EnglishTechEpisodes[0].Rank != 1 ||
		board.EnglishTechEpisodes[1].Title != "Internet Roundup" {
		t.Fatalf("english tech episodes = %#v", board.EnglishTechEpisodes)
	}
	for _, episode := range board.EnglishTechEpisodes {
		if episode.Title == "Tech One" || episode.ShowTitle == "Wellness Hour" {
			t.Fatalf("english tech panel kept an unrelated or show-title item: %#v", episode)
		}
	}
	if len(board.EnglishBusinessEpisodes) != 2 || board.EnglishBusinessEpisodes[0].ShowTitle != "Business One" ||
		board.EnglishBusinessEpisodes[1].ShowTitle != "Business Two" || board.EnglishEpisodeError != "" {
		t.Fatalf("english business episodes = %#v, error = %q", board.EnglishBusinessEpisodes, board.EnglishEpisodeError)
	}
	for _, episode := range board.EnglishBusinessEpisodes {
		if episode.ShowTitle == "General Talk" {
			t.Fatalf("finance panel kept a general-interest show: %#v", episode)
		}
	}
	if _, err := r.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lookups != 5 {
		t.Fatalf("lookups = %d, want 5 (CN shows, CN episodes, US shows, US tech episodes, US business episodes) and a cached second call", lookups)
	}
}

func TestResolveArticleOnlyFeedIsNotSubscribable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>
<title>文章站</title><item><title>一篇没有音频的文章</title><guid>a1</guid></item>
</channel></rss>`))
	}))
	defer server.Close()
	r := testResolver(server.URL)

	res := r.Resolve(context.Background(), server.URL+"/feed")
	if res.Status != "unsupported" || res.Kind != "unsupported" {
		t.Fatalf("article feed resolution = %#v", res)
	}
	if !strings.Contains(res.Message, "没有音频单集") {
		t.Fatalf("message should explain missing audio: %q", res.Message)
	}
}

func TestResolveAppleEpisodeOnlyLinkExplains(t *testing.T) {
	r := testResolver("https://itunes.example")
	res := r.Resolve(context.Background(), "https://podcasts.apple.com/us/podcast/x?i=9001")
	if res.Status != "needs_feed" || !strings.Contains(res.Message, "单集 ID") || !strings.Contains(res.Message, "RSS") {
		t.Fatalf("resolution = %#v", res)
	}
}
