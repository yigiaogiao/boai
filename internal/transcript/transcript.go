// Package transcript parses publisher-provided WebVTT, SRT, JSON and plain
// text transcripts into timestamped segments.
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Segment is one timed line of a transcript.
type Segment struct {
	Index   int
	Start   float64
	End     float64
	Speaker string
	Text    string
}

const maxSegments = 20000

var (
	voicePattern = regexp.MustCompile(`(?i)<v(?:\.[^ >]+)?\s+([^>]+)>`)
	tagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// Parse chooses a parser based on MIME type, URL suffix and content.
func Parse(data []byte, contentType, rawURL string) ([]Segment, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 {
		return nil, errors.New("transcript: empty document")
	}
	ctype := strings.ToLower(contentType)
	lowerURL := strings.ToLower(rawURL)
	switch {
	case strings.Contains(ctype, "vtt") || strings.HasSuffix(lowerURL, ".vtt") || bytes.HasPrefix(trimmed, []byte("WEBVTT")):
		return ParseVTT(trimmed)
	case strings.Contains(ctype, "srt") || strings.HasSuffix(lowerURL, ".srt"):
		return ParseSRT(trimmed)
	case strings.Contains(ctype, "json") || strings.HasSuffix(lowerURL, ".json") ||
		trimmed[0] == '{' || trimmed[0] == '[':
		if segments, err := ParseJSON(trimmed); err == nil {
			return segments, nil
		}
	}
	if bytes.Contains(trimmed, []byte("-->")) {
		if segments, err := ParseVTT(trimmed); err == nil {
			return segments, nil
		}
		if segments, err := ParseSRT(trimmed); err == nil {
			return segments, nil
		}
	}
	text := cleanCueText(string(trimmed))
	if text == "" {
		return nil, errors.New("transcript: no readable text")
	}
	if runes := []rune(text); len(runes) > 200000 {
		text = string(runes[:200000])
	}
	return []Segment{{Index: 1, Start: 0, End: 0, Text: text}}, nil
}

// ParseVTT parses WebVTT cues.
func ParseVTT(data []byte) ([]Segment, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var segments []Segment
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") ||
			strings.HasPrefix(line, "REGION") || strings.HasPrefix(line, "WEBVTT") {
			continue
		}
		arrow := strings.Index(line, "-->")
		if arrow < 0 {
			continue
		}
		start, okStart := parseTimestamp(strings.TrimSpace(line[:arrow]))
		endRaw := strings.TrimSpace(line[arrow+3:])
		if fields := strings.Fields(endRaw); len(fields) > 0 {
			endRaw = fields[0]
		}
		end, okEnd := parseTimestamp(endRaw)
		if !okStart {
			continue
		}
		if !okEnd || end < start {
			end = start
		}
		var textLines []string
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next == "" {
				i++
				break
			}
			if strings.Contains(next, "-->") {
				break
			}
			textLines = append(textLines, next)
			i++
		}
		speaker, text := splitSpeaker(strings.Join(textLines, " "))
		if text == "" {
			continue
		}
		segments = append(segments, Segment{Start: start, End: end, Speaker: speaker, Text: text})
		if len(segments) >= maxSegments {
			break
		}
	}
	if len(segments) == 0 {
		return nil, errors.New("vtt: no cues")
	}
	return finalize(segments), nil
}

// ParseSRT parses SubRip cues.
func ParseSRT(data []byte) ([]Segment, error) {
	blocks := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n")
	var segments []Segment
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		arrowIndex := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				arrowIndex = i
				break
			}
		}
		if arrowIndex < 0 || arrowIndex+1 >= len(lines) {
			continue
		}
		parts := strings.Split(lines[arrowIndex], "-->")
		if len(parts) != 2 {
			continue
		}
		start, okStart := parseTimestamp(parts[0])
		endRaw := strings.TrimSpace(parts[1])
		if fields := strings.Fields(endRaw); len(fields) > 0 {
			endRaw = fields[0]
		}
		end, okEnd := parseTimestamp(endRaw)
		if !okStart {
			continue
		}
		if !okEnd || end < start {
			end = start
		}
		speaker, text := splitSpeaker(strings.Join(lines[arrowIndex+1:], " "))
		if text == "" {
			continue
		}
		segments = append(segments, Segment{Start: start, End: end, Speaker: speaker, Text: text})
		if len(segments) >= maxSegments {
			break
		}
	}
	if len(segments) == 0 {
		return nil, errors.New("srt: no cues")
	}
	return finalize(segments), nil
}

// ParseJSON accepts common transcript JSON shapes: a bare array of segments,
// {"segments":[...]}, or {"transcript":[...]}.
func ParseJSON(data []byte) ([]Segment, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var list []any
	switch value := raw.(type) {
	case []any:
		list = value
	case map[string]any:
		for _, key := range []string{"segments", "transcript", "results", "items"} {
			if arr, ok := value[key].([]any); ok {
				list = arr
				break
			}
		}
	}
	if len(list) == 0 {
		return nil, errors.New("json: no segment array")
	}
	var segments []Segment
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := stringField(obj, "text", "content", "body", "sentence")
		if text == "" {
			continue
		}
		start, startOK := numberField(obj, "start", "startTime", "start_time", "from", "offset")
		end, endOK := numberField(obj, "end", "endTime", "end_time", "to")
		if !startOK {
			start = 0
		}
		if !endOK || end < start {
			end = start
		}
		speaker := stringField(obj, "speaker", "speakerName", "speaker_name", "who")
		segments = append(segments, Segment{Start: start, End: end, Speaker: speaker, Text: cleanCueText(text)})
		if len(segments) >= maxSegments {
			break
		}
	}
	if len(segments) == 0 {
		return nil, errors.New("json: no usable segments")
	}
	return finalize(segments), nil
}

func stringField(obj map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := obj[name]; ok {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func numberField(obj map[string]any, names ...string) (float64, bool) {
	for _, name := range names {
		value, ok := obj[name]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			if v < 0 {
				return 0, false
			}
			if strings.Contains(strings.ToLower(name), "ms") {
				return v / 1000, true
			}
			return v, true
		case json.Number:
			f, err := v.Float64()
			if err == nil && f >= 0 {
				return f, true
			}
		case string:
			if f, ok := parseTimestamp(v); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func parseTimestamp(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || value < 0 {
			return 0, false
		}
		total = total*60 + value
	}
	return total, true
}

func splitSpeaker(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	speaker := ""
	if match := voicePattern.FindStringSubmatch(text); len(match) == 2 {
		speaker = strings.TrimSpace(match[1])
	}
	text = voicePattern.ReplaceAllString(text, " ")
	text = cleanCueText(text)
	return speaker, text
}

func cleanCueText(raw string) string {
	text := tagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	text = spacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func finalize(segments []Segment) []Segment {
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].Start == segments[j].Start {
			return segments[i].Index < segments[j].Index
		}
		return segments[i].Start < segments[j].Start
	})
	for i := range segments {
		segments[i].Index = i
		if segments[i].End <= segments[i].Start {
			if i+1 < len(segments) {
				segments[i].End = segments[i+1].Start
			} else {
				segments[i].End = segments[i].Start + 5
			}
		}
	}
	return segments
}

// Timed reports whether the transcript has usable timestamps.
func Timed(segments []Segment) bool {
	for _, seg := range segments {
		if seg.End > seg.Start {
			return true
		}
	}
	return false
}
