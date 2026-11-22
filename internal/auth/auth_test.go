// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package auth

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected format %s", h)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(h, "correct horse batterY") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=0,t=2,p=1$aa$bb", "$bcrypt$x$y$z$w"} {
		if VerifyPassword(bad, "x") {
			t.Fatalf("malformed hash accepted: %q", bad)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	cases := map[string]bool{
		"short":                false,
		"password1234":         false,
		"aaaaaaaaaaaaaa":       false,
		"budi-rahasia-2026!":   false, // contains username
		"Langit Biru Pagi 7":   true,
		"kopi-susu-gula-aren1": true,
	}
	for pw, ok := range cases {
		err := ValidatePassword(pw, "budi")
		if (err == nil) != ok {
			t.Errorf("%q: got err=%v, want ok=%v", pw, err, ok)
		}
	}
}

func TestTempPasswordAndToken(t *testing.T) {
	p := TempPassword()
	if len(p) != 19 || strings.Count(p, "-") != 3 {
		t.Fatalf("temp password format: %q", p)
	}
	if err := ValidatePassword(p, "admin"); err != nil {
		t.Fatalf("temp password fails policy: %v", err)
	}
	tok, h := NewSessionToken()
	if len(tok) < 40 || string(HashToken(tok)) != string(h) {
		t.Fatal("token/hash mismatch")
	}
	tok2, _ := NewSessionToken()
	if tok == tok2 {
		t.Fatal("token not random")
	}
}

func TestNormalizeUsername(t *testing.T) {
	if u, err := NormalizeUsername("  Budi.Santoso "); err != nil || u != "budi.santoso" {
		t.Fatalf("got %q %v", u, err)
	}
	for _, bad := range []string{"ab", "-budi", "budi santoso", "budi@x", strings.Repeat("a", 51)} {
		if _, err := NormalizeUsername(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
