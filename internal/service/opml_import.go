package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
	"time"

	"boai/internal/feed"
)

// OPMLItemResult reports one outline from an import.
type OPMLItemResult struct {
	Title   string `json:"title"`
	FeedURL string `json:"feed_url"`
	Status  string `json:"status"` // added, existing, failed, skipped
	Message string `json:"message,omitempty"`
}

// OPMLResult summarizes an OPML import. Added counts only feeds whose
// documents were fetched and parsed during this import.
type OPMLResult struct {
	Added    int              `json:"added"`
	Existing int              `json:"existing"`
	Failed   int              `json:"failed"`
	Skipped  int              `json:"skipped"`
	Items    []OPMLItemResult `json:"items"`
	Message  string           `json:"message"`
}

const (
	maxOPMLItems = 100
	// Fetching a large export one outline at a time made a 60-show import feel
	// frozen for minutes. Network fetches run in this many workers while the
	// database writes stay serial, which is both faster and friendly to the
	// single-writer SQLite setup.
	opmlWorkers = 8
)

type opmlPrepared struct {
	parsed  *feed.Feed
	fetched fetchedFeed
}

// ImportOPML confirms and stores each subscription in an OPML document.
// Results keep the original outline order regardless of fetch completion
// order, and every item still gets its own added/existing/failed/skipped
// outcome.
func (s *Service) ImportOPML(ctx context.Context, owner string, data []byte) (OPMLResult, error) {
	parsed, err := feed.ParseOPML(data)
	if err != nil {
		return OPMLResult{}, errors.New("这不是有效的 OPML 文件")
	}
	existingFeeds, err := s.store.ListFeeds(ctx, owner)
	if err != nil {
		return OPMLResult{}, errors.New("无法读取已有订阅")
	}
	existing := make(map[string]bool, len(existingFeeds))
	for _, item := range existingFeeds {
		existing[item.URL] = true
	}

	items := parsed.Items
	outcomes := make([]OPMLItemResult, len(items))
	prepared := make([]*opmlPrepared, len(items))
	limit := len(items)
	if limit > maxOPMLItems {
		limit = maxOPMLItems
	}
	for index, item := range items {
		title := item.Title
		if title == "" {
			title = item.FeedURL
		}
		outcomes[index] = OPMLItemResult{Title: title, FeedURL: item.FeedURL}
		switch {
		case index >= maxOPMLItems:
			outcomes[index].Status = "skipped"
			outcomes[index].Message = "超过单次导入上限"
		case ctx.Err() != nil:
			outcomes[index].Status = "skipped"
			outcomes[index].Message = "导入已中断"
		case existing[item.FeedURL]:
			outcomes[index].Status = "existing"
			outcomes[index].Message = "已在资料库中"
		}
	}

	var wg sync.WaitGroup
	jobs := make(chan int)
	for worker := 0; worker < opmlWorkers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				item := items[index]
				itemCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				fetched, fetchErr := s.fetchFeed(itemCtx, item.FeedURL, "", "")
				cancel()
				if ctx.Err() != nil {
					outcomes[index].Status = "skipped"
					outcomes[index].Message = "导入已中断"
					continue
				}
				if fetchErr != nil {
					outcomes[index].Status = "failed"
					outcomes[index].Message = fetchErr.Error()
					continue
				}
				parsedFeed, parseErr := feed.Parse(fetched.body, fetched.finalURL)
				if parseErr != nil {
					outcomes[index].Status = "failed"
					outcomes[index].Message = opmlParseFailure(fetched)
					continue
				}
				prepared[index] = &opmlPrepared{parsed: parsedFeed, fetched: fetched}
			}
		}()
	}

enqueue:
	for index := 0; index < limit; index++ {
		if outcomes[index].Status != "" {
			continue
		}
		select {
		case jobs <- index:
		case <-ctx.Done():
			for rest := index; rest < limit; rest++ {
				if outcomes[rest].Status == "" {
					outcomes[rest].Status = "skipped"
					outcomes[rest].Message = "导入已中断"
				}
			}
			break enqueue
		}
	}
	close(jobs)
	wg.Wait()

	// Writes are deliberately serial: SQLite is configured for a small pool,
	// and UpsertFeed is a transaction. The fetched documents are already
	// parsed, so this phase is fast.
	stored := make(map[string]bool, len(existing))
	for url := range existing {
		stored[url] = true
	}
	for index := 0; index < limit; index++ {
		if prepared[index] == nil {
			continue
		}
		if ctx.Err() != nil {
			outcomes[index].Status = "skipped"
			outcomes[index].Message = "导入已中断"
			continue
		}
		item := prepared[index]
		if stored[item.fetched.finalURL] {
			outcomes[index].Status = "existing"
			outcomes[index].Message = "已在资料库中"
			continue
		}
		feedID, _, err := s.store.UpsertFeed(ctx, owner, toFeedInput(item.parsed, item.fetched))
		if err != nil {
			outcomes[index].Status = "failed"
			outcomes[index].Message = "写入资料库失败"
			continue
		}
		_ = s.store.RecordFeedResult(ctx, owner, feedID, true, item.fetched.etag,
			item.fetched.lastModified, s.now().Add(s.updateInterval), "")
		stored[item.fetched.finalURL] = true
		outcomes[index].Status = "added"
	}

	result := OPMLResult{Items: outcomes, Skipped: parsed.Skipped}
	for _, outcome := range outcomes {
		switch outcome.Status {
		case "added":
			result.Added++
		case "existing":
			result.Existing++
		case "failed":
			result.Failed++
		case "skipped":
			result.Skipped++
		}
	}
	result.Message = opmlMessage(result)
	return result, nil
}

var htmlTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// opmlParseFailure separates "the URL returned an HTML access page" from a
// genuinely malformed feed. Some hosts answer a blocked feed URL with a 200
// HTML error page after a redirect, which would otherwise look like corrupt
// RSS and hide the real reason from the import result.
func opmlParseFailure(fetched fetchedFeed) string {
	if !looksLikeHTML(fetched) {
		return "不是可解析的 RSS／Atom 订阅源"
	}
	title := htmlPageTitle(fetched.body)
	lower := strings.ToLower(title)
	if strings.Contains(title, "403") || strings.Contains(lower, "forbidden") ||
		strings.Contains(title, "无法访问") || strings.Contains(title, "拒绝访问") {
		if title != "" {
			return fmt.Sprintf("订阅源返回 403 访问页（%s），源站拒绝当前网络访问", title)
		}
		return "订阅源返回 403 访问页，源站拒绝当前网络访问"
	}
	if title != "" {
		return fmt.Sprintf("订阅源返回网页而不是 RSS／Atom（%s）", title)
	}
	return "订阅源返回网页而不是 RSS／Atom"
}

func looksLikeHTML(fetched fetchedFeed) bool {
	if fetched.contentType == "text/html" || fetched.contentType == "application/xhtml+xml" {
		return true
	}
	head := bytes.ToLower(fetched.body)
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("<!doctype html")) || bytes.Contains(head, []byte("<html"))
}

func htmlPageTitle(body []byte) string {
	match := htmlTitlePattern.FindSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	title := strings.Join(strings.Fields(html.UnescapeString(string(match[1]))), " ")
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80]) + "…"
	}
	return title
}

func opmlMessage(result OPMLResult) string {
	parts := []string{fmt.Sprintf("新增 %d 个订阅", result.Added)}
	if result.Existing > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已存在", result.Existing))
	}
	if result.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个失败", result.Failed))
	}
	if result.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d 个跳过", result.Skipped))
	}
	return strings.Join(parts, "，")
}
