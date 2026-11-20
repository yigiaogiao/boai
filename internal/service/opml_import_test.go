package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportOPMLExplainsHTMLAccessPage(t *testing.T) {
	accessPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			http.Redirect(w, r, "/blocked.html", http.StatusFound)
		case "/blocked.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>403 · 无法访问</title></head>`+
				`<body>denied by IP ACL</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer accessPage.Close()

	svc, st := newTestService(t, true)
	opml := fmt.Sprintf(`<?xml version="1.0"?><opml version="2.0"><body>`+
		`<outline type="rss" title="被拒绝的节目" xmlUrl="%s/feed"/>`+
		`</body></opml>`, accessPage.URL)

	result, err := svc.ImportOPML(context.Background(), "opml-html-owner", []byte(opml))
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending != 1 || result.Failed != 0 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if status := result.Items[0].Status; status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	message := result.Items[0].Message
	if !strings.Contains(message, "403") || !strings.Contains(message, "源站拒绝") {
		t.Fatalf("message = %q, want an explicit 403 explanation", message)
	}
	if !strings.Contains(result.Message, "已加入但暂时无法获取内容") {
		t.Fatalf("summary = %q, want pending explanation", result.Message)
	}

	// The outline must survive as a retryable subscription; other podcast apps
	// keep the entry too even when the source is unreachable at import time.
	feeds, err := st.ListFeeds(context.Background(), "opml-html-owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 {
		t.Fatalf("feeds = %#v, want one pending subscription", feeds)
	}
	if feeds[0].Title != "被拒绝的节目" || !strings.Contains(feeds[0].LastError, "403") {
		t.Fatalf("feed = %#v, want OPML title and recorded error", feeds[0])
	}
	if feeds[0].ConsecutiveFailures != 1 {
		t.Fatalf("consecutive failures = %d, want 1", feeds[0].ConsecutiveFailures)
	}
}
