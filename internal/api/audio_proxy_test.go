package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boai/internal/api"
	"boai/internal/service"
	"boai/internal/store"
)

// TestAPIAudioProxyRejectsActiveContent checks the HTTP boundary as well as the
// service boundary: HTML must never reach the browser with an audio-looking
// status, and upstream 416 bodies must not be relayed.
func TestAPIAudioProxyRejectsActiveContent(t *testing.T) {
	feedBody := ""
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
			_, _ = io.WriteString(w, feedBody)
		case "/good.wav":
			http.ServeFile(w, r, fixturePath("episode.wav"))
		case "/unsafe.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = io.WriteString(w, "<!doctype html><html><body><script>alert(1)</script></body></html>")
		case "/generic-html.mp3":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "<!doctype html><html><body>not audio</body></html>")
		case "/range-416.wav":
			w.Header().Set("Content-Range", "bytes */2880044")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			_, _ = io.WriteString(w, "<html><body>range error</body></html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(feedServer.Close)

	feedBody = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0"><channel><title>音频代理测试台</title><link>%[1]s/show</link><description>proxy</description>
<item><title>正常音频</title><guid>proxy-good</guid><pubDate>Mon, 17 Nov 2025 09:00:00 +0000</pubDate><enclosure url="%[1]s/good.wav" type="audio/wav" length="2880044"/></item>
<item><title>伪装音频</title><guid>proxy-unsafe</guid><pubDate>Mon, 17 Nov 2025 08:00:00 +0000</pubDate><enclosure url="%[1]s/unsafe.mp3" type="audio/mpeg" length="100"/></item>
<item><title>通用类型伪装</title><guid>proxy-generic</guid><pubDate>Mon, 17 Nov 2025 07:00:00 +0000</pubDate><enclosure url="%[1]s/generic-html.mp3" type="application/octet-stream" length="100"/></item>
<item><title>416 响应</title><guid>proxy-416</guid><pubDate>Mon, 17 Nov 2025 06:00:00 +0000</pubDate><enclosure url="%[1]s/range-416.wav" type="audio/wav" length="2880044"/></item>
</channel></rss>`, feedServer.URL)

	st, err := store.Open(filepath.Join(t.TempDir(), "audio-proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, service.Options{
		UpdateInterval: time.Hour,
		HTTPTimeout:    5 * time.Second,
		AllowPrivate:   true,
	})
	handler := api.New(svc, api.Options{
		WebDir:          t.TempDir(),
		UserHeader:      "X-HC-User-ID",
		TrustUserHeader: true,
		Version:         "test-version",
	}).Handler()
	owner := "audio-proxy-user"

	body, _ := json.Marshal(map[string]any{"feed_url": feedServer.URL + "/feed.xml"})
	decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, handler, http.MethodPost, "/api/feeds", owner, body, nil), http.StatusCreated)
	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, handler, http.MethodGet, "/api/episodes?filter=all", owner, nil, nil), http.StatusOK)
	byTitle := make(map[string]int64, len(episodes.Episodes))
	for _, episode := range episodes.Episodes {
		byTitle[episode.Title] = episode.ID
	}
	if len(byTitle) != 4 {
		t.Fatalf("episodes = %d, want 4", len(byTitle))
	}
	audioPath := func(title string) string {
		return fmt.Sprintf("/api/episodes/%d/audio", byTitle[title])
	}

	for _, title := range []string{"伪装音频", "通用类型伪装"} {
		t.Run(title, func(t *testing.T) {
			recorder := do(t, handler, http.MethodGet, audioPath(title), owner, nil, nil)
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502; body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q, want JSON", recorder.Header().Get("Content-Type"))
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "<html") {
				t.Fatalf("HTML leaked: %s", recorder.Body.String())
			}
		})
	}

	missing := do(t, handler, http.MethodGet, audioPath("416 响应"), owner, nil, map[string]string{"Range": "bytes=9999999-"})
	if missing.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("416 status = %d; body = %s", missing.Code, missing.Body.String())
	}
	if missing.Body.Len() != 0 {
		t.Fatalf("416 body leaked %d bytes: %s", missing.Body.Len(), missing.Body.String())
	}
	if strings.Contains(strings.ToLower(missing.Header().Get("Content-Type")), "html") {
		t.Fatalf("416 content type = %q", missing.Header().Get("Content-Type"))
	}
	if missing.Header().Get("Content-Range") != "bytes */2880044" {
		t.Fatalf("416 content range = %q", missing.Header().Get("Content-Range"))
	}

	rangeRecorder := do(t, handler, http.MethodGet, audioPath("正常音频"), owner, nil, map[string]string{"Range": "bytes=0-99"})
	if rangeRecorder.Code != http.StatusPartialContent || rangeRecorder.Body.Len() != 100 {
		t.Fatalf("range = %d, %d bytes", rangeRecorder.Code, rangeRecorder.Body.Len())
	}
	if rangeRecorder.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("range content type = %q", rangeRecorder.Header().Get("Content-Type"))
	}

	head := do(t, handler, http.MethodHead, audioPath("正常音频"), owner, nil, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
	if head.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("HEAD content type = %q", head.Header().Get("Content-Type"))
	}
}
