package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"boai/internal/store"
)

// TestOpenAudioRejectsNonAudioContent covers the media-proxy boundary: an
// enclosure may be pointed at HTML, SVG or a generic file, but only content
// identified as audio (or an audio URL with a trustworthy hint) may stream.
func TestOpenAudioRejectsNonAudioContent(t *testing.T) {
	wav := fixtureBytes(t, "episode.wav")
	html := []byte("<!doctype html><html><body><script>alert(1)</script></body></html>")
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	mux := http.NewServeMux()
	serveBytes := func(contentType string, body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("/ok.wav", serveBytes("audio/wav", wav))
	mux.HandleFunc("/html-as-mpeg.mp3", serveBytes("audio/mpeg", html))
	mux.HandleFunc("/cover.svg", serveBytes("image/svg+xml", svg))
	mux.HandleFunc("/generic.wav", serveBytes("application/octet-stream", wav))
	mux.HandleFunc("/generic-as-svg.wav", serveBytes("application/octet-stream", svg))
	mux.HandleFunc("/generic-html.mp3", serveBytes("application/octet-stream", html))
	mux.HandleFunc("/range.wav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "range.wav", time.Time{}, bytes.NewReader(wav))
	})
	mux.HandleFunc("/range.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "range.bin", time.Time{}, bytes.NewReader(wav))
	})
	mux.HandleFunc("/range-unknown.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "range-unknown.bin", time.Time{}, bytes.NewReader(wav))
	})
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		remaining, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if remaining > 0 {
			http.Redirect(w, r, "/hop/"+strconv.Itoa(remaining-1), http.StatusFound)
			return
		}
		http.Redirect(w, r, "/ok.wav", http.StatusFound)
	})
	mux.HandleFunc("/range-416.wav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */2880044")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = w.Write(html)
	})
	feedBody := ""
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = io.WriteString(w, feedBody)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	type mediaCase struct {
		title      string
		path       string
		declared   string
		rangeValue string
		wantType   string
		wantStatus int
		wantErr    bool
	}
	cases := []mediaCase{
		{title: "ok-wav", path: "/ok.wav", declared: "audio/wav", wantType: "audio/wav", wantStatus: http.StatusOK},
		{title: "redirect-chain", path: "/hop/6", declared: "audio/wav", wantType: "audio/wav", wantStatus: http.StatusOK},
		{title: "html-as-mpeg", path: "/html-as-mpeg.mp3", declared: "audio/mpeg", wantErr: true},
		{title: "svg-declared", path: "/cover.svg", declared: "image/svg+xml", wantErr: true},
		{title: "generic-wav", path: "/generic.wav", declared: "application/octet-stream", wantType: "audio/wave", wantStatus: http.StatusOK},
		{title: "generic-svg", path: "/generic-as-svg.wav", declared: "application/octet-stream", wantErr: true},
		{title: "generic-html", path: "/generic-html.mp3", declared: "application/octet-stream", wantErr: true},
		{title: "range-wav", path: "/range.wav", declared: "application/octet-stream", rangeValue: "bytes=0-99", wantType: "audio/wav", wantStatus: http.StatusPartialContent},
		{title: "range-declared", path: "/range.bin", declared: "audio/mpeg", rangeValue: "bytes=0-99", wantType: "audio/mpeg", wantStatus: http.StatusPartialContent},
		{title: "range-no-hint", path: "/range-unknown.bin", declared: "", rangeValue: "bytes=0-99", wantErr: true},
		{title: "range-416", path: "/range-416.wav", declared: "audio/wav", rangeValue: "bytes=9999999-", wantStatus: http.StatusRequestedRangeNotSatisfiable},
	}

	var items strings.Builder
	for _, media := range cases {
		attribute := ""
		if media.declared != "" {
			attribute = ` type="` + media.declared + `"`
		}
		fmt.Fprintf(&items, `<item><title>%s</title><guid>media-%s</guid><pubDate>Mon, 17 Nov 2025 09:00:00 +0000</pubDate><enclosure url="%s%s" length="2880044"%s/></item>`,
			media.title, media.title, server.URL, media.path, attribute)
	}
	feedBody = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0"><channel><title>媒体边界测试台</title><link>%s/show</link><description>media boundary</description>%s</channel></rss>`, server.URL, items.String())

	svc, st := newTestService(t, true)
	ctx := context.Background()
	owner := "media-owner"
	if _, _, err := svc.Subscribe(ctx, owner, "", server.URL+"/feed.xml", nil); err != nil {
		t.Fatal(err)
	}
	episodes, err := st.ListEpisodes(ctx, owner, store.EpisodeFilter{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := make(map[string]int64, len(episodes))
	for _, episode := range episodes {
		byTitle[episode.Title] = episode.ID
	}
	if len(byTitle) != len(cases) {
		t.Fatalf("episodes = %d, want %d", len(byTitle), len(cases))
	}

	for _, media := range cases {
		t.Run(media.title, func(t *testing.T) {
			resp, contentType, err := svc.OpenAudio(ctx, owner, byTitle[media.title], media.rangeValue)
			if media.wantErr {
				if !errors.Is(err, ErrUnsafeAudio) {
					if resp != nil {
						resp.Body.Close()
					}
					t.Fatalf("err = %v, want ErrUnsafeAudio", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != media.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, media.wantStatus)
			}
			if contentType != media.wantType {
				t.Fatalf("content type = %q, want %q", contentType, media.wantType)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if media.rangeValue != "" && media.wantStatus != http.StatusRequestedRangeNotSatisfiable && string(body) == string(html) {
				t.Fatal("upstream error body leaked through a successful audio response")
			}
			if media.rangeValue != "" && media.wantStatus == http.StatusPartialContent && len(body) != 100 {
				t.Fatalf("range body = %d bytes, want 100", len(body))
			}
		})
	}
}
