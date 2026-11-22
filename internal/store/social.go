// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type SocialAccount struct {
	ID               string     `json:"id"`
	Platform         string     `json:"platform"`
	ExternalID       string     `json:"external_id"`
	DisplayName      string     `json:"display_name"`
	AccessTokenEnc   string     `json:"-"`
	RefreshTokenEnc  string     `json:"-"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RefreshExpiresAt *time.Time `json:"-"`
	TokenRefreshedAt time.Time  `json:"token_refreshed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type Post struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	AssetID     string     `json:"asset_id"`
	AccountID   *string    `json:"account_id"`
	Platform    string     `json:"platform"`
	Title       string     `json:"title"`
	Caption     string     `json:"caption"`
	Privacy     string     `json:"privacy"`
	AIGenerated bool       `json:"ai_generated"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	ApprovedAt  *time.Time `json:"approved_at"`
	PublishedAt *time.Time `json:"published_at"`
	ExternalID  string     `json:"external_id"`
	ExternalURL string     `json:"external_url"`
	Note        string     `json:"note"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by"`
	ApprovedBy  string     `json:"approved_by"`
	AccountName string     `json:"account_name"`
}

// ---------- OAuth state ----------

func (s *Store) SaveOAuthState(ctx context.Context, state, platform string) error {
	s.DB.Exec(ctx, `DELETE FROM oauth_states WHERE created_at < now() - interval '1 hour'`)
	_, err := s.DB.Exec(ctx, `INSERT INTO oauth_states(state, platform) VALUES($1,$2)`, state, platform)
	return err
}

// ConsumeOAuthState deletes and validates a state (single use, 15 min lifetime).
func (s *Store) ConsumeOAuthState(ctx context.Context, state, platform string) bool {
	var p string
	err := s.DB.QueryRow(ctx, `DELETE FROM oauth_states WHERE state=$1 AND created_at > now() - interval '15 minutes' RETURNING platform`, state).Scan(&p)
	return err == nil && p == platform
}

// ---------- accounts ----------

const accountCols = `id, platform, external_id, display_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, token_refreshed_at, created_at`

func scanAccount(row pgx.Row) (*SocialAccount, error) {
	var a SocialAccount
	err := row.Scan(&a.ID, &a.Platform, &a.ExternalID, &a.DisplayName, &a.AccessTokenEnc, &a.RefreshTokenEnc, &a.ExpiresAt, &a.RefreshExpiresAt, &a.TokenRefreshedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) UpsertAccount(ctx context.Context, a *SocialAccount) error {
	return s.DB.QueryRow(ctx, `INSERT INTO social_accounts(platform, external_id, display_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, token_refreshed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (platform, external_id) DO UPDATE SET display_name=EXCLUDED.display_name, access_token_enc=EXCLUDED.access_token_enc,
			refresh_token_enc=CASE WHEN EXCLUDED.refresh_token_enc <> '' THEN EXCLUDED.refresh_token_enc ELSE social_accounts.refresh_token_enc END,
			expires_at=EXCLUDED.expires_at, refresh_expires_at=EXCLUDED.refresh_expires_at, token_refreshed_at=now(), updated_at=now()
		RETURNING id, created_at`,
		a.Platform, a.ExternalID, a.DisplayName, a.AccessTokenEnc, a.RefreshTokenEnc, a.ExpiresAt, a.RefreshExpiresAt).Scan(&a.ID, &a.CreatedAt)
}

func (s *Store) UpdateAccountTokens(ctx context.Context, a *SocialAccount) error {
	_, err := s.DB.Exec(ctx, `UPDATE social_accounts SET access_token_enc=$2,
		refresh_token_enc=CASE WHEN $3 <> '' THEN $3 ELSE refresh_token_enc END,
		expires_at=$4, refresh_expires_at=COALESCE($5, refresh_expires_at), token_refreshed_at=now(), updated_at=now() WHERE id=$1`,
		a.ID, a.AccessTokenEnc, a.RefreshTokenEnc, a.ExpiresAt, a.RefreshExpiresAt)
	return err
}

func (s *Store) ListAccounts(ctx context.Context) ([]SocialAccount, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+accountCols+` FROM social_accounts ORDER BY platform, display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialAccount{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) GetAccount(ctx context.Context, id string) (*SocialAccount, error) {
	return scanAccount(s.DB.QueryRow(ctx, `SELECT `+accountCols+` FROM social_accounts WHERE id=$1`, id))
}

// DeleteAccount removes stored tokens; pending posts for it are cancelled.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	s.DB.Exec(ctx, `UPDATE posts SET status='cancelled', error='akun diputus', updated_at=now() WHERE account_id=$1 AND status IN ('pending_approval','scheduled')`, id)
	ct, err := s.DB.Exec(ctx, `DELETE FROM social_accounts WHERE id=$1`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------- posts ----------

const postCols = `p.id, p.project_id, p.asset_id, p.account_id, p.platform, p.title, p.caption, p.privacy, p.ai_generated, p.scheduled_at, p.status,
	p.attempts, p.approved_at, p.published_at, p.external_id, p.external_url, p.note, p.error, p.created_at, p.created_by, p.approved_by, COALESCE(a.display_name, '')`

func scanPost(row pgx.Row) (*Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.ProjectID, &p.AssetID, &p.AccountID, &p.Platform, &p.Title, &p.Caption, &p.Privacy, &p.AIGenerated, &p.ScheduledAt, &p.Status,
		&p.Attempts, &p.ApprovedAt, &p.PublishedAt, &p.ExternalID, &p.ExternalURL, &p.Note, &p.Error, &p.CreatedAt, &p.CreatedBy, &p.ApprovedBy, &p.AccountName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) CreatePost(ctx context.Context, p *Post) error {
	return s.DB.QueryRow(ctx, `INSERT INTO posts(project_id, asset_id, account_id, platform, title, caption, privacy, ai_generated, scheduled_at, status, approved_at, created_by, approved_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, CASE WHEN $10='scheduled' THEN now() END, $11, $12) RETURNING id, created_at`,
		p.ProjectID, p.AssetID, p.AccountID, p.Platform, p.Title, p.Caption, p.Privacy, p.AIGenerated, p.ScheduledAt, p.Status, p.CreatedBy, p.ApprovedBy).Scan(&p.ID, &p.CreatedAt)
}

func (s *Store) ListPosts(ctx context.Context, projectID string) ([]Post, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+postCols+` FROM posts p LEFT JOIN social_accounts a ON a.id=p.account_id WHERE p.project_id=$1 ORDER BY p.created_at DESC LIMIT 200`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) GetPost(ctx context.Context, id string) (*Post, error) {
	return scanPost(s.DB.QueryRow(ctx, `SELECT `+postCols+` FROM posts p LEFT JOIN social_accounts a ON a.id=p.account_id WHERE p.id=$1`, id))
}

func (s *Store) ApprovePost(ctx context.Context, id, approver string) error {
	ct, err := s.DB.Exec(ctx, `UPDATE posts SET status='scheduled', approved_at=now(), approved_by=$2, updated_at=now() WHERE id=$1 AND status='pending_approval'`, id, approver)
	if err == nil && ct.RowsAffected() == 0 {
		return errors.New("post tidak menunggu persetujuan")
	}
	return err
}

func (s *Store) CancelPost(ctx context.Context, id string) error {
	ct, err := s.DB.Exec(ctx, `UPDATE posts SET status='cancelled', updated_at=now() WHERE id=$1 AND status IN ('pending_approval','scheduled')`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return errors.New("post tidak bisa dibatalkan (sudah diproses)")
	}
	return err
}

// ClaimDuePosts atomically marks up to n due posts as publishing (safe with several instances).
func (s *Store) ClaimDuePosts(ctx context.Context, n int) ([]Post, error) {
	rows, err := s.DB.Query(ctx, `UPDATE posts SET status='publishing', attempts=attempts+1, updated_at=now()
		WHERE id IN (SELECT id FROM posts WHERE status='scheduled' AND scheduled_at <= now()
			AND (next_attempt_at IS NULL OR next_attempt_at <= now()) ORDER BY scheduled_at FOR UPDATE SKIP LOCKED LIMIT $1)
		RETURNING id`, n)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]Post, 0, len(ids))
	for _, id := range ids {
		if p, err := s.GetPost(ctx, id); err == nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *Store) MarkPublished(ctx context.Context, id, extID, extURL, note string) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status='published', published_at=now(), external_id=$2, external_url=$3, note=$4, error='', updated_at=now() WHERE id=$1`, id, extID, extURL, note)
	return err
}

func (s *Store) MarkRetry(ctx context.Context, id, msg string, next time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status='scheduled', error=$2, next_attempt_at=$3, updated_at=now() WHERE id=$1`, id, msg, next)
	return err
}

func (s *Store) MarkFailed(ctx context.Context, id, msg string) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status='failed', error=$2, updated_at=now() WHERE id=$1`, id, msg)
	return err
}

// FailInterrupted: posts left "publishing" by a crash are NOT retried automatically
// (the platform may already have published them) — they are flagged for manual check.
func (s *Store) FailInterrupted(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status='failed', error='Terputus saat publikasi (server restart). Cek akun sebelum mencoba lagi.', updated_at=now() WHERE status='publishing'`)
	return err
}
