// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package social

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// TikTok: Login Kit (web) + Content Posting API direct post with FILE_UPLOAD.
type TikTok struct {
	ClientKey, ClientSecret, RedirectURI string
	AuthBase, APIBase                    string
	PollEvery, PollTimeout               time.Duration
}

func NewTikTok(key, secret, redirect string) *TikTok {
	return &TikTok{ClientKey: key, ClientSecret: secret, RedirectURI: redirect,
		AuthBase: "https://www.tiktok.com/v2/auth/authorize/", APIBase: "https://open.tiktokapis.com/v2",
		PollEvery: 10 * time.Second, PollTimeout: 15 * time.Minute}
}

func (t *TikTok) Name() string     { return "tiktok" }
func (t *TikTok) Configured() bool { return t.ClientKey != "" && t.ClientSecret != "" }

func (t *TikTok) AuthURL(state string) string {
	q := url.Values{"client_key": {t.ClientKey}, "scope": {"user.info.basic,video.publish,video.upload"},
		"response_type": {"code"}, "redirect_uri": {t.RedirectURI}, "state": {state}}
	return t.AuthBase + "?" + q.Encode()
}

type tiktokToken struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	OpenID           string `json:"open_id"`
}

func (tt tiktokToken) token() *Token {
	return &Token{Access: tt.AccessToken, Refresh: tt.RefreshToken, ExpiresAt: expiresIn(tt.ExpiresIn), RefreshExpiresAt: expiresIn(tt.RefreshExpiresIn)}
}

func (t *TikTok) Exchange(ctx context.Context, code string) (*Token, *Profile, error) {
	var tt tiktokToken
	if err := postForm(ctx, t.APIBase+"/oauth/token/", url.Values{
		"client_key": {t.ClientKey}, "client_secret": {t.ClientSecret}, "code": {code},
		"grant_type": {"authorization_code"}, "redirect_uri": {t.RedirectURI},
	}, nil, &tt); err != nil {
		return nil, nil, fmt.Errorf("TikTok token: %w", err)
	}
	if tt.AccessToken == "" {
		return nil, nil, errors.New("TikTok token: respons tanpa access_token")
	}
	var u struct {
		Data struct {
			User struct {
				OpenID      string `json:"open_id"`
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"data"`
	}
	name := tt.OpenID
	if err := getJSON(ctx, t.APIBase+"/user/info/?fields=open_id,display_name", bearer(tt.AccessToken), &u); err == nil && u.Data.User.DisplayName != "" {
		name = u.Data.User.DisplayName
	}
	return tt.token(), &Profile{ExternalID: tt.OpenID, DisplayName: name}, nil
}

func (t *TikTok) Refresh(ctx context.Context, tok *Token, _ time.Time, force bool) (*Token, error) {
	needAccess := tok.ExpiresAt == nil || time.Until(*tok.ExpiresAt) < 5*time.Minute
	needRotate := tok.RefreshExpiresAt != nil && time.Until(*tok.RefreshExpiresAt) < 30*24*time.Hour
	if !force && !needAccess && !needRotate {
		return nil, nil
	}
	var tt tiktokToken
	if err := postForm(ctx, t.APIBase+"/oauth/token/", url.Values{
		"client_key": {t.ClientKey}, "client_secret": {t.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {tok.Refresh},
	}, nil, &tt); err != nil {
		return nil, fmt.Errorf("TikTok refresh: %w", err)
	}
	// Refresh tokens rotate — the new one replaces the old.
	return tt.token(), nil
}

type tiktokEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e tiktokEnvelope) err() error {
	if e.Error.Code != "" && e.Error.Code != "ok" {
		return fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
	}
	return nil
}

func (t *TikTok) Publish(ctx context.Context, tok *Token, r PublishRequest) (*PublishResult, error) {
	h := bearer(tok.Access)
	// 1) creator info: allowed privacy levels and max duration
	var ci struct {
		tiktokEnvelope
		Data struct {
			CreatorUsername      string   `json:"creator_username"`
			PrivacyLevelOptions  []string `json:"privacy_level_options"`
			MaxVideoPostDuration int      `json:"max_video_post_duration_sec"`
		} `json:"data"`
	}
	if err := postJSON(ctx, t.APIBase+"/post/publish/creator_info/query/", map[string]any{}, h, &ci); err != nil {
		return nil, retryable(fmt.Errorf("TikTok creator info: %w", err))
	}
	if err := ci.err(); err != nil {
		return nil, fmt.Errorf("TikTok creator info: %w", err)
	}
	if ci.Data.MaxVideoPostDuration > 0 && r.DurationSec > float64(ci.Data.MaxVideoPostDuration) {
		return nil, fmt.Errorf("TikTok: durasi %.0f dtk melebihi batas akun %d dtk", r.DurationSec, ci.Data.MaxVideoPostDuration)
	}
	want := "SELF_ONLY"
	if r.Privacy == "public" {
		want = "PUBLIC_TO_EVERYONE"
	}
	allowed := false
	for _, o := range ci.Data.PrivacyLevelOptions {
		if o == want {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("TikTok: privasi %s tidak tersedia untuk akun ini (opsi: %s)", want, strings.Join(ci.Data.PrivacyLevelOptions, ", "))
	}

	// 2) init direct post with FILE_UPLOAD
	chunk, count := tiktokChunks(r.VideoSize)
	var initResp struct {
		tiktokEnvelope
		Data struct {
			PublishID string `json:"publish_id"`
			UploadURL string `json:"upload_url"`
		} `json:"data"`
	}
	body := map[string]any{
		"post_info": map[string]any{
			"title": truncateRunes(r.Caption, 2200), "privacy_level": want,
			"disable_duet": false, "disable_comment": false, "disable_stitch": false,
			"is_aigc": r.AIGenerated,
		},
		"source_info": map[string]any{"source": "FILE_UPLOAD", "video_size": r.VideoSize, "chunk_size": chunk, "total_chunk_count": count},
	}
	if err := postJSON(ctx, t.APIBase+"/post/publish/video/init/", body, h, &initResp); err != nil {
		return nil, retryable(fmt.Errorf("TikTok init: %w", err))
	}
	if err := initResp.err(); err != nil {
		return nil, fmt.Errorf("TikTok init: %w", err)
	}

	// 3) upload chunks sequentially
	f, err := os.Open(r.VideoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for i := int64(0); i < count; i++ {
		start := i * chunk
		end := start + chunk - 1
		if i == count-1 {
			end = r.VideoSize - 1 // final chunk absorbs the remainder
		}
		size := end - start + 1
		buf := make([]byte, size)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, initResp.Data.UploadURL, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.ContentLength = size
		req.Header.Set("Content-Type", "video/mp4")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, r.VideoSize))
		if _, err := doReq(req, nil); err != nil {
			return nil, fmt.Errorf("TikTok upload chunk %d/%d: %w", i+1, count, err)
		}
	}

	// 4) poll status (content is submitted now — never retry from here on)
	profile := "https://www.tiktok.com/@" + ci.Data.CreatorUsername
	deadline := time.Now().Add(t.PollTimeout)
	for time.Now().Before(deadline) {
		if err := sleep(ctx, t.PollEvery); err != nil {
			return nil, err
		}
		var st struct {
			tiktokEnvelope
			Data struct {
				Status     string  `json:"status"`
				FailReason string  `json:"fail_reason"`
				PostIDs    []int64 `json:"publicaly_available_post_id"`
			} `json:"data"`
		}
		if err := postJSON(ctx, t.APIBase+"/post/publish/status/fetch/", map[string]string{"publish_id": initResp.Data.PublishID}, h, &st); err != nil {
			continue // transient; keep polling
		}
		switch st.Data.Status {
		case "PUBLISH_COMPLETE":
			if len(st.Data.PostIDs) > 0 {
				id := strconv.FormatInt(st.Data.PostIDs[0], 10)
				return &PublishResult{ExternalID: id, URL: profile + "/video/" + id}, nil
			}
			return &PublishResult{ExternalID: initResp.Data.PublishID, URL: profile,
				Note: "Terbit, menunggu moderasi TikTok sebelum link publik tersedia."}, nil
		case "FAILED":
			return nil, fmt.Errorf("TikTok gagal: %s", st.Data.FailReason)
		}
	}
	return &PublishResult{ExternalID: initResp.Data.PublishID, URL: profile,
		Note: "Video terkirim; TikTok masih memproses. Cek aplikasi TikTok."}, nil
}

// tiktokChunks follows the media transfer rules: <=64 MB → one chunk; otherwise
// 10 MB chunks with the final chunk absorbing the remainder (< 20 MB, under the 128 MB cap).
func tiktokChunks(size int64) (chunk, count int64) {
	const single = 64 * 1000 * 1000
	const c = 10 * 1000 * 1000
	if size <= single {
		return size, 1
	}
	return c, size / c
}
