-- SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
-- Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS image_style TEXT NOT NULL DEFAULT 'cinematic';
