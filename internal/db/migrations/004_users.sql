-- SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
-- Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

-- Individual user accounts + server-side sessions (replaces the shared APP_ACCESS_TOKEN).
-- ISO/IEC 27001:2022 A.5.15 access control, A.5.16 identity, A.5.17 authentication info, A.8.5 secure auth.

CREATE TABLE IF NOT EXISTS users (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username             TEXT NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,49}$'),
    display_name         TEXT NOT NULL DEFAULT '',
    role                 TEXT NOT NULL CHECK (role IN ('admin','user')),
    password_hash        TEXT NOT NULL,                 -- Argon2id, never plaintext
    active               BOOLEAN NOT NULL DEFAULT true,
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    failed_attempts      INT NOT NULL DEFAULT 0,
    locked_until         TIMESTAMPTZ,
    last_login_at        TIMESTAMPTZ,
    password_changed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by           TEXT NOT NULL DEFAULT '',      -- username of the admin who created the account
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The cookie holds a random token; only its SHA-256 hash is stored.
CREATE TABLE IF NOT EXISTS sessions (
    token_hash   BYTEA PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    remote_addr  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);

-- Ownership / accountability (usernames are immutable, so they are stored directly).
ALTER TABLE projects ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE posts    ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE posts    ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS audit_log_actor_idx ON audit_log(actor, at);
