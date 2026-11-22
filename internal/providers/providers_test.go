// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"storyboard/internal/config"
)

func TestMockOutputs(t *testing.T) {
	m := Mock{}
	img, err := m.GenerateImage(context.Background(), ImageRequest{Prompt: "a <b>cat</b> & dog", AspectRatio: "9:16"})
	if err != nil || img.Mime != "image/svg+xml" || !strings.Contains(string(img.Data), "&lt;b&gt;cat") {
		t.Fatalf("mock image not escaped: %v", err)
	}
	a, _ := m.Synthesize(context.Background(), TTSRequest{Text: "satu dua tiga empat lima"})
	if a.Mime != "audio/wav" || string(a.Data[:4]) != "RIFF" {
		t.Fatal("mock wav invalid")
	}
}

func TestHTTPInlineAndAsync(t *testing.T) {
	var polls int32
	png := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/img":
			json.NewEncoder(w).Encode(map[string]string{"b64": base64.StdEncoding.EncodeToString(png), "mime": "image/png"})
		case "/vid":
			json.NewEncoder(w).Encode(map[string]string{"status": "queued", "status_url": "http://" + r.Host + "/status"})
		case "/status":
			if atomic.AddInt32(&polls, 1) < 2 {
				json.NewEncoder(w).Encode(map[string]string{"status": "running"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "succeeded", "b64": base64.StdEncoding.EncodeToString([]byte("vid")), "mime": "video/mp4"})
		}
	}))
	defer srv.Close()

	h := NewHTTP(config.Provider{Kind: "http", URL: srv.URL + "/img", APIKey: "k"})
	m, err := h.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err != nil || m.Mime != "image/png" {
		t.Fatalf("inline: %v %+v", err, m)
	}

	v := NewHTTP(config.Provider{Kind: "http", URL: srv.URL + "/vid", APIKey: "k"})
	v.PollEvery = 10 * time.Millisecond
	m, err = v.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"})
	if err != nil || m.Mime != "video/mp4" || string(m.Data) != "vid" {
		t.Fatalf("async: %v %+v", err, m)
	}
}
