// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package secure provides AES-256-GCM encryption for OAuth tokens at rest and
// HMAC-signed, expiring URLs for media that platforms must fetch publicly.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Box struct {
	aead    cipher.AEAD
	signKey []byte
}

// New expects a base64-encoded 32-byte key (TOKEN_ENC_KEY).
func New(b64Key string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("TOKEN_ENC_KEY must be base64 of exactly 32 random bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Separate key for URL signing, derived so the encryption key is never used for two purposes.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("storyboard/url-signing/v1"))
	return &Box{aead: aead, signKey: mac.Sum(nil)}, nil
}

func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(ct), nil
}

func (b *Box) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if len(enc) < 3 || enc[:3] != "v1:" {
		return "", errors.New("unknown token encoding")
	}
	raw, err := base64.StdEncoding.DecodeString(enc[3:])
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("token decryption failed (wrong TOKEN_ENC_KEY?)")
	}
	return string(pt), nil
}

func (b *Box) sig(assetID string, exp int64) string {
	mac := hmac.New(sha256.New, b.signKey)
	fmt.Fprintf(mac, "%s|%d", assetID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignAsset returns query values exp & sig valid for ttl.
func (b *Box) SignAsset(assetID string, ttl time.Duration) (string, string) {
	exp := time.Now().Add(ttl).Unix()
	return strconv.FormatInt(exp, 10), b.sig(assetID, exp)
}

func (b *Box) VerifyAsset(assetID, expStr, sig string) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(b.sig(assetID, exp)), []byte(sig))
}
