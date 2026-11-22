-- SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
-- Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

-- Storyboard Studio schema (PostgreSQL 13+; gen_random_uuid is built in)

CREATE TABLE IF NOT EXISTS projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title               TEXT NOT NULL,
    video_type          TEXT NOT NULL CHECK (video_type IN ('documentation','advertisement','drama')),
    brief               TEXT NOT NULL DEFAULT '',
    target_duration_sec INT  NOT NULL DEFAULT 60 CHECK (target_duration_sec BETWEEN 5 AND 1800),
    aspect_ratio        TEXT NOT NULL DEFAULT '16:9',
    language            TEXT NOT NULL DEFAULT 'id',
    tone                TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per pipeline stage per project. Status machine:
-- locked -> ready -> generating -> review -> approved   (reject => ready with feedback)
CREATE TABLE IF NOT EXISTS stages (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'locked',
    output      JSONB NOT NULL DEFAULT '{}'::jsonb,
    feedback    TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    version     INT  NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, kind)
);

CREATE TABLE IF NOT EXISTS assets (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    scene_id      UUID,
    kind          TEXT NOT NULL CHECK (kind IN ('ref_image','ref_video','image','video','audio')),
    storage_name  TEXT NOT NULL,          -- random server-side filename, never user-controlled
    original_name TEXT NOT NULL DEFAULT '',
    mime          TEXT NOT NULL,
    size_bytes    BIGINT NOT NULL,
    source        TEXT NOT NULL CHECK (source IN ('upload','generated')),
    note          TEXT NOT NULL DEFAULT '',  -- user description of the reference
    consent       BOOLEAN NOT NULL DEFAULT false, -- uploader confirmed rights/consent (people in media)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS assets_project_idx ON assets(project_id);

CREATE TABLE IF NOT EXISTS scenes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    idx             INT  NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    shot            TEXT NOT NULL DEFAULT '',
    duration_sec    NUMERIC(6,2) NOT NULL DEFAULT 5,
    voiceover       TEXT NOT NULL DEFAULT '',
    image_prompt    TEXT NOT NULL DEFAULT '',
    video_prompt    TEXT NOT NULL DEFAULT '',
    audio_asset_id  UUID REFERENCES assets(id) ON DELETE SET NULL,
    image_asset_id  UUID REFERENCES assets(id) ON DELETE SET NULL,
    video_asset_id  UUID REFERENCES assets(id) ON DELETE SET NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scenes_project_idx ON scenes(project_id, idx);

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_scene_fk;
ALTER TABLE assets ADD CONSTRAINT assets_scene_fk FOREIGN KEY (scene_id) REFERENCES scenes(id) ON DELETE CASCADE;

-- ISO/IEC 27001 A.8.15 logging: who did what, when. No secrets / file contents stored here.
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor       TEXT NOT NULL,
    remote_addr TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    entity      TEXT NOT NULL,
    entity_id   TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS audit_log_at_idx ON audit_log(at);
