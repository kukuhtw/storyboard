// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package media stores binary files on local disk under DATA_DIR/media with
// random, server-generated names (no user-controlled paths → no traversal).
package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type Storage struct{ Dir string }

var nameRe = regexp.MustCompile(`^[a-f0-9]{32}\.[a-z0-9]{2,5}$`)

func New(dataDir string) (*Storage, error) {
	dir := filepath.Join(dataDir, "media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Storage{Dir: dir}, nil
}

var extByMime = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif", "image/svg+xml": "svg",
	"video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov",
	"audio/wav": "wav", "audio/x-wav": "wav", "audio/mpeg": "mp3", "audio/ogg": "ogg",
	"application/x-subrip": "srt",
}

func ExtFor(mime string) (string, bool) { e, ok := extByMime[mime]; return e, ok }

func randomName(ext string) string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b) + "." + ext
}

// Save copies at most limit bytes from r into a new file. Returns name and size.
func (s *Storage) Save(r io.Reader, mime string, limit int64) (string, int64, error) {
	ext, ok := ExtFor(mime)
	if !ok {
		return "", 0, fmt.Errorf("unsupported media type %q", mime)
	}
	name := randomName(ext)
	f, err := os.OpenFile(filepath.Join(s.Dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("file too large")
	}
	if err != nil {
		os.Remove(filepath.Join(s.Dir, name))
		return "", 0, err
	}
	return name, n, nil
}

func (s *Storage) Path(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", errors.New("invalid storage name")
	}
	return filepath.Join(s.Dir, name), nil
}

func (s *Storage) Read(name string) ([]byte, error) {
	p, err := s.Path(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (s *Storage) Remove(names ...string) {
	for _, n := range names {
		if p, err := s.Path(n); err == nil {
			os.Remove(p)
		}
	}
}
