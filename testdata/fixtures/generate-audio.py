#!/usr/bin/env python3
"""Regenerate episode.wav.

The fixture feed advertises a three-minute episode with chapters at 00:00,
01:05 and 02:10, and transcript lines up to 00:48. The tone changes every
15 seconds so a listener can hear where a seek landed.

Usage: python3 testdata/fixtures/generate-audio.py
"""

import math
import struct
import wave
from pathlib import Path

RATE = 8000
DURATION_SECONDS = 180
TONES_HZ = [220, 247, 262, 294, 330, 349, 392, 440, 494, 523, 587, 659]
OUT = Path(__file__).with_name("episode.wav")


def main() -> None:
    total_samples = RATE * DURATION_SECONDS
    segment_samples = RATE * 15
    with wave.open(str(OUT), "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(RATE)
        frames = bytearray()
        for index in range(total_samples):
            tone = TONES_HZ[min(index * len(TONES_HZ) // total_samples, len(TONES_HZ) - 1)]
            offset = (index % segment_samples) / RATE
            fade = min(1.0, offset / 0.02, (15 - offset) / 0.02)
            sample = int(0.18 * 32767 * math.sin(2 * math.pi * tone * index / RATE) * fade)
            frames += struct.pack("<h", sample)
        audio.writeframes(frames)
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes, {DURATION_SECONDS}s)")


if __name__ == "__main__":
    main()
