// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package providers

// BytePlus ModelArk adapter (Seedream = image, Seedance = video).
// Wire format taken from the official SDK github.com/byteplus-sdk/byteplus-go-sdk-v2
// (service/arkruntime, v1.0.86): POST /images/generations,
// POST /contents/generations/tasks, GET /contents/generations/tasks/{id}.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type BytePlusConfig struct {
	BaseURL         string // default https://ark.ap-southeast.bytepluses.com/api/v3
	APIKey          string
	ImageModel      string // model ID or endpoint ID (ep-...) from the ModelArk console
	VideoModel      string
	ImageSize       string // override, e.g. "2K" or "2048x1152"; empty = derived from aspect ratio
	VideoResolution string // e.g. "720p", "1080p"
	VideoMinSec     int
	VideoMaxSec     int
	Watermark       bool
}

type BytePlus struct {
	cfg         BytePlusConfig
	client      *http.Client
	PollEvery   time.Duration
	PollTimeout time.Duration
	MaxBytes    int64
}

func NewBytePlus(c BytePlusConfig) *BytePlus {
	if c.BaseURL == "" {
		c.BaseURL = "https://ark.ap-southeast.bytepluses.com/api/v3"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.VideoMinSec <= 0 {
		c.VideoMinSec = 5
	}
	if c.VideoMaxSec < c.VideoMinSec {
		c.VideoMaxSec = 10
	}
	return &BytePlus{cfg: c, client: &http.Client{Timeout: 5 * time.Minute},
		PollEvery: 5 * time.Second, PollTimeout: 30 * time.Minute, MaxBytes: 500 << 20}
}

// ---------- wire types (subset of the SDK's model package) ----------

type bpImageReq struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Image          any    `json:"image,omitempty"` // string or []string (URL or data URL)
	ResponseFormat string `json:"response_format,omitempty"`
	Size           string `json:"size,omitempty"`
	Watermark      *bool  `json:"watermark,omitempty"`
}

type bpImageResp struct {
	Data []struct {
		URL     *string `json:"url"`
		B64JSON *string `json:"b64_json"`
	} `json:"data"`
	Error *bpError `json:"error"`
}

type bpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bpContentItem struct {
	Type     string `json:"type"` // text | image_url
	Text     string `json:"text,omitempty"`
	ImageURL *bpURL `json:"image_url,omitempty"`
	Role     string `json:"role,omitempty"` // first_frame | reference_image
}

type bpURL struct {
	URL string `json:"url"`
}

type bpTaskReq struct {
	Model      string          `json:"model"`
	Content    []bpContentItem `json:"content"`
	Ratio      string          `json:"ratio,omitempty"`
	Duration   int64           `json:"duration,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	Watermark  *bool           `json:"watermark,omitempty"`
}

type bpTask struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"` // queued | running | succeeded | failed | cancelled
	Error   *bpError `json:"error"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
}

// ---------- HTTP plumbing ----------

func (b *BytePlus) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("BytePlus request failed: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, b.MaxBytes))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error *bpError `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != nil {
			return fmt.Errorf("BytePlus HTTP %d %s: %s", res.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("BytePlus HTTP %d", res.StatusCode)
	}
	return json.Unmarshal(data, out)
}

func (b *BytePlus) download(ctx context.Context, url, fallbackMime string) (*Media, error) {
	h := &HTTP{client: b.client, MaxBytes: b.MaxBytes}
	return h.download(ctx, url, fallbackMime)
}

// ---------- image (Seedream) ----------

// imageSize maps the project aspect ratio to a pixel size (~2K long edge).
func (b *BytePlus) imageSize(ratio string) string {
	if b.cfg.ImageSize != "" {
		return b.cfg.ImageSize
	}
	switch ratio {
	case "9:16":
		return "1440x2560"
	case "1:1":
		return "2048x2048"
	case "4:5":
		return "1792x2240"
	default:
		return "2560x1440"
	}
}

func (b *BytePlus) GenerateImage(ctx context.Context, r ImageRequest) (*Media, error) {
	if b.cfg.ImageModel == "" {
		return nil, errors.New("BYTEPLUS_IMAGE_MODEL belum diisi")
	}
	req := bpImageReq{
		Model: b.cfg.ImageModel, Prompt: r.Prompt, ResponseFormat: "url",
		Size: b.imageSize(r.AspectRatio), Watermark: &b.cfg.Watermark,
	}
	switch len(r.ReferenceImages) {
	case 0:
	case 1:
		req.Image = r.ReferenceImages[0]
	default:
		req.Image = r.ReferenceImages
	}
	var out bpImageResp
	if err := b.do(ctx, http.MethodPost, "/images/generations", req, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("BytePlus image: %s %s", out.Error.Code, out.Error.Message)
	}
	if len(out.Data) == 0 {
		return nil, errors.New("BytePlus image: respons kosong")
	}
	d := out.Data[0]
	if d.B64JSON != nil && *d.B64JSON != "" {
		raw, err := base64.StdEncoding.DecodeString(*d.B64JSON)
		if err != nil {
			return nil, err
		}
		return &Media{Mime: http.DetectContentType(raw), Data: raw}, nil
	}
	if d.URL != nil && *d.URL != "" {
		// Result URLs are temporary; download immediately into our own storage.
		return b.download(ctx, *d.URL, "image/jpeg")
	}
	return nil, errors.New("BytePlus image: tidak ada url/b64_json")
}

// ---------- video (Seedance) ----------

func (b *BytePlus) duration(sec float64) int64 {
	d := int(math.Round(sec))
	if d < b.cfg.VideoMinSec {
		d = b.cfg.VideoMinSec
	}
	if d > b.cfg.VideoMaxSec {
		d = b.cfg.VideoMaxSec
	}
	return int64(d)
}

func (b *BytePlus) GenerateVideo(ctx context.Context, r VideoRequest) (*Media, error) {
	if b.cfg.VideoModel == "" {
		return nil, errors.New("BYTEPLUS_VIDEO_MODEL belum diisi")
	}
	content := []bpContentItem{{Type: "text", Text: r.Prompt}}
	if r.StartImage != "" {
		// Image-to-video: the approved scene keyframe becomes the first frame.
		content = append(content, bpContentItem{Type: "image_url", ImageURL: &bpURL{URL: r.StartImage}, Role: "first_frame"})
	}
	req := bpTaskReq{
		Model: b.cfg.VideoModel, Content: content, Ratio: r.AspectRatio,
		Duration: b.duration(r.DurationSec), Resolution: b.cfg.VideoResolution, Watermark: &b.cfg.Watermark,
	}
	var created bpTask
	if err := b.do(ctx, http.MethodPost, "/contents/generations/tasks", req, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("BytePlus video: task id kosong")
	}
	deadline := time.Now().Add(b.PollTimeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(b.PollEvery):
		}
		var t bpTask
		if err := b.do(ctx, http.MethodGet, "/contents/generations/tasks/"+created.ID, nil, &t); err != nil {
			return nil, err
		}
		switch t.Status {
		case "succeeded":
			if t.Content.VideoURL == "" {
				return nil, errors.New("BytePlus video: sukses tanpa video_url")
			}
			return b.download(ctx, t.Content.VideoURL, "video/mp4")
		case "failed", "cancelled", "expired":
			msg := t.Status
			if t.Error != nil {
				msg += ": " + t.Error.Code + " " + t.Error.Message
			}
			return nil, errors.New("BytePlus video " + msg)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("BytePlus video: timeout menunggu task %s", created.ID)
		}
	}
}
