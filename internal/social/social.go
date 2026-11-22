// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package social publishes the final video to YouTube (Shorts), Instagram (Reels),
// Threads and TikTok via their official APIs. Wire formats were checked against the
// official docs (Nov 2026): YouTube Data API v3 resumable upload, TikTok Content
// Posting API (direct post, FILE_UPLOAD), Instagram API with Instagram Login
// (graph.instagram.com) and Threads API (graph.threads.com).
package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Token struct {
	Access           string
	Refresh          string
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
}

type Profile struct {
	ExternalID  string
	DisplayName string
}

type PublishRequest struct {
	ExternalUserID string
	VideoPath      string // local file (YouTube, TikTok upload the bytes)
	VideoSize      int64
	DurationSec    float64
	PublicURL      string // signed temporary URL (Instagram, Threads fetch it)
	Title          string
	Caption        string
	Privacy        string // "public" | "private" (| "unlisted" for YouTube)
	AIGenerated    bool
}

type PublishResult struct {
	ExternalID string
	URL        string
	Note       string
}

// Platform is one social network integration.
type Platform interface {
	Name() string
	Configured() bool
	AuthURL(state string) string
	Exchange(ctx context.Context, code string) (*Token, *Profile, error)
	// Refresh returns a new token, or nil if no refresh is needed yet.
	Refresh(ctx context.Context, t *Token, refreshedAt time.Time, force bool) (*Token, error)
	Publish(ctx context.Context, t *Token, r PublishRequest) (*PublishResult, error)
}

// RetryableError marks failures that happened before the content reached the platform,
// so retrying cannot create a duplicate post.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

func retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

func IsRetryable(err error) bool {
	var r *RetryableError
	return errors.As(err, &r)
}

// ---------- HTTP helpers (never leak tokens into errors) ----------

var httpClient = &http.Client{Timeout: 30 * time.Minute}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg) }

// sanitize strips URLs (which may carry access_token query params) from transport errors.
func sanitize(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("request to %s failed: %v", hostOf(ue.URL), ue.Err)
	}
	return err
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return "remote"
}

func do(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, sanitize(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doReq(req, out)
}

func doReq(req *http.Request, out any) (*http.Response, error) {
	rawURL := req.URL.String()
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitize(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 10<<20))
	if err != nil {
		return res, err
	}
	if res.StatusCode >= 300 && res.StatusCode != http.StatusPermanentRedirect {
		return res, &apiError{Status: res.StatusCode, Msg: errorMessage(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return res, fmt.Errorf("unexpected response from %s", hostOf(rawURL))
		}
	}
	return res, nil
}

// errorMessage extracts a human message from the various error shapes.
func errorMessage(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		s := string(data)
		if len(s) > 300 {
			s = s[:300]
		}
		return s
	}
	if e, ok := m["error"].(map[string]any); ok {
		parts := []string{}
		for _, k := range []string{"code", "message", "error_user_msg"} {
			if v, ok := e[k]; ok && fmt.Sprint(v) != "" && fmt.Sprint(v) != "ok" {
				parts = append(parts, fmt.Sprint(v))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}
	if e, ok := m["error"].(string); ok {
		if d, ok := m["error_description"].(string); ok {
			return e + ": " + d
		}
		return e
	}
	if msg, ok := m["error_message"].(string); ok {
		return msg
	}
	b, _ := json.Marshal(m)
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}

func postForm(ctx context.Context, rawURL string, form url.Values, headers map[string]string, out any) error {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		h[k] = v
	}
	_, err := do(ctx, http.MethodPost, rawURL, h, strings.NewReader(form.Encode()), out)
	return err
}

func postJSON(ctx context.Context, rawURL string, body any, headers map[string]string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	h := map[string]string{"Content-Type": "application/json; charset=UTF-8"}
	for k, v := range headers {
		h[k] = v
	}
	_, err = do(ctx, http.MethodPost, rawURL, h, bytes.NewReader(b), out)
	return err
}

func postJSONResp(ctx context.Context, rawURL string, body any, headers map[string]string) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Content-Type": "application/json; charset=UTF-8"}
	for k, v := range headers {
		h[k] = v
	}
	return do(ctx, http.MethodPost, rawURL, h, bytes.NewReader(b), nil)
}

func getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	_, err := do(ctx, http.MethodGet, rawURL, headers, nil, out)
	return err
}

func bearer(t string) map[string]string { return map[string]string{"Authorization": "Bearer " + t} }

func expiresIn(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Now().Add(time.Duration(sec) * time.Second)
	return &t
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// anyString converts JSON numbers/strings (ids are numeric in some Meta responses).
func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	case json.Number:
		return t.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
