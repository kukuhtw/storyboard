// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package media

import (
	"strings"
	"testing"
)

func TestSaveLimitsAndPaths(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name, n, err := s.Save(strings.NewReader("hello"), "image/png", 10)
	if err != nil || n != 5 || !strings.HasSuffix(name, ".png") {
		t.Fatalf("save: %v %d %s", err, n, name)
	}
	if _, _, err := s.Save(strings.NewReader("too long content"), "image/png", 4); err == nil {
		t.Fatal("expected size limit error")
	}
	if _, _, err := s.Save(strings.NewReader("x"), "text/html", 10); err == nil {
		t.Fatal("expected unsupported type")
	}
	for _, bad := range []string{"../etc/passwd", "a.png", "..\\x.png"} {
		if _, err := s.Path(bad); err == nil {
			t.Fatalf("path %q accepted", bad)
		}
	}
}
