// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package providers abstracts image, video and text-to-speech generation.
//
// Two implementations per capability:
//   - mock: offline placeholders so the whole pipeline can be exercised without keys.
//   - http: a generic JSON adapter. Point *_API_URL at your vendor (or a thin proxy that
//     translates to the vendor's API). Contract documented in README.md.
package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"storyboard/internal/config"
)

type Media struct {
	Mime string
	Data []byte
}

type ImageRequest struct {
	Prompt          string   `json:"prompt"`
	AspectRatio     string   `json:"aspect_ratio"`
	ReferenceImages []string `json:"reference_images,omitempty"` // data URLs
}

type VideoRequest struct {
	Prompt          string   `json:"prompt"`
	AspectRatio     string   `json:"aspect_ratio"`
	DurationSec     float64  `json:"duration_sec"`
	StartImage      string   `json:"start_image,omitempty"`      // data URL of the approved scene image
	ReferenceVideos []string `json:"reference_videos,omitempty"` // data URLs
}

type TTSRequest struct {
	Text     string `json:"text"`
	Voice    string `json:"voice"`
	Language string `json:"language"`
}

type ImageGen interface {
	GenerateImage(ctx context.Context, r ImageRequest) (*Media, error)
}
type VideoGen interface {
	GenerateVideo(ctx context.Context, r VideoRequest) (*Media, error)
}
type TTS interface {
	Synthesize(ctx context.Context, r TTSRequest) (*Media, error)
}

func DataURL(m Media) string {
	return "data:" + m.Mime + ";base64," + base64.StdEncoding.EncodeToString(m.Data)
}

// ---------------- mock ----------------

type Mock struct{}

func (Mock) GenerateImage(_ context.Context, r ImageRequest) (*Media, error) {
	w, h := 1280, 720
	switch r.AspectRatio {
	case "9:16":
		w, h = 720, 1280
	case "1:1":
		w, h = 1024, 1024
	case "4:5":
		w, h = 1024, 1280
	}
	return &Media{Mime: "image/svg+xml", Data: []byte(placeholderSVG(w, h, "MOCK IMAGE", r.Prompt))}, nil
}

// Mock video returns an SVG "animatic card" (no real video encoder needed offline).
func (Mock) GenerateVideo(_ context.Context, r VideoRequest) (*Media, error) {
	w, h := 1280, 720
	if r.AspectRatio == "9:16" {
		w, h = 720, 1280
	}
	return &Media{Mime: "image/svg+xml", Data: []byte(placeholderSVG(w, h, fmt.Sprintf("MOCK VIDEO · %.0fs", r.DurationSec), r.Prompt))}, nil
}

// Mock TTS returns a silent WAV whose length approximates reading speed.
func (Mock) Synthesize(_ context.Context, r TTSRequest) (*Media, error) {
	words := len(strings.Fields(r.Text))
	secs := float64(words)/2.5 + 0.5
	return &Media{Mime: "audio/wav", Data: silentWAV(secs)}, nil
}

func placeholderSVG(w, h int, label, prompt string) string {
	if len(prompt) > 600 {
		prompt = prompt[:600] + "…"
	}
	var lines []string
	words := strings.Fields(prompt)
	cur := ""
	maxChars := w / 14
	for _, wd := range words {
		if len(cur)+len(wd)+1 > maxChars {
			lines = append(lines, cur)
			cur = wd
		} else if cur == "" {
			cur = wd
		} else {
			cur += " " + wd
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, h, w, h)
	fmt.Fprintf(&b, `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#1e293b"/><stop offset="1" stop-color="#4c1d95"/></linearGradient></defs>`)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="url(#g)"/>`)
	fmt.Fprintf(&b, `<text x="40" y="70" font-family="sans-serif" font-size="34" font-weight="700" fill="#fbbf24">%s</text>`, html.EscapeString(label))
	for i, l := range lines {
		if i > 22 {
			break
		}
		fmt.Fprintf(&b, `<text x="40" y="%d" font-family="sans-serif" font-size="22" fill="#e2e8f0">%s</text>`, 130+i*32, html.EscapeString(l))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func silentWAV(secs float64) []byte {
	const rate = 16000
	n := int(secs * rate)
	var buf bytes.Buffer
	dataLen := uint32(n * 2)
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, 36+dataLen)
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataLen)
	buf.Write(make([]byte, dataLen))
	return buf.Bytes()
}

// ---------------- generic HTTP ----------------

// HTTP posts the request as JSON to URL with "Authorization: Bearer <APIKey>".
// Accepted responses (JSON):
//
//	{"b64": "...", "mime": "image/png"}                 inline result
//	{"url": "https://..."}                              downloadable result
//	{"status_url": "https://...", "status": "queued"}   async; polled until
//	    {"status":"succeeded","url"|"b64":...} or {"status":"failed","error":"..."}
type HTTP struct {
	cfg    config.Provider
	client *http.Client
	// PollEvery / PollTimeout control async jobs (video generation is usually async).
	PollEvery   time.Duration
	PollTimeout time.Duration
	MaxBytes    int64
}

func NewHTTP(cfg config.Provider) *HTTP {
	return &HTTP{cfg: cfg, client: &http.Client{Timeout: 5 * time.Minute}, PollEvery: 5 * time.Second, PollTimeout: 20 * time.Minute, MaxBytes: 500 << 20}
}

type httpResp struct {
	B64       string `json:"b64"`
	Mime      string `json:"mime"`
	URL       string `json:"url"`
	StatusURL string `json:"status_url"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

func (h *HTTP) GenerateImage(ctx context.Context, r ImageRequest) (*Media, error) {
	return h.call(ctx, r)
}
func (h *HTTP) GenerateVideo(ctx context.Context, r VideoRequest) (*Media, error) {
	return h.call(ctx, r)
}
func (h *HTTP) Synthesize(ctx context.Context, r TTSRequest) (*Media, error) { return h.call(ctx, r) }

func (h *HTTP) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
	}
	req.Header.Set("User-Agent", "storyboard-studio/1.0")
	return h.client.Do(req)
}

func (h *HTTP) call(ctx context.Context, body any) (*Media, error) {
	res, err := h.do(ctx, http.MethodPost, h.cfg.URL, body)
	if err != nil {
		return nil, fmt.Errorf("provider request failed: %w", err)
	}
	r, err := h.decode(res)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(h.PollTimeout)
	for r.StatusURL != "" && r.B64 == "" && r.URL == "" {
		if r.Status == "failed" {
			return nil, fmt.Errorf("provider job failed: %s", r.Error)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("provider job timed out")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(h.PollEvery):
		}
		res, err := h.do(ctx, http.MethodGet, r.StatusURL, nil)
		if err != nil {
			return nil, err
		}
		next, err := h.decode(res)
		if err != nil {
			return nil, err
		}
		if next.StatusURL == "" {
			next.StatusURL = r.StatusURL
		}
		r = next
	}
	if r.Status == "failed" {
		return nil, fmt.Errorf("provider job failed: %s", r.Error)
	}
	if r.B64 != "" {
		data, err := base64.StdEncoding.DecodeString(r.B64)
		if err != nil {
			return nil, fmt.Errorf("provider returned invalid base64: %w", err)
		}
		return &Media{Mime: nzMime(r.Mime, http.DetectContentType(data)), Data: data}, nil
	}
	if r.URL != "" {
		return h.download(ctx, r.URL, r.Mime)
	}
	return nil, errors.New("provider returned no media")
}

func (h *HTTP) decode(res *http.Response) (*httpResp, error) {
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, h.MaxBytes))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		msg := string(b)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("provider HTTP %d: %s", res.StatusCode, msg)
	}
	var r httpResp
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("provider returned non-JSON response")
	}
	return &r, nil
}

func (h *HTTP) download(ctx context.Context, url, mime string) (*Media, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, errors.New("provider returned unsupported URL scheme")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("download HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, h.MaxBytes))
	if err != nil {
		return nil, err
	}
	ct := strings.Split(res.Header.Get("Content-Type"), ";")[0]
	return &Media{Mime: nzMime(mime, nzMime(ct, http.DetectContentType(data))), Data: data}, nil
}

func nzMime(a, b string) string {
	if a != "" && a != "application/octet-stream" {
		return a
	}
	return b
}

// Set bundles the configured providers.
type Set struct {
	Image ImageGen
	Video VideoGen
	TTS   TTS
	Names map[string]string
}

func Build(c *config.Config) *Set {
	s := &Set{Names: map[string]string{}}
	var bp *BytePlus
	if c.Image.Kind == "byteplus" || c.Video.Kind == "byteplus" {
		b := c.BytePlus
		bp = NewBytePlus(BytePlusConfig{
			BaseURL: b.BaseURL, APIKey: b.APIKey, ImageModel: b.ImageModel, VideoModel: b.VideoModel,
			ImageSize: b.ImageSize, VideoResolution: b.VideoResolution,
			VideoMinSec: b.VideoMinSec, VideoMaxSec: b.VideoMaxSec, Watermark: b.Watermark,
		})
	}
	switch c.Image.Kind {
	case "http":
		s.Image = NewHTTP(c.Image)
	case "byteplus":
		s.Image = bp
	default:
		s.Image = Mock{}
	}
	switch c.Video.Kind {
	case "http":
		s.Video = NewHTTP(c.Video)
	case "byteplus":
		s.Video = bp
	default:
		s.Video = Mock{}
	}
	switch c.TTS.Kind {
	case "http":
		s.TTS = NewHTTP(c.TTS)
	case "openai":
		o := c.OpenAITTS
		s.TTS = NewOpenAITTS(OpenAITTSConfig{BaseURL: o.BaseURL, APIKey: o.APIKey, Model: o.Model, Voice: o.Voice, Instructions: o.Instructions, Speed: o.Speed})
	default:
		s.TTS = Mock{}
	}
	s.Names["image"], s.Names["video"], s.Names["tts"] = c.Image.Kind, c.Video.Kind, c.TTS.Kind
	return s
}
