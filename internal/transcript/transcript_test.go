package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseVTTFixture(t *testing.T) {
	segments, err := Parse(readFixture(t, "episode.vtt"), "text/vtt", "http://127.0.0.1:8090/episode.vtt")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(segments))
	}
	if segments[0].Speaker != "主持人" || segments[0].Start != 0 {
		t.Fatalf("first segment = %#v", segments[0])
	}
	if segments[1].Speaker != "嘉宾" || segments[1].Start != 9 {
		t.Fatalf("second segment = %#v", segments[1])
	}
	if !Timed(segments) {
		t.Fatal("VTT segments must be timed")
	}
}

func TestParseSRTFixture(t *testing.T) {
	segments, err := Parse(readFixture(t, "episode.srt"), "application/x-subrip", "http://127.0.0.1:8090/episode.srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(segments))
	}
	if segments[2].Start != 19 || segments[2].End != 31 {
		t.Fatalf("third segment = %#v", segments[2])
	}
	if segments[0].Text != "推荐越准确，意外是不是越少？" {
		t.Fatalf("text = %q", segments[0].Text)
	}
}

func TestParseJSONFixture(t *testing.T) {
	segments, err := Parse(readFixture(t, "episode.json"), "application/json", "http://127.0.0.1:8090/episode.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(segments))
	}
	if segments[1].Speaker != "嘉宾" || segments[1].Start != 7 || segments[1].End != 15.2 {
		t.Fatalf("second segment = %#v", segments[1])
	}
}

func TestParsePlainTextFallback(t *testing.T) {
	segments, err := Parse(readFixture(t, "episode.txt"), "text/plain", "http://127.0.0.1:8090/episode.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(segments))
	}
	if segments[0].Start != 0 || segments[0].End != 0 || segments[0].Text == "" {
		t.Fatalf("plain segment = %#v", segments[0])
	}
	if Timed(segments) {
		t.Fatal("plain text must not be timed")
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse([]byte("   \n"), "text/plain", ""); err == nil {
		t.Fatal("expected an error for empty input")
	}
}

func TestParseJSONShapes(t *testing.T) {
	segments, err := ParseJSON([]byte(`{"transcript":[{"startTime":1.5,"endTime":3,"content":"hello","speakerName":"A"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Start != 1.5 || segments[0].End != 3 || segments[0].Speaker != "A" {
		t.Fatalf("segments = %#v", segments)
	}
	if _, err := ParseJSON([]byte(`{"items":[]}`)); err == nil {
		t.Fatal("expected an error for no segments")
	}
}
