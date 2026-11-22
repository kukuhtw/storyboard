// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package api

import (
	"bytes"
	"io"
)

func readFull(r io.Reader, buf []byte) (int, error) {
	n, err := io.ReadFull(r, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil
	}
	return n, err
}

func multiReader(head []byte, rest io.Reader) io.Reader {
	return io.MultiReader(bytes.NewReader(head), rest)
}

// isQuickTime detects MOV files ("ftypqt" brand) which DetectContentType doesn't know.
func isQuickTime(b []byte) bool {
	return len(b) >= 12 && string(b[4:8]) == "ftyp" && string(b[8:10]) == "qt"
}
