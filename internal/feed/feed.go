// Package feed parses RSS 2.0, Atom and OPML, and resolves public podcast
// links into subscription feeds.
package feed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Feed is the normalized result of parsing a subscription document.
type Feed struct {
	Title       string
	SiteURL     string
	Description string
	ImageURL    string
	Episodes    []Episode
}

// Playable reports whether the feed has at least one episode with an audio
// enclosure. Article-only RSS feeds parse fine but are not podcast feeds.
func (f *Feed) Playable() bool {
	for _, episode := range f.Episodes {
		if episode.AudioURL != "" {
			return true
		}
	}
	return false
}

// Episode is one playable item from a feed.
type Episode struct {
	GUID            string
	Title           string
	Description     string
	Link            string
	AudioURL        string
	AudioType       string
	ImageURL        string
	DurationSeconds int
	PublishedAt     *time.Time
	Transcripts     []TranscriptRef
	Chapters        []Chapter
}

// Chapter is a timestamp the publisher listed in the episode notes.
type Chapter struct {
	Start int    `json:"start"`
	Label string `json:"label"`
}

// TranscriptRef points at a transcript offered by the publisher.
type TranscriptRef struct {
	URL      string
	Type     string
	Language string
}

var (
	tagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
	spacePattern  = regexp.MustCompile(`\s+`)
	ctrlPattern   = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	entityPattern = regexp.MustCompile(`&[a-zA-Z][a-zA-Z0-9]{1,20};`)
)

// Parse normalizes a feed document. feedURL is used for relative links and as
// a fallback title. A document that parses but has no episodes is still a
// valid feed; a document that is not RSS/Atom is not.
func Parse(data []byte, feedURL string) (*Feed, error) {
	root, err := ParseXML(data)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(feedURL)
	var f *Feed
	switch root.Name {
	case "rss", "rdf":
		f, err = parseRSS(root, base)
	case "feed":
		f, err = parseAtom(root, base)
	default:
		return nil, fmt.Errorf("not a feed: unexpected root <%s>", root.Name)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.Title) == "" {
		if base != nil && base.Hostname() != "" {
			f.Title = base.Hostname()
		} else {
			f.Title = "未命名节目"
		}
	}
	if f.SiteURL == "" {
		f.SiteURL = feedURL
	}
	f.Title = truncate(cleanText(f.Title), 500)
	f.Description = truncate(cleanText(f.Description), 50000)
	return f, nil
}

func parseRSS(root *Node, base *url.URL) (*Feed, error) {
	channel := root.Child("channel")
	if channel == nil {
		channel = root.FindFirst("channel")
	}
	if channel == nil {
		return nil, fmt.Errorf("rss: missing channel")
	}
	f := &Feed{
		Title:       channel.ChildText("title"),
		Description: firstChildText(channel, "description", "subtitle", "summary"),
		ImageURL:    resolveURL(base, feedImage(channel)),
	}
	if link := channel.ChildText("link"); link != "" {
		f.SiteURL = resolveURL(base, link)
	} else if l := channel.Child("link"); l != nil {
		f.SiteURL = resolveURL(base, l.Attr("href"))
	}
	items := channel.ChildrenNamed("item")
	if len(items) == 0 && strings.EqualFold(root.Name, "rdf") {
		items = root.ChildrenNamed("item")
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		ep, ok := parseRSSItem(item, base, f.ImageURL)
		if !ok || seen[ep.GUID] {
			continue
		}
		seen[ep.GUID] = true
		f.Episodes = append(f.Episodes, ep)
	}
	return f, nil
}

func parseRSSItem(node *Node, base *url.URL, feedImageURL string) (Episode, bool) {
	rawDescription := rawDescriptionOf(node)
	ep := Episode{
		Title:       truncate(cleanText(node.ChildText("title")), 1000),
		Description: truncate(cleanText(rawDescription), 50000),
		ImageURL:    resolveURL(base, itemImage(node, feedImageURL)),
	}
	if link := node.ChildText("link"); link != "" {
		ep.Link = resolveURL(base, link)
	}
	audioURL, audioType := audioOf(node)
	ep.AudioURL = resolveURL(base, audioURL)
	ep.AudioType = strings.ToLower(strings.TrimSpace(audioType))
	if ep.AudioURL == "" {
		return Episode{}, false
	}
	ep.PublishedAt = parseTime(firstChildText(node, "pubdate", "published", "updated", "date"))
	ep.DurationSeconds = parseItemDuration(node)
	ep.Transcripts = transcriptsOf(node, base)
	ep.Chapters = chaptersFrom(rawDescription, ep.DurationSeconds)
	ep.GUID = strings.TrimSpace(node.ChildText("guid"))
	if ep.GUID == "" {
		ep.GUID = strings.TrimSpace(node.ChildText("id"))
	}
	ep.GUID = truncate(ep.GUID, 1024)
	if ep.GUID == "" {
		ep.GUID = fallbackGUID(ep)
	}
	if ep.Title == "" {
		ep.Title = "未命名单集"
	}
	return ep, true
}

func parseAtom(root *Node, base *url.URL) (*Feed, error) {
	f := &Feed{
		Title:       root.ChildText("title"),
		Description: firstChildText(root, "subtitle", "summary"),
	}
	for _, link := range root.ChildrenNamed("link") {
		rel := strings.ToLower(link.Attr("rel"))
		typ := strings.ToLower(link.Attr("type"))
		if (rel == "" || rel == "alternate") && (typ == "" || strings.HasPrefix(typ, "text/html")) {
			if href := link.Attr("href"); href != "" {
				f.SiteURL = resolveURL(base, href)
				break
			}
		}
	}
	for _, name := range []string{"logo", "icon"} {
		if v := root.ChildText(name); v != "" {
			f.ImageURL = resolveURL(base, v)
			break
		}
	}
	seen := make(map[string]bool)
	for _, entry := range root.ChildrenNamed("entry") {
		ep, ok := parseAtomEntry(entry, base, f.ImageURL)
		if !ok || seen[ep.GUID] {
			continue
		}
		seen[ep.GUID] = true
		f.Episodes = append(f.Episodes, ep)
	}
	return f, nil
}

func parseAtomEntry(node *Node, base *url.URL, feedImageURL string) (Episode, bool) {
	rawDescription := rawDescriptionOf(node)
	ep := Episode{
		Title:       truncate(cleanText(node.ChildText("title")), 1000),
		Description: truncate(cleanText(rawDescription), 50000),
		ImageURL:    resolveURL(base, itemImage(node, feedImageURL)),
	}
	ep.GUID = truncate(strings.TrimSpace(node.ChildText("id")), 1024)
	var audioURL, audioType string
	for _, link := range node.ChildrenNamed("link") {
		rel := strings.ToLower(link.Attr("rel"))
		typ := strings.ToLower(link.Attr("type"))
		href := link.Attr("href")
		if href == "" {
			continue
		}
		switch {
		case rel == "enclosure" && (typ == "" || strings.HasPrefix(typ, "audio/")):
			if audioURL == "" {
				audioURL, audioType = href, typ
			}
		case (rel == "alternate" || rel == "") && (typ == "" || strings.HasPrefix(typ, "text/html")) && ep.Link == "":
			ep.Link = resolveURL(base, href)
		}
	}
	if audioURL == "" {
		audioURL, audioType = audioOf(node)
	}
	ep.AudioURL = resolveURL(base, audioURL)
	ep.AudioType = strings.ToLower(strings.TrimSpace(audioType))
	if ep.AudioURL == "" {
		return Episode{}, false
	}
	ep.PublishedAt = parseTime(firstChildText(node, "published", "updated"))
	ep.DurationSeconds = parseItemDuration(node)
	ep.Transcripts = transcriptsOf(node, base)
	ep.Chapters = chaptersFrom(rawDescription, ep.DurationSeconds)
	if ep.GUID == "" {
		ep.GUID = ep.Link
	}
	if ep.GUID == "" {
		ep.GUID = fallbackGUID(ep)
	}
	if ep.Title == "" {
		ep.Title = "未命名单集"
	}
	return ep, true
}

func rawDescriptionOf(node *Node) string {
	for _, name := range []string{"encoded", "content", "description", "summary", "subtitle"} {
		child := node.Child(name)
		if child == nil {
			continue
		}
		if child.Attr("src") != "" {
			continue
		}
		if value := strings.TrimSpace(child.Text.String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(child.InnerText()); value != "" {
			return value
		}
	}
	return ""
}

var (
	chapterBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>|</p\s*>|</li\s*>|</div\s*>|</h[1-6]\s*>|\n`)
	chapterTimePattern  = regexp.MustCompile(`^\s*((?:\d{1,2}:)?\d{1,2}:\d{2})\s*(?:[:\-–—.)·]\s*|\s+)(.+)$`)
	inlineTimePattern   = regexp.MustCompile(`(?:^|\s)((?:\d{1,2}:)?\d{1,2}:\d{2})(?:\s*[:\-–—.)·]\s*|\s+)`)
)

// chaptersFrom extracts publisher-provided timestamps from episode notes.
// It never invents a chapter: at least two parseable timestamps are required.
func chaptersFrom(raw string, duration int) []Chapter {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var candidates []Chapter
	seen := make(map[int]bool)
	add := func(clock, label string) {
		start := ParseDuration(clock)
		if start < 0 || start > 24*3600 {
			return
		}
		if duration > 0 && start > duration+120 {
			return
		}
		label = truncate(cleanText(label), 120)
		label = strings.TrimRight(label, " -–—:：.)·")
		if label == "" || seen[start] {
			return
		}
		seen[start] = true
		candidates = append(candidates, Chapter{Start: start, Label: label})
	}
	for _, line := range chapterBreakPattern.Split(raw, -1) {
		text := strings.TrimSpace(cleanText(line))
		if match := chapterTimePattern.FindStringSubmatch(text); len(match) == 3 {
			add(match[1], match[2])
		}
	}
	if len(candidates) < 2 {
		text := cleanText(raw)
		matches := inlineTimePattern.FindAllStringSubmatchIndex(text, 60)
		for index, match := range matches {
			end := len(text)
			if index+1 < len(matches) {
				end = matches[index+1][0]
			}
			label := strings.TrimSpace(text[match[1]:end])
			add(text[match[2]:match[3]], label)
			if len(candidates) >= 50 {
				break
			}
		}
	}
	if len(candidates) < 2 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Start < candidates[j].Start })
	if len(candidates) > 50 {
		candidates = candidates[:50]
	}
	return candidates
}

func audioOf(node *Node) (string, string) {
	var fallbackURL, fallbackType string
	for _, enc := range node.ChildrenNamed("enclosure") {
		u := enc.Attr("url")
		if u == "" {
			continue
		}
		typ := strings.ToLower(enc.Attr("type"))
		if strings.HasPrefix(typ, "audio/") || typ == "" {
			return u, typ
		}
		if fallbackURL == "" {
			fallbackURL, fallbackType = u, typ
		}
	}
	for _, media := range node.ChildrenNamed("content") {
		u := media.Attr("url")
		typ := strings.ToLower(media.Attr("type"))
		if u != "" && strings.HasPrefix(typ, "audio/") {
			return u, typ
		}
	}
	for _, link := range node.ChildrenNamed("link") {
		if strings.ToLower(link.Attr("rel")) == "enclosure" {
			u := link.Attr("href")
			typ := strings.ToLower(link.Attr("type"))
			if u != "" && (typ == "" || strings.HasPrefix(typ, "audio/")) {
				return u, typ
			}
		}
	}
	return fallbackURL, fallbackType
}

func parseItemDuration(node *Node) int {
	if child := node.Child("duration"); child != nil {
		if value := ParseDuration(child.TextValue()); value > 0 {
			return value
		}
	}
	for _, name := range []string{"content", "enclosure"} {
		for _, child := range node.ChildrenNamed(name) {
			if value := ParseDuration(child.Attr("duration")); value > 0 {
				return value
			}
		}
	}
	return 0
}

func transcriptsOf(node *Node, base *url.URL) []TranscriptRef {
	var refs []TranscriptRef
	seen := make(map[string]bool)
	add := func(rawURL, typ, lang string) {
		resolved := resolveURL(base, rawURL)
		if resolved == "" || seen[resolved] {
			return
		}
		seen[resolved] = true
		refs = append(refs, TranscriptRef{
			URL:      truncate(resolved, 2048),
			Type:     strings.ToLower(strings.TrimSpace(typ)),
			Language: truncate(strings.TrimSpace(lang), 32),
		})
	}
	for _, child := range node.ChildrenNamed("transcript") {
		raw := child.Attr("url")
		if raw == "" {
			raw = child.TextValue()
		}
		lang := child.Attr("language")
		if lang == "" {
			lang = child.Attr("lang")
		}
		add(raw, child.Attr("type"), lang)
	}
	for _, link := range node.ChildrenNamed("link") {
		rel := strings.ToLower(link.Attr("rel"))
		typ := strings.ToLower(link.Attr("type"))
		if rel == "transcript" || strings.Contains(typ, "vtt") || strings.Contains(typ, "srt") {
			add(link.Attr("href"), typ, link.Attr("hreflang"))
		}
	}
	return refs
}

func itemImage(node *Node, fallback string) string {
	for _, image := range node.ChildrenNamed("image") {
		if href := image.Attr("href"); href != "" {
			return href
		}
		if u := image.ChildText("url"); u != "" {
			return u
		}
	}
	for _, name := range []string{"thumbnail", "icon"} {
		if child := node.Child(name); child != nil {
			if u := child.Attr("url"); u != "" {
				return u
			}
			if u := child.Attr("href"); u != "" {
				return u
			}
		}
	}
	return fallback
}

func feedImage(node *Node) string {
	for _, image := range node.ChildrenNamed("image") {
		if href := image.Attr("href"); href != "" {
			return href
		}
		if u := image.ChildText("url"); u != "" {
			return u
		}
	}
	for _, name := range []string{"logo", "icon"} {
		if v := node.ChildText(name); v != "" {
			return v
		}
	}
	for _, thumb := range node.ChildrenNamed("thumbnail") {
		if u := thumb.Attr("url"); u != "" {
			return u
		}
	}
	return ""
}

func firstChildText(node *Node, names ...string) string {
	for _, name := range names {
		if value := node.ChildText(name); value != "" {
			return value
		}
	}
	return ""
}

// ParseDuration accepts seconds, MM:SS and HH:MM:SS.
func ParseDuration(raw string) int {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	var total float64
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return 0
		}
		for _, part := range parts {
			value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || value < 0 {
				return 0
			}
			total = total*60 + value
		}
	} else {
		value, err := strconv.ParseFloat(s, 64)
		if err != nil || value < 0 {
			return 0
		}
		total = value
	}
	if total <= 0 || total > 7*24*3600 {
		return 0
	}
	return int(math.Round(total))
}

func parseTime(raw string) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

func resolveURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func fallbackGUID(ep Episode) string {
	published := ""
	if ep.PublishedAt != nil {
		published = ep.PublishedAt.UTC().Format(time.RFC3339)
	}
	sum := sha256.Sum256([]byte(ep.Title + "|" + published + "|" + ep.AudioURL))
	return "gen-" + hex.EncodeToString(sum[:12])
}

func cleanText(value string) string {
	if value == "" {
		return ""
	}
	value = tagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	value = entityPattern.ReplaceAllString(value, " ")
	value = ctrlPattern.ReplaceAllString(value, "")
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func truncate(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
