-- SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
-- Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

-- Final render assets
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_kind_check;
ALTER TABLE assets ADD CONSTRAINT assets_kind_check
    CHECK (kind IN ('ref_image','ref_video','image','video','audio','final_video','subtitle'));

-- New pipeline stage "render" for existing projects
INSERT INTO stages(project_id, kind, status)
SELECT p.id, 'render',
       CASE WHEN EXISTS (SELECT 1 FROM stages s WHERE s.project_id = p.id AND s.kind = 'videos' AND s.status = 'approved')
            THEN 'ready' ELSE 'locked' END
FROM projects p
ON CONFLICT (project_id, kind) DO NOTHING;

-- Connected social accounts. Tokens are AES-256-GCM encrypted by the application (TOKEN_ENC_KEY).
CREATE TABLE IF NOT EXISTS social_accounts (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform           TEXT NOT NULL CHECK (platform IN ('youtube','instagram','threads','tiktok')),
    external_id        TEXT NOT NULL,
    display_name       TEXT NOT NULL DEFAULT '',
    access_token_enc   TEXT NOT NULL,
    refresh_token_enc  TEXT NOT NULL DEFAULT '',
    expires_at         TIMESTAMPTZ,
    refresh_expires_at TIMESTAMPTZ,
    token_refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (platform, external_id)
);

-- One-time OAuth state values (CSRF protection for the connect flow)
CREATE TABLE IF NOT EXISTS oauth_states (
    state      TEXT PRIMARY KEY,
    platform   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Publishing queue. status: pending_approval -> scheduled -> publishing -> published | failed ; cancelled
CREATE TABLE IF NOT EXISTS posts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    asset_id        UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    account_id      UUID REFERENCES social_accounts(id) ON DELETE SET NULL,
    platform        TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    caption         TEXT NOT NULL DEFAULT '',
    privacy         TEXT NOT NULL DEFAULT 'public',
    ai_generated    BOOLEAN NOT NULL DEFAULT true,
    scheduled_at    TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL,
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    approved_at     TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    external_id     TEXT NOT NULL DEFAULT '',
    external_url    TEXT NOT NULL DEFAULT '',
    note            TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS posts_due_idx ON posts(status, scheduled_at);
CREATE INDEX IF NOT EXISTS posts_project_idx ON posts(project_id, created_at DESC);
