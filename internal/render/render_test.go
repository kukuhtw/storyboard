// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package render

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Integration test: requires ffmpeg/ffprobe (set FFMPEG_PATH/FFPROBE_PATH or have them on PATH).
func TestRenderEndToEnd(t *testing.T) {
	ff, fp := os.Getenv("FFMPEG_PATH"), os.Getenv("FFPROBE_PATH")
	if ff == "" {
		ff = "ffmpeg"
	}
	if fp == "" {
		fp = "ffprobe"
	}
	r := &Renderer{FFmpeg: ff, FFprobe: fp, TmpDir: t.TempDir()}
	if err := r.Available(); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	gen := func(args ...string) string {
		out := filepath.Join(dir, args[len(args)-1])
		args[len(args)-1] = out
		cmd := exec.Command(ff, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gen %v: %v %s", args, err, b)
		}
		return out
	}
	clip := gen("-f", "lavfi", "-i", "testsrc=size=640x360:rate=24:duration=2", "-pix_fmt", "yuv420p", "clip.mp4")
	img := gen("-f", "lavfi", "-i", "color=c=red:s=800x600:d=1", "-frames:v", "1", "img.png")
	vo := gen("-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:a", "libmp3lame", "vo.mp3")

	res, err := r.Render(ctx, "9:16", []Segment{
		{VisualKind: VisualVideo, VisualPath: clip, AudioPath: vo, DurationSec: 5, Subtitle: "Halo semua, selamat datang di tutorial klaim online kami hari ini."},
		{VisualKind: VisualImage, VisualPath: img, DurationSec: 2, Subtitle: ""},
		{VisualKind: VisualNone, AudioPath: vo, DurationSec: 1, Subtitle: "Terima kasih."},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(res.WorkDir)

	// seg1: clip 2s < VO 3s → 3.4s ; seg2: image 2s ; seg3: VO 3s → 3.4s  ≈ 8.8s
	if res.DurationSec < 8.3 || res.DurationSec > 9.4 {
		t.Fatalf("unexpected duration %.2f", res.DurationSec)
	}
	if res.Width != 1080 || res.Height != 1920 {
		t.Fatalf("dims %dx%d", res.Width, res.Height)
	}
	out, err := exec.Command(fp, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height,pix_fmt", "-of", "csv=p=0", res.VideoPath).Output()
	if err != nil || strings.TrimSpace(string(out)) != "h264,1080,1920,yuv420p" {
		t.Fatalf("video stream: %q %v", out, err)
	}
	out, _ = exec.Command(fp, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name,sample_rate,channels", "-of", "csv=p=0", res.VideoPath).Output()
	if strings.TrimSpace(string(out)) != "aac,48000,2" {
		t.Fatalf("audio stream: %q", out)
	}
	srt := string(res.SRT)
	if !strings.Contains(srt, "00:00:00,000 --> ") || !strings.Contains(srt, "Terima kasih.") || strings.Count(srt, "-->") != 3 {
		t.Fatalf("srt:\n%s", srt)
	}
}

func TestSplitCuesAndSRT(t *testing.T) {
	c := splitCues(strings.Repeat("kata ", 25), 10, 5)
	if len(c) != 3 || c[0].start != 10 || c[2].end < 14.99 || c[2].end > 15.01 {
		t.Fatalf("cues %+v", c)
	}
	if got := srtTime(3725.5); got != "01:02:05,500" {
		t.Fatal(got)
	}
}
