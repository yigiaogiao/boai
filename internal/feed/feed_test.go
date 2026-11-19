package feed

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseRSSFixture(t *testing.T) {
	data := readFixture(t, "feed.xml")
	parsed, err := Parse(data, "http://127.0.0.1:8090/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Title != "潮汐测试台" {
		t.Fatalf("title = %q", parsed.Title)
	}
	if len(parsed.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(parsed.Episodes))
	}
	first := parsed.Episodes[0]
	if first.GUID != "fixture-episode-1" {
		t.Fatalf("guid = %q", first.GUID)
	}
	if first.AudioURL != "http://127.0.0.1:8090/episode.wav" || first.AudioType != "audio/wav" {
		t.Fatalf("audio = %q %q", first.AudioURL, first.AudioType)
	}
	if first.DurationSeconds != 180 {
		t.Fatalf("duration = %d, want 180", first.DurationSeconds)
	}
	if first.PublishedAt == nil || first.PublishedAt.Format("2006-01-02") != "2025-11-18" {
		t.Fatalf("published = %v", first.PublishedAt)
	}
	if len(first.Chapters) != 3 {
		t.Fatalf("chapters = %d, want 3: %#v", len(first.Chapters), first.Chapters)
	}
	wantStarts := []int{0, 65, 130}
	for i, chapter := range first.Chapters {
		if chapter.Start != wantStarts[i] {
			t.Fatalf("chapter %d start = %d, want %d", i, chapter.Start, wantStarts[i])
		}
		if chapter.Label == "" {
			t.Fatalf("chapter %d has empty label", i)
		}
	}
	if len(first.Transcripts) != 1 {
		t.Fatalf("transcripts = %d, want 1", len(first.Transcripts))
	}
	if first.Transcripts[0].URL != "http://127.0.0.1:8090/episode.vtt" ||
		first.Transcripts[0].Type != "text/vtt" ||
		first.Transcripts[0].Language != "zh-CN" {
		t.Fatalf("transcript ref = %#v", first.Transcripts[0])
	}
	if len(parsed.Episodes[1].Chapters) != 0 {
		t.Fatalf("second episode should have no chapters without timestamps: %#v", parsed.Episodes[1].Chapters)
	}
	if len(parsed.Episodes[1].Transcripts) != 0 {
		t.Fatalf("second episode should have no transcripts: %#v", parsed.Episodes[1].Transcripts)
	}
}

func TestParseUpdatedRSSAddsEpisode(t *testing.T) {
	parsed, err := Parse(readFixture(t, "feed-updated.xml"), "http://127.0.0.1:8090/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Episodes) != 3 {
		t.Fatalf("episodes = %d, want 3", len(parsed.Episodes))
	}
	if parsed.Episodes[0].GUID != "fixture-episode-3" || parsed.Episodes[0].DurationSeconds != 150 {
		t.Fatalf("unexpected first episode: %#v", parsed.Episodes[0])
	}
	if len(parsed.Episodes[0].Chapters) != 2 {
		t.Fatalf("updated chapters = %#v", parsed.Episodes[0].Chapters)
	}
}

func TestParseAtom(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>原子测试台</title>
  <link rel="alternate" href="https://example.com/show"/>
  <subtitle>一个 Atom 订阅</subtitle>
  <entry>
    <id>tag:example.com,2025:1</id>
    <title>第一集</title>
    <published>2025-11-18T09:00:00Z</published>
    <summary>简介</summary>
    <link rel="enclosure" type="audio/mpeg" href="https://example.com/ep1.mp3"/>
    <link rel="alternate" href="https://example.com/ep1"/>
  </entry>
</feed>`)
	parsed, err := Parse(data, "https://example.com/atom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Title != "原子测试台" || len(parsed.Episodes) != 1 {
		t.Fatalf("unexpected atom parse: %#v", parsed)
	}
	if parsed.Episodes[0].AudioURL != "https://example.com/ep1.mp3" ||
		parsed.Episodes[0].Link != "https://example.com/ep1" {
		t.Fatalf("atom links: %#v", parsed.Episodes[0])
	}
	if parsed.Episodes[0].PublishedAt == nil ||
		!parsed.Episodes[0].PublishedAt.Equal(time.Date(2025, 11, 18, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("atom published = %v", parsed.Episodes[0].PublishedAt)
	}
}

func TestParseRejectsNonFeed(t *testing.T) {
	if _, err := Parse([]byte(`<html><body>not a feed</body></html>`), "https://example.com"); err == nil {
		t.Fatal("expected an error for non-feed XML")
	}
	if _, err := Parse([]byte(`not xml`), "https://example.com"); err == nil {
		t.Fatal("expected an error for malformed XML")
	}
}

func TestParseDuration(t *testing.T) {
	tests := map[string]int{
		"90":       90,
		"01:30":    90,
		"00:03:00": 180,
		"1:02:03":  3723,
		"90.5":     91,
		"":         0,
		"abc":      0,
		"-10":      0,
		"00:00:00": 0,
	}
	for raw, want := range tests {
		if got := ParseDuration(raw); got != want {
			t.Errorf("ParseDuration(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseOPMLDeduplicates(t *testing.T) {
	parsed, err := ParseOPML(readFixture(t, "subscriptions.opml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 1 {
		t.Fatalf("items = %d, want 1: %#v", len(parsed.Items), parsed.Items)
	}
	if parsed.Items[0].FeedURL != "http://127.0.0.1:8090/feed.xml" {
		t.Fatalf("feed url = %q", parsed.Items[0].FeedURL)
	}
	if parsed.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", parsed.Skipped)
	}
	if _, err := ParseOPML([]byte(`<?xml version="1.0"?><html/>`)); err == nil {
		t.Fatal("expected an error for non-OPML")
	}
}

func TestChaptersRequireTwoTimestamps(t *testing.T) {
	one := chaptersFrom("00:00 只有一个时间点", 100)
	if len(one) != 0 {
		t.Fatalf("one timestamp should not become chapters: %#v", one)
	}
	many := chaptersFrom("00:00 开场\n00:30 中段\n01:00 结尾", 100)
	if len(many) != 3 {
		t.Fatalf("chapters = %#v", many)
	}
	if many[0].Start != 0 || many[2].Start != 60 {
		t.Fatalf("chapter starts = %#v", many)
	}
}
