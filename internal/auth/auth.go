// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package auth holds password hashing (Argon2id), password policy, temporary
// passwords and session tokens. ISO/IEC 27001:2022 A.5.17 / A.8.5.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// OWASP Password Storage Cheat Sheet (Argon2id, m=19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16

	MinPasswordLen = 12
	MaxPasswordLen = 128
)

// HashPassword returns a PHC-style string: $argon2id$v=19$m=..,t=..,p=..$salt$hash
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword checks pw against an encoded hash in constant time.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 256*1024 || t == 0 || t > 10 || p == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash, _ = HashPassword("timing-equaliser-not-a-real-password")

// DummyVerify burns the same CPU as a real check so that unknown usernames
// cannot be distinguished from wrong passwords by response time.
func DummyVerify(pw string) { VerifyPassword(dummyHash, pw) }

var commonPasswords = map[string]bool{
	"password1234": true, "password12345": true, "123456789012": true, "qwertyuiop12": true,
	"administrator": true, "admin1234567": true, "passw0rd1234": true, "welcome12345": true,
	"iloveyou1234": true, "changeme1234": true, "storyboard123": true, "storyboard1234": true,
}

// ValidatePassword enforces the password policy (length-based, NIST SP 800-63B style).
func ValidatePassword(pw, username string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return fmt.Errorf("password minimal %d karakter", MinPasswordLen)
	}
	if n > MaxPasswordLen {
		return fmt.Errorf("password maksimal %d karakter", MaxPasswordLen)
	}
	low := strings.ToLower(pw)
	if commonPasswords[low] {
		return errors.New("password terlalu umum")
	}
	if username != "" && strings.Contains(low, strings.ToLower(username)) {
		return errors.New("password tidak boleh memuat username")
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return errors.New("password terlalu mudah ditebak (karakter berulang)")
	}
	return nil
}

const tempAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// TempPassword returns a random one-time password like "aB3d-Ef7h-Jk9m-Np2q".
func TempPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	var sb strings.Builder
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(tempAlphabet[int(v)%len(tempAlphabet)])
	}
	return sb.String()
}

// NewSessionToken returns a random token for the cookie and its SHA-256 hash
// for the database (a DB leak does not reveal usable session cookies).
func NewSessionToken() (string, []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, HashToken(tok)
}

func HashToken(tok string) []byte {
	h := sha256.Sum256([]byte("sb-session:" + tok))
	return h[:]
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,49}$`)

// NormalizeUsername lowercases and validates a username.
func NormalizeUsername(s string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(s))
	if !usernameRe.MatchString(u) {
		return "", errors.New("username 3–50 karakter: huruf kecil, angka, titik, minus, garis bawah")
	}
	return u, nil
}
