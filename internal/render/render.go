// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package render merges per-scene clips + voice-over audio into one final MP4
// (H.264/AAC, faststart — accepted by YouTube, Instagram, Threads and TikTok)
// and produces an SRT subtitle file from the voice-over script.
package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Renderer struct {
	FFmpeg  string
	FFprobe string
	TmpDir  string
}

// Visual source kinds for a segment.
const (
	VisualVideo = "video"
	VisualImage = "image"
	VisualNone  = "none" // colour card (e.g. mock SVG placeholders ffmpeg cannot decode)
)

type Segment struct {
	VisualKind  string
	VisualPath  string
	AudioPath   string // "" → silence
	DurationSec float64
	Subtitle    string
}

type Result struct {
	VideoPath   string
	SRT         []byte
	DurationSec float64
	Width       int
	Height      int
	WorkDir     string
}

const fps = 30

func Dimensions(ratio string) (int, int) {
	switch ratio {
	case "9:16":
		return 1080, 1920
	case "1:1":
		return 1080, 1080
	case "4:5":
		return 1080, 1350
	default:
		return 1920, 1080
	}
}

func (r *Renderer) Available() error {
	if _, err := exec.LookPath(r.FFmpeg); err != nil {
		return fmt.Errorf("ffmpeg tidak ditemukan (%s). Install ffmpeg atau set FFMPEG_PATH", r.FFmpeg)
	}
	if _, err := exec.LookPath(r.FFprobe); err != nil {
		return fmt.Errorf("ffprobe tidak ditemukan (%s). Install ffmpeg atau set FFPROBE_PATH", r.FFprobe)
	}
	return nil
}

// Duration returns a media file's duration in seconds.
func (r *Renderer) Duration(ctx context.Context, path string) (float64, error) {
	out, err := exec.CommandContext(ctx, r.FFprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe gagal membaca %s", filepath.Base(path))
	}
	var v struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(v.Format.Duration, 64)
	if err != nil {
		return 0, fmt.Errorf("durasi tidak terbaca untuk %s", filepath.Base(path))
	}
	return d, nil
}

func (r *Renderer) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, r.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return nil
}

// Render builds the final video. Caller must os.RemoveAll(result.WorkDir) when done.
func (r *Renderer) Render(ctx context.Context, ratio string, segs []Segment) (*Result, error) {
	if len(segs) == 0 {
		return nil, errors.New("tidak ada scene untuk dirender")
	}
	if err := r.Available(); err != nil {
		return nil, err
	}
	if r.TmpDir != "" {
		os.MkdirAll(r.TmpDir, 0o700)
	}
	work, err := os.MkdirTemp(r.TmpDir, "render-")
	if err != nil {
		return nil, err
	}
	res := &Result{WorkDir: work}
	w, h := Dimensions(ratio)
	res.Width, res.Height = w, h

	var list strings.Builder
	var cues []cue
	elapsed := 0.0
	for i, s := range segs {
		dur, audioDur, err := r.segmentDuration(ctx, s)
		if err != nil {
			os.RemoveAll(work)
			return nil, fmt.Errorf("scene %d: %w", i+1, err)
		}
		out := filepath.Join(work, fmt.Sprintf("seg_%03d.mp4", i))
		if err := r.renderSegment(ctx, s, w, h, dur, out); err != nil {
			os.RemoveAll(work)
			return nil, fmt.Errorf("scene %d: %w", i+1, err)
		}
		fmt.Fprintf(&list, "file '%s'\n", filepath.ToSlash(filepath.Base(out)))
		speak := audioDur
		if speak <= 0 || speak > dur {
			speak = dur
		}
		cues = append(cues, splitCues(s.Subtitle, elapsed, speak)...)
		elapsed += dur
	}
	listPath := filepath.Join(work, "list.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	final := filepath.Join(work, "final.mp4")
	// All segments share codec params → lossless concat; faststart puts moov first (IG/Threads requirement).
	if err := r.run(ctx, "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-movflags", "+faststart", final); err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	d, err := r.Duration(ctx, final)
	if err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	res.VideoPath, res.DurationSec, res.SRT = final, d, buildSRT(cues)
	return res, nil
}

// segmentDuration = max(scene duration or clip length, voice-over length + small tail).
func (r *Renderer) segmentDuration(ctx context.Context, s Segment) (float64, float64, error) {
	base := s.DurationSec
	if s.VisualKind == VisualVideo {
		vd, err := r.Duration(ctx, s.VisualPath)
		if err != nil {
			return 0, 0, err
		}
		base = vd
	}
	audio := 0.0
	if s.AudioPath != "" {
		ad, err := r.Duration(ctx, s.AudioPath)
		if err != nil {
			return 0, 0, err
		}
		audio = ad
		if ad+0.4 > base {
			base = ad + 0.4
		}
	}
	if base < 1 {
		base = 1
	}
	return math.Round(base*100) / 100, audio, nil
}

func (r *Renderer) renderSegment(ctx context.Context, s Segment, w, h int, dur float64, out string) error {
	args := []string{}
	switch s.VisualKind {
	case VisualVideo:
		args = append(args, "-i", s.VisualPath)
	case VisualImage:
		args = append(args, "-loop", "1", "-framerate", strconv.Itoa(fps), "-i", s.VisualPath)
	default:
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x111827:s=%dx%d:r=%d", w, h, fps))
	}
	if s.AudioPath != "" {
		args = append(args, "-i", s.AudioPath)
	} else {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
	}
	fade := 0.25
	if dur < 1.5 {
		fade = dur / 6
	}
	d := strconv.FormatFloat(dur, 'f', 2, 64)
	vf := fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p,"+
		"tpad=stop_mode=clone:stop_duration=%s,fade=t=in:st=0:d=%.2f,fade=t=out:st=%.2f:d=%.2f[v];"+
		"[1:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,apad[a]",
		w, h, w, h, fps, d, fade, dur-fade, fade)
	args = append(args,
		"-filter_complex", vf, "-map", "[v]", "-map", "[a]", "-t", d,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-profile:v", "high", "-r", strconv.Itoa(fps), "-g", strconv.Itoa(fps*2),
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		out)
	return r.run(ctx, args...)
}

// ---------- subtitles ----------

type cue struct {
	start, end float64
	text       string
}

// splitCues breaks a scene's voice-over into ≤10-word chunks, timed proportionally.
func splitCues(text string, start, dur float64) []cue {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 || dur <= 0 {
		return nil
	}
	var chunks [][]string
	for i := 0; i < len(words); i += 10 {
		j := i + 10
		if j > len(words) {
			j = len(words)
		}
		chunks = append(chunks, words[i:j])
	}
	var out []cue
	t := start
	for _, c := range chunks {
		d := dur * float64(len(c)) / float64(len(words))
		out = append(out, cue{start: t, end: t + d, text: strings.Join(c, " ")})
		t += d
	}
	return out
}

func srtTime(sec float64) string {
	ms := int(math.Round(sec * 1000))
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

func buildSRT(cues []cue) []byte {
	var b strings.Builder
	for i, c := range cues {
		text := c.text
		// wrap to two lines of ~42 chars
		if utf8.RuneCountInString(text) > 42 {
			ws := strings.Fields(text)
			half := len(ws) / 2
			text = strings.Join(ws[:half], " ") + "\n" + strings.Join(ws[half:], " ")
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.start), srtTime(c.end), text)
	}
	return []byte(b.String())
}
