// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package web embeds the single-page frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(static, "static")
	return sub
}
