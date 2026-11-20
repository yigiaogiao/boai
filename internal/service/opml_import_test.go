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

	svc, _ := newTestService(t, true)
	opml := fmt.Sprintf(`<?xml version="1.0"?><opml version="2.0"><body>`+
		`<outline type="rss" title="被拒绝的节目" xmlUrl="%s/feed"/>`+
		`</body></opml>`, accessPage.URL)

	result, err := svc.ImportOPML(context.Background(), "opml-html-owner", []byte(opml))
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
	message := result.Items[0].Message
	if !strings.Contains(message, "403") || !strings.Contains(message, "源站拒绝") {
		t.Fatalf("message = %q, want an explicit 403 explanation", message)
	}
	if !strings.Contains(result.Message, "1 个失败") {
		t.Fatalf("summary = %q, want failed count", result.Message)
	}
}
