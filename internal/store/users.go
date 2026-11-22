// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type User struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name"`
	Role               string     `json:"role"`
	PasswordHash       string     `json:"-"`
	Active             bool       `json:"active"`
	MustChangePassword bool       `json:"must_change_password"`
	FailedAttempts     int        `json:"failed_attempts"`
	LockedUntil        *time.Time `json:"locked_until"`
	LastLoginAt        *time.Time `json:"last_login_at"`
	PasswordChangedAt  time.Time  `json:"password_changed_at"`
	CreatedBy          string     `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
}

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var ErrUsernameTaken = errors.New("username sudah dipakai")

const userCols = `id, username, display_name, role, password_hash, active, must_change_password, failed_attempts,
	locked_until, last_login_at, password_changed_at, created_by, created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Active, &u.MustChangePassword,
		&u.FailedAttempts, &u.LockedUntil, &u.LastLoginAt, &u.PasswordChangedAt, &u.CreatedBy, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	err := s.DB.QueryRow(ctx, `INSERT INTO users(username, display_name, role, password_hash, must_change_password, created_by)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id, active, password_changed_at, created_at`,
		u.Username, u.DisplayName, u.Role, u.PasswordHash, u.MustChangePassword, u.CreatedBy).
		Scan(&u.ID, &u.Active, &u.PasswordChangedAt, &u.CreatedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrUsernameTaken
	}
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username=$1`, username))
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY active DESC, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUser changes display name, role and active flag.
func (s *Store) UpdateUser(ctx context.Context, id, displayName, role string, active bool) error {
	ct, err := s.DB.Exec(ctx, `UPDATE users SET display_name=$2, role=$3, active=$4, updated_at=now() WHERE id=$1`, id, displayName, role, active)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CountOtherActiveAdmins counts active admins other than id (guards against locking everyone out).
func (s *Store) CountOtherActiveAdmins(ctx context.Context, id string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND active AND id<>$1`, id).Scan(&n)
	return n, err
}

// SetPassword stores a new hash, resets lockout and revokes all sessions of the user.
func (s *Store) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2, must_change_password=$3, failed_attempts=0, locked_until=NULL,
		password_changed_at=now(), updated_at=now() WHERE id=$1`, id, hash, mustChange)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordLoginFailure increments the counter and locks the account after `threshold` consecutive failures.
func (s *Store) RecordLoginFailure(ctx context.Context, id string, threshold int, lockFor time.Duration) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET failed_attempts = failed_attempts + 1,
		locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN now() + make_interval(secs => $3) ELSE locked_until END
		WHERE id=$1`, id, threshold, lockFor.Seconds())
	return err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET failed_attempts=0, locked_until=NULL, last_login_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) UnlockUser(ctx context.Context, id string) error {
	ct, err := s.DB.Exec(ctx, `UPDATE users SET failed_attempts=0, locked_until=NULL, updated_at=now() WHERE id=$1`, id)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------- sessions ----------

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID, remote string, ttl time.Duration) error {
	s.DB.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions(token_hash, user_id, expires_at, remote_addr) VALUES($1,$2, now() + make_interval(secs => $3), $4)`,
		tokenHash, userID, ttl.Seconds(), remote)
	return err
}

// SessionUser returns the active user owning a valid session. Sessions past their
// absolute expiry or idle timeout are deleted. last_seen_at is refreshed at most once a minute.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte, idle time.Duration) (*User, error) {
	var lastSeen, expires time.Time
	var u User
	err := s.DB.QueryRow(ctx, `SELECT s.last_seen_at, s.expires_at, `+prefixed("u", userCols)+`
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash=$1`, tokenHash).
		Scan(&lastSeen, &expires, &u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Active, &u.MustChangePassword,
			&u.FailedAttempts, &u.LockedUntil, &u.LastLoginAt, &u.PasswordChangedAt, &u.CreatedBy, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if !u.Active || now.After(expires) || now.Sub(lastSeen) > idle {
		s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
		return nil, ErrNotFound
	}
	if now.Sub(lastSeen) > time.Minute {
		s.DB.Exec(ctx, `UPDATE sessions SET last_seen_at=now() WHERE token_hash=$1`, tokenHash)
	}
	return &u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

// DeleteUserSessions revokes every session of a user except `keep` (may be nil).
func (s *Store) DeleteUserSessions(ctx context.Context, userID string, keep []byte) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND ($2::bytea IS NULL OR token_hash <> $2)`, userID, keep)
	return err
}

func (s *Store) CountUserSessions(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT user_id, count(*) FROM sessions WHERE expires_at > now() GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---------- audit log ----------

type AuditEntry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Actor      string          `json:"actor"`
	RemoteAddr string          `json:"remote_addr"`
	Action     string          `json:"action"`
	Entity     string          `json:"entity"`
	EntityID   string          `json:"entity_id"`
	Detail     json.RawMessage `json:"detail"`
}

// ListAudit returns the newest entries, optionally filtered by actor and/or action.
func (s *Store) ListAudit(ctx context.Context, actor, action string, limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, at, actor, remote_addr, action, entity, entity_id, detail FROM audit_log
		WHERE ($1 = '' OR actor = $1) AND ($2 = '' OR action = $2) ORDER BY id DESC LIMIT $3`, actor, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.RemoteAddr, &e.Action, &e.Entity, &e.EntityID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
