package service

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestFixtureAudioCoversLongestTimeline keeps the test audio honest: the
// fixture feeds advertise episodes up to three minutes, chapters up to 02:10
// and transcript lines up to 00:48. A shorter WAV would make seeking and
// timestamp jumps untestable even though the feed metadata says otherwise.
func TestFixtureAudioCoversLongestTimeline(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "episode.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatal("episode.wav is not a RIFF/WAVE file")
	}
	var byteRate, dataSize uint32
	for offset := 12; offset+8 <= len(data); {
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if chunkSize < 0 || offset+8+chunkSize > len(data) {
			break
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if chunkSize >= 16 {
				byteRate = binary.LittleEndian.Uint32(data[offset+8+8 : offset+8+12])
			}
		case "data":
			dataSize = uint32(chunkSize)
		}
		offset += 8 + chunkSize + chunkSize%2
	}
	if byteRate == 0 || dataSize == 0 {
		t.Fatal("episode.wav has no usable fmt/data chunks")
	}
	duration := float64(dataSize) / float64(byteRate)
	if duration < 180 {
		t.Fatalf("episode.wav lasts %.1fs, want at least 180s", duration)
	}
	t.Logf("fixture audio: %.1fs, %d bytes", duration, len(data))
}
