// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package social

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// metaPlatform implements Instagram (Instagram API with Instagram Login) and Threads.
// Both: OAuth code → short-lived token → long-lived (60 d) token, refreshable after 24 h;
// publishing = create container from a public video_url → poll status → publish.
type metaPlatform struct {
	name                 string
	appID, appSecret     string
	redirectURI          string
	authURL              string // www.instagram.com/oauth/authorize | threads.com/oauth/authorize
	shortTokenURL        string // api.instagram.com/oauth/access_token | graph.threads.com/oauth/access_token
	graphHost            string // https://graph.instagram.com | https://graph.threads.com
	version              string // v27.0 | v1.0
	scopes               string
	exchangeGrant        string // ig_exchange_token | th_exchange_token
	refreshGrant         string // ig_refresh_token | th_refresh_token
	PollEvery, PollLimit time.Duration
}

func NewInstagram(appID, secret, redirect, version string) *metaPlatform {
	if version == "" {
		version = "v27.0"
	}
	return &metaPlatform{name: "instagram", appID: appID, appSecret: secret, redirectURI: redirect,
		authURL: "https://www.instagram.com/oauth/authorize", shortTokenURL: "https://api.instagram.com/oauth/access_token",
		graphHost: "https://graph.instagram.com", version: version,
		scopes: "instagram_business_basic,instagram_business_content_publish", exchangeGrant: "ig_exchange_token", refreshGrant: "ig_refresh_token",
		PollEvery: 20 * time.Second, PollLimit: 15 * time.Minute}
}

func NewThreads(appID, secret, redirect, version string) *metaPlatform {
	if version == "" {
		version = "v1.0"
	}
	return &metaPlatform{name: "threads", appID: appID, appSecret: secret, redirectURI: redirect,
		authURL: "https://threads.com/oauth/authorize", shortTokenURL: "https://graph.threads.com/oauth/access_token",
		graphHost: "https://graph.threads.com", version: version,
		scopes: "threads_basic,threads_content_publish", exchangeGrant: "th_exchange_token", refreshGrant: "th_refresh_token",
		PollEvery: 20 * time.Second, PollLimit: 15 * time.Minute}
}

func (m *metaPlatform) Name() string     { return m.name }
func (m *metaPlatform) Configured() bool { return m.appID != "" && m.appSecret != "" }

func (m *metaPlatform) AuthURL(state string) string {
	q := url.Values{"client_id": {m.appID}, "redirect_uri": {m.redirectURI}, "response_type": {"code"}, "scope": {m.scopes}, "state": {state}}
	return m.authURL + "?" + q.Encode()
}

func (m *metaPlatform) graph(path string) string { return m.graphHost + "/" + m.version + path }

func (m *metaPlatform) Exchange(ctx context.Context, code string) (*Token, *Profile, error) {
	var raw map[string]any
	if err := postForm(ctx, m.shortTokenURL, url.Values{
		"client_id": {m.appID}, "client_secret": {m.appSecret}, "grant_type": {"authorization_code"},
		"redirect_uri": {m.redirectURI}, "code": {code},
	}, nil, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s token: %w", m.name, err)
	}
	// Instagram wraps the result in {"data":[{...}]}; Threads returns it flat.
	if arr, ok := raw["data"].([]any); ok && len(arr) > 0 {
		if first, ok := arr[0].(map[string]any); ok {
			raw = first
		}
	}
	short := anyString(raw["access_token"])
	userID := anyString(raw["user_id"])
	if short == "" {
		return nil, nil, fmt.Errorf("%s token: respons tanpa access_token", m.name)
	}
	// long-lived exchange (server side, needs app secret)
	var ll struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	q := url.Values{"grant_type": {m.exchangeGrant}, "client_secret": {m.appSecret}, "access_token": {short}}
	if err := getJSON(ctx, m.graphHost+"/access_token?"+q.Encode(), nil, &ll); err != nil {
		return nil, nil, fmt.Errorf("%s long-lived token: %w", m.name, err)
	}
	tok := &Token{Access: ll.AccessToken, ExpiresAt: expiresIn(ll.ExpiresIn)}
	var me map[string]any
	fields := "id,username"
	if m.name == "instagram" {
		fields = "user_id,username"
	}
	if err := getJSON(ctx, m.graph("/me?fields="+fields), bearer(tok.Access), &me); err != nil {
		return nil, nil, fmt.Errorf("%s profile: %w", m.name, err)
	}
	if id := anyString(me["user_id"]); id != "" {
		userID = id
	} else if id := anyString(me["id"]); id != "" && userID == "" {
		userID = id
	}
	if userID == "" {
		return nil, nil, fmt.Errorf("%s: user id tidak ditemukan", m.name)
	}
	name := anyString(me["username"])
	if name == "" {
		name = userID
	}
	return tok, &Profile{ExternalID: userID, DisplayName: "@" + name}, nil
}

// Refresh extends the 60-day token. Allowed only when the token is ≥24 h old.
func (m *metaPlatform) Refresh(ctx context.Context, t *Token, refreshedAt time.Time, force bool) (*Token, error) {
	if time.Since(refreshedAt) < 24*time.Hour {
		return nil, nil
	}
	if !force && t.ExpiresAt != nil && time.Until(*t.ExpiresAt) > 15*24*time.Hour {
		return nil, nil
	}
	var ll struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	q := url.Values{"grant_type": {m.refreshGrant}, "access_token": {t.Access}}
	if err := getJSON(ctx, m.graphHost+"/refresh_access_token?"+q.Encode(), nil, &ll); err != nil {
		return nil, fmt.Errorf("%s refresh: %w", m.name, err)
	}
	return &Token{Access: ll.AccessToken, ExpiresAt: expiresIn(ll.ExpiresIn)}, nil
}

func (m *metaPlatform) Publish(ctx context.Context, t *Token, r PublishRequest) (*PublishResult, error) {
	if r.PublicURL == "" {
		return nil, errors.New("PUBLIC_BASE_URL belum diset: Instagram/Threads perlu URL video publik")
	}
	h := bearer(t.Access)
	form := url.Values{"video_url": {r.PublicURL}}
	var containerPath, publishPath, statusFields string
	if m.name == "instagram" {
		if r.DurationSec < 3 || r.DurationSec > 900 {
			return nil, fmt.Errorf("Instagram Reels: durasi harus 3 dtk – 15 mnt (sekarang %.0f dtk)", r.DurationSec)
		}
		form.Set("media_type", "REELS")
		form.Set("caption", truncateRunes(r.Caption, 2200))
		form.Set("share_to_feed", "true")
		if r.AIGenerated {
			form.Set("is_ai_generated", "true")
		}
		containerPath, publishPath, statusFields = "/"+r.ExternalUserID+"/media", "/"+r.ExternalUserID+"/media_publish", "status_code"
	} else {
		if r.DurationSec > 300 {
			return nil, fmt.Errorf("Threads: durasi video maksimal 5 menit (sekarang %.0f dtk)", r.DurationSec)
		}
		form.Set("media_type", "VIDEO")
		form.Set("text", truncateRunes(r.Caption, 500))
		form.Set("is_made_with_ai", strconv.FormatBool(r.AIGenerated))
		containerPath, publishPath, statusFields = "/"+r.ExternalUserID+"/threads", "/"+r.ExternalUserID+"/threads_publish", "status,error_message"
	}

	var c struct {
		ID string `json:"id"`
	}
	if err := postForm(ctx, m.graph(containerPath), form, h, &c); err != nil {
		return nil, retryable(fmt.Errorf("%s container: %w", m.name, err))
	}
	if c.ID == "" {
		return nil, retryable(fmt.Errorf("%s container: id kosong", m.name))
	}

	// Poll until the platform has downloaded & processed the video.
	deadline := time.Now().Add(m.PollLimit)
	ready := false
	for !ready {
		if time.Now().After(deadline) {
			return nil, retryable(fmt.Errorf("%s: pemrosesan video melebihi %s", m.name, m.PollLimit))
		}
		if err := sleep(ctx, m.PollEvery); err != nil {
			return nil, err
		}
		var st map[string]any
		if err := getJSON(ctx, m.graph("/"+c.ID+"?fields="+statusFields), h, &st); err != nil {
			continue
		}
		status := anyString(st["status_code"])
		if status == "" {
			status = anyString(st["status"])
		}
		switch status {
		case "FINISHED":
			ready = true
		case "ERROR", "EXPIRED":
			msg := anyString(st["error_message"])
			// Not published yet → safe to retry with a fresh container.
			return nil, retryable(fmt.Errorf("%s memproses video gagal: %s %s", m.name, status, msg))
		case "PUBLISHED":
			ready = true
		}
	}

	var pub struct {
		ID string `json:"id"`
	}
	if err := postForm(ctx, m.graph(publishPath), url.Values{"creation_id": {c.ID}}, h, &pub); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status >= 500 {
			// Outcome unknown — don't auto-retry (could duplicate).
			return nil, fmt.Errorf("%s publish (status tidak pasti, cek akun): %w", m.name, err)
		}
		return nil, retryable(fmt.Errorf("%s publish: %w", m.name, err))
	}
	res := &PublishResult{ExternalID: pub.ID}
	var pl struct {
		Permalink string `json:"permalink"`
	}
	if err := getJSON(ctx, m.graph("/"+pub.ID+"?fields=permalink"), h, &pl); err == nil {
		res.URL = pl.Permalink
	}
	return res, nil
}
