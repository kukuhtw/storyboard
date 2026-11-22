// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package social

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// YouTube: Google OAuth web-server flow + Data API v3 resumable upload.
type YouTube struct {
	ClientID, ClientSecret, RedirectURI     string
	AuthBase, TokenURL, APIBase, UploadBase string // overridable for tests
}

func NewYouTube(id, secret, redirect string) *YouTube {
	return &YouTube{ClientID: id, ClientSecret: secret, RedirectURI: redirect,
		AuthBase: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
		APIBase: "https://www.googleapis.com/youtube/v3", UploadBase: "https://www.googleapis.com/upload/youtube/v3"}
}

func (y *YouTube) Name() string     { return "youtube" }
func (y *YouTube) Configured() bool { return y.ClientID != "" && y.ClientSecret != "" }

func (y *YouTube) AuthURL(state string) string {
	q := url.Values{
		"client_id": {y.ClientID}, "redirect_uri": {y.RedirectURI}, "response_type": {"code"},
		"scope":       {"https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"},
		"access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"}, "state": {state},
	}
	return y.AuthBase + "?" + q.Encode()
}

type googleToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (y *YouTube) Exchange(ctx context.Context, code string) (*Token, *Profile, error) {
	var gt googleToken
	err := postForm(ctx, y.TokenURL, url.Values{
		"code": {code}, "client_id": {y.ClientID}, "client_secret": {y.ClientSecret},
		"redirect_uri": {y.RedirectURI}, "grant_type": {"authorization_code"},
	}, nil, &gt)
	if err != nil {
		return nil, nil, fmt.Errorf("YouTube token: %w", err)
	}
	tok := &Token{Access: gt.AccessToken, Refresh: gt.RefreshToken, ExpiresAt: expiresIn(gt.ExpiresIn)}
	var ch struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := getJSON(ctx, y.APIBase+"/channels?part=snippet&mine=true", bearer(tok.Access), &ch); err != nil {
		return nil, nil, fmt.Errorf("YouTube channel: %w", err)
	}
	if len(ch.Items) == 0 {
		return nil, nil, errors.New("akun Google ini belum memiliki channel YouTube")
	}
	return tok, &Profile{ExternalID: ch.Items[0].ID, DisplayName: ch.Items[0].Snippet.Title}, nil
}

func (y *YouTube) Refresh(ctx context.Context, t *Token, _ time.Time, force bool) (*Token, error) {
	if !force && t.ExpiresAt != nil && time.Until(*t.ExpiresAt) > 5*time.Minute {
		return nil, nil
	}
	if t.Refresh == "" {
		return nil, errors.New("YouTube: refresh token tidak ada, hubungkan ulang akun")
	}
	var gt googleToken
	err := postForm(ctx, y.TokenURL, url.Values{
		"client_id": {y.ClientID}, "client_secret": {y.ClientSecret},
		"refresh_token": {t.Refresh}, "grant_type": {"refresh_token"},
	}, nil, &gt)
	if err != nil {
		return nil, fmt.Errorf("YouTube refresh: %w", err)
	}
	// Google does not return a new refresh token on refresh; keep the old one.
	return &Token{Access: gt.AccessToken, Refresh: t.Refresh, ExpiresAt: expiresIn(gt.ExpiresIn)}, nil
}

func (y *YouTube) Publish(ctx context.Context, t *Token, r PublishRequest) (*PublishResult, error) {
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = truncateRunes(strings.TrimSpace(r.Caption), 90)
	}
	title = truncateRunes(strings.NewReplacer("<", "", ">", "").Replace(title), 100)
	desc := r.Caption
	if !strings.Contains(strings.ToLower(desc), "#shorts") {
		desc = strings.TrimSpace(desc + "\n\n#Shorts")
	}
	desc = truncateRunes(strings.NewReplacer("<", "", ">", "").Replace(desc), 5000)
	privacy := r.Privacy
	if privacy != "private" && privacy != "unlisted" {
		privacy = "public"
	}
	meta := map[string]any{
		"snippet": map[string]any{"title": title, "description": desc, "categoryId": "22"},
		"status": map[string]any{
			"privacyStatus": privacy, "selfDeclaredMadeForKids": false,
			"containsSyntheticMedia": r.AIGenerated, // disclosure of realistic altered/synthetic content
		},
	}
	// 1) start resumable session
	initURL := y.UploadBase + "/videos?uploadType=resumable&part=" + url.QueryEscape("snippet,status")
	h := bearer(t.Access)
	h["X-Upload-Content-Type"] = "video/mp4"
	h["X-Upload-Content-Length"] = strconv.FormatInt(r.VideoSize, 10)
	res, err := postJSONResp(ctx, initURL, meta, h)
	if err != nil {
		return nil, retryable(fmt.Errorf("YouTube upload init: %w", err))
	}
	session := res.Header.Get("Location")
	if session == "" {
		return nil, retryable(errors.New("YouTube upload init: tidak ada session URI"))
	}
	// 2) upload bytes
	f, err := os.Open(r.VideoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, f)
	if err != nil {
		return nil, err
	}
	req.ContentLength = r.VideoSize
	req.Header.Set("Authorization", "Bearer "+t.Access)
	req.Header.Set("Content-Type", "video/mp4")
	var video struct {
		ID string `json:"id"`
	}
	if _, err := doReq(req, &video); err != nil {
		return nil, fmt.Errorf("YouTube upload: %w", err)
	}
	if video.ID == "" {
		return nil, errors.New("YouTube upload: id video kosong")
	}
	return &PublishResult{ExternalID: video.ID, URL: "https://www.youtube.com/shorts/" + video.ID,
		Note: "Diunggah. YouTube mengklasifikasikan Shorts otomatis (vertikal, ≤3 menit)."}, nil
}
