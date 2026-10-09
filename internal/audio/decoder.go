// Package audio converts bounded, local encoded files to the model input format.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/androiddrew/parakeet-run/internal/config"
)

const SampleRate = 16000

var ErrInvalid = errors.New("invalid audio")
var ErrTooLong = errors.New("audio exceeds maximum decoded duration")

type Decoder struct{ Config config.Audio }

func (d Decoder) Check() error { _, err := exec.LookPath(d.Config.FFmpeg); return err }

// tailBuffer bounds native decoder diagnostics independently of input size.
type tailBuffer struct{ bytes.Buffer }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > 8192 {
		p = p[len(p)-8192:]
	}
	if b.Len()+len(p) > 8192 {
		b.Next(b.Len() + len(p) - 8192)
	}
	b.Buffer.Write(p)
	return n, nil
}

func (d Decoder) Decode(ctx context.Context, path string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, d.Config.DecodeTimeout)
	defer cancel()
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The demuxer allowlist excludes playlists and image/concat demuxers. Do not
	// enable external MOV data references or network protocols for uploaded media.
	cmd := exec.CommandContext(ctx, d.Config.FFmpeg, "-nostdin", "-hide_banner", "-v", "error", "-xerror",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "wav,mp3,mov,matroska,webm,ogg,flac,mpeg,aac",
		"-threads", "1", "-i", path, "-map", "0:a:0", "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", "16000", "-threads", "1", "-f", "f32le", "pipe:1")
	cmd.WaitDelay = 2 * time.Second
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start audio decoder: %w", err)
	}
	maxSamples := int(d.Config.MaxDuration / time.Millisecond * 16)
	samples := make([]float32, 0, min(maxSamples, SampleRate*30))
	buf := make([]byte, 32*1024)
	var decodeErr error
	for {
		n, readErr := io.ReadFull(stdout, buf)
		if n%4 != 0 {
			decodeErr = fmt.Errorf("%w: incomplete PCM sample", ErrInvalid)
			break
		}
		if len(samples)+n/4 > maxSamples {
			decodeErr = ErrTooLong
			break
		}
		for i := 0; i < n; i += 4 {
			sample := math.Float32frombits(binary.LittleEndian.Uint32(buf[i:]))
			if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
				decodeErr = fmt.Errorf("%w: non-finite sample", ErrInvalid)
				break
			}
			samples = append(samples, sample)
		}
		if decodeErr != nil {
			break
		}
		if readErr != nil {
			if readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				decodeErr = readErr
			}
			break
		}
	}
	if decodeErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("%w: decoder: %v: %s", ErrInvalid, waitErr, stderr.String())
	}
	if len(samples) < 320 {
		return nil, fmt.Errorf("%w: at least 20ms is required", ErrInvalid)
	}
	return samples, nil
}
