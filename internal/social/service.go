// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package social

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"storyboard/internal/media"
	"storyboard/internal/secure"
	"storyboard/internal/store"
)

type Service struct {
	Store         *store.Store
	Box           *secure.Box // nil → publishing disabled (TOKEN_ENC_KEY missing)
	Media         *media.Storage
	Platforms     map[string]Platform
	PublicBaseURL string
	Probe         func(ctx context.Context, path string) (float64, error)

	wg sync.WaitGroup
}

func (s *Service) Platform(name string) (Platform, error) {
	p, ok := s.Platforms[name]
	if !ok {
		return nil, fmt.Errorf("platform %q tidak dikenal", name)
	}
	if !p.Configured() {
		return nil, fmt.Errorf("platform %s belum dikonfigurasi (client id/secret)", name)
	}
	if s.Box == nil {
		return nil, errors.New("TOKEN_ENC_KEY belum diset; publikasi dinonaktifkan")
	}
	return p, nil
}

// Status lists each platform and whether it is configured.
func (s *Service) Status() map[string]bool {
	out := map[string]bool{}
	for k, p := range s.Platforms {
		out[k] = p.Configured() && s.Box != nil
	}
	return out
}

// ---------- OAuth connect ----------

func (s *Service) StartConnect(ctx context.Context, platform string) (string, error) {
	p, err := s.Platform(platform)
	if err != nil {
		return "", err
	}
	b := make([]byte, 24)
	rand.Read(b)
	state := hex.EncodeToString(b)
	if err := s.Store.SaveOAuthState(ctx, state, platform); err != nil {
		return "", err
	}
	return p.AuthURL(state), nil
}

func (s *Service) FinishConnect(ctx context.Context, platform, state, code string) (*store.SocialAccount, error) {
	p, err := s.Platform(platform)
	if err != nil {
		return nil, err
	}
	if !s.Store.ConsumeOAuthState(ctx, state, platform) {
		return nil, errors.New("state OAuth tidak valid atau kedaluwarsa, ulangi koneksi")
	}
	tok, prof, err := p.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	acc := &store.SocialAccount{Platform: platform, ExternalID: prof.ExternalID, DisplayName: prof.DisplayName,
		ExpiresAt: tok.ExpiresAt, RefreshExpiresAt: tok.RefreshExpiresAt}
	if acc.AccessTokenEnc, err = s.Box.Encrypt(tok.Access); err != nil {
		return nil, err
	}
	if acc.RefreshTokenEnc, err = s.Box.Encrypt(tok.Refresh); err != nil {
		return nil, err
	}
	if err := s.Store.UpsertAccount(ctx, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

// token decrypts an account's token and refreshes it when due.
func (s *Service) token(ctx context.Context, acc *store.SocialAccount, p Platform, force bool) (*Token, error) {
	access, err := s.Box.Decrypt(acc.AccessTokenEnc)
	if err != nil {
		return nil, err
	}
	refresh, err := s.Box.Decrypt(acc.RefreshTokenEnc)
	if err != nil {
		return nil, err
	}
	tok := &Token{Access: access, Refresh: refresh, ExpiresAt: acc.ExpiresAt, RefreshExpiresAt: acc.RefreshExpiresAt}
	nt, err := p.Refresh(ctx, tok, acc.TokenRefreshedAt, force)
	if err != nil {
		if tok.ExpiresAt != nil && time.Until(*tok.ExpiresAt) > time.Minute {
			slog.Warn("token refresh failed, using current token", "platform", acc.Platform, "account", acc.ID)
			return tok, nil
		}
		return nil, err
	}
	if nt == nil {
		return tok, nil
	}
	upd := *acc
	upd.ExpiresAt, upd.RefreshExpiresAt = nt.ExpiresAt, nt.RefreshExpiresAt
	if upd.AccessTokenEnc, err = s.Box.Encrypt(nt.Access); err != nil {
		return nil, err
	}
	if upd.RefreshTokenEnc, err = s.Box.Encrypt(nt.Refresh); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateAccountTokens(ctx, &upd); err != nil {
		return nil, err
	}
	return nt, nil
}

// PublicURL returns a signed, expiring URL for a final video (Instagram/Threads fetch it).
func (s *Service) PublicURL(assetID string, ttl time.Duration) string {
	if s.PublicBaseURL == "" || s.Box == nil {
		return ""
	}
	exp, sig := s.Box.SignAsset(assetID, ttl)
	return s.PublicBaseURL + "/public/media/" + url.PathEscape(assetID) + "?exp=" + exp + "&sig=" + sig
}

// ---------- scheduler ----------

const maxAttempts = 3

// Run processes due posts until ctx is cancelled. Safe to run on several instances
// (posts are claimed with FOR UPDATE SKIP LOCKED).
func (s *Service) Run(ctx context.Context) {
	if s.Box == nil {
		slog.Info("social publishing disabled (TOKEN_ENC_KEY not set)")
		return
	}
	if err := s.Store.FailInterrupted(ctx); err != nil {
		slog.Warn("fail interrupted posts", "err", err)
	}
	tick := time.NewTicker(15 * time.Second)
	maint := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	defer maint.Stop()
	s.maintain(ctx)
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-maint.C:
			s.maintain(ctx)
		case <-tick.C:
			posts, err := s.Store.ClaimDuePosts(ctx, 3)
			if err != nil {
				slog.Warn("claim posts", "err", err)
				continue
			}
			for _, p := range posts {
				p := p
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.publish(context.WithoutCancel(ctx), &p)
				}()
			}
		}
	}
}

// maintain keeps long-lived tokens alive (Meta 60-day tokens, TikTok rotating refresh tokens).
func (s *Service) maintain(ctx context.Context) {
	accs, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return
	}
	for i := range accs {
		p, ok := s.Platforms[accs[i].Platform]
		if !ok || !p.Configured() {
			continue
		}
		if _, err := s.token(ctx, &accs[i], p, false); err != nil {
			slog.Warn("token maintenance failed", "platform", accs[i].Platform, "account", accs[i].ID, "err", err)
		}
	}
}

func (s *Service) publish(ctx context.Context, post *store.Post) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	err := s.publishOnce(ctx, post)
	if err == nil {
		return
	}
	msg := err.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	slog.Warn("publish failed", "post", post.ID, "platform", post.Platform, "attempt", post.Attempts, "err", msg)
	if IsRetryable(err) && post.Attempts < maxAttempts {
		backoff := time.Duration(post.Attempts*post.Attempts) * 2 * time.Minute
		s.Store.MarkRetry(ctx, post.ID, msg, time.Now().Add(backoff))
		return
	}
	s.Store.MarkFailed(ctx, post.ID, msg)
	s.Store.Audit(ctx, "scheduler", "", "publish_failed", "post", post.ID, map[string]any{"platform": post.Platform})
}

func (s *Service) publishOnce(ctx context.Context, post *store.Post) error {
	if post.AccountID == nil {
		return errors.New("akun tujuan sudah diputus")
	}
	p, err := s.Platform(post.Platform)
	if err != nil {
		return err
	}
	acc, err := s.Store.GetAccount(ctx, *post.AccountID)
	if err != nil {
		return err
	}
	asset, err := s.Store.GetAsset(ctx, post.AssetID)
	if err != nil {
		return err
	}
	path, err := s.Media.Path(asset.StorageName)
	if err != nil {
		return err
	}
	dur := 0.0
	if s.Probe != nil {
		if d, err := s.Probe(ctx, path); err == nil {
			dur = d
		}
	}
	tok, err := s.token(ctx, acc, p, false)
	if err != nil {
		return retryable(err)
	}
	res, err := p.Publish(ctx, tok, PublishRequest{
		ExternalUserID: acc.ExternalID, VideoPath: path, VideoSize: asset.SizeBytes, DurationSec: dur,
		PublicURL: s.PublicURL(asset.ID, 2*time.Hour), Title: post.Title, Caption: post.Caption,
		Privacy: post.Privacy, AIGenerated: post.AIGenerated,
	})
	if err != nil {
		return err
	}
	if err := s.Store.MarkPublished(ctx, post.ID, res.ExternalID, res.URL, res.Note); err != nil {
		return err
	}
	s.Store.Audit(ctx, "scheduler", "", "published", "post", post.ID, map[string]any{"platform": post.Platform, "external_id": res.ExternalID})
	return nil
}
