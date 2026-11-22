// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package secure

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestBox(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b.Encrypt("secret-token")
	if strings.Contains(enc, "secret") {
		t.Fatal("plaintext leaked")
	}
	if dec, err := b.Decrypt(enc); err != nil || dec != "secret-token" {
		t.Fatalf("decrypt: %v %q", err, dec)
	}
	other, _ := New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("wrong key must fail")
	}
	exp, sig := b.SignAsset("a1", time.Minute)
	if !b.VerifyAsset("a1", exp, sig) || b.VerifyAsset("a2", exp, sig) || b.VerifyAsset("a1", "1", sig) {
		t.Fatal("signature check wrong")
	}
	if _, err := New("short"); err == nil {
		t.Fatal("bad key accepted")
	}
}
