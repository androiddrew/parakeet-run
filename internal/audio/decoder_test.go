package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/androiddrew/parakeet-run/internal/config"
)

func sourceWAV(t *testing.T) string {
	t.Helper()
	const rate, channels = 48000, 2
	pcm := make([]int16, rate*channels)
	for i := range rate {
		pcm[2*i] = int16(math.Sin(2*math.Pi*440*float64(i)/rate) * 12000)
		pcm[2*i+1] = pcm[2*i]
	}
	path := filepath.Join(t.TempDir(), "stereo.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, value := range []any{[]byte("RIFF"), uint32(36 + len(pcm)*2), []byte("WAVEfmt "), uint32(16), uint16(1), uint16(channels), uint32(rate), uint32(rate * channels * 2), uint16(channels * 2), uint16(16), []byte("data"), uint32(len(pcm) * 2), pcm} {
		if err := binary.Write(f, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestFormatsAndResampling(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	source := sourceWAV(t)
	for _, tc := range []struct{ ext, codec, mux string }{
		{"wav", "pcm_s16le", "wav"}, {"mp3", "libmp3lame", "mp3"}, {"mpga", "libmp3lame", "mp3"},
		{"mp4", "aac", "mp4"}, {"m4a", "aac", "mp4"}, {"mpeg", "mp2", "mpeg"},
		{"webm", "libopus", "webm"}, {"ogg", "libvorbis", "ogg"}, {"flac", "flac", "flac"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio."+tc.ext)
			cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", source, "-c:a", tc.codec, "-f", tc.mux, path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("encode fixture: %v: %s", err, out)
			}
			d := Decoder{Config: config.Defaults().Audio}
			samples, err := d.Decode(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if len(samples) < 15000 || len(samples) > 18000 {
				t.Fatalf("unexpected duration: %d samples", len(samples))
			}
			var energy float64
			for _, s := range samples {
				energy += float64(s * s)
			}
			if energy < 1 {
				t.Fatal("decoded audio lost signal")
			}
		})
	}
}

func TestDurationInvalidAndCancellation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	d := Decoder{Config: config.Defaults().Audio}
	d.Config.MaxDuration = 100 * time.Millisecond
	if _, err := d.Decode(context.Background(), sourceWAV(t)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("duration limit: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.wav")
	if err := os.WriteFile(bad, []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Decode(ctx, bad); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
