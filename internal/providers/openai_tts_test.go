// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAITTS(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"Incorrect API key"}}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3mp3"))
	}))
	defer srv.Close()

	tts := NewOpenAITTS(OpenAITTSConfig{BaseURL: srv.URL + "/v1", APIKey: "sk-test", Instructions: "Warm and calm."})
	m, err := tts.Synthesize(context.Background(), TTSRequest{Text: "Halo, selamat datang.", Voice: "default", Language: "id"})
	if err != nil || m.Mime != "audio/mpeg" || string(m.Data) != "ID3mp3" {
		t.Fatalf("synth: %v %+v", err, m)
	}
	if got["model"] != "gpt-4o-mini-tts" || got["voice"] != "alloy" || got["response_format"] != "mp3" {
		t.Fatalf("bad request: %v", got)
	}
	if instr, _ := got["instructions"].(string); !strings.Contains(instr, "Indonesian") || !strings.Contains(instr, "Warm") {
		t.Fatalf("instructions missing: %v", got["instructions"])
	}

	// tts-1 must not receive "instructions"
	got = nil
	tts1 := NewOpenAITTS(OpenAITTSConfig{BaseURL: srv.URL + "/v1", APIKey: "sk-test", Model: "tts-1", Voice: "nova"})
	if _, err := tts1.Synthesize(context.Background(), TTSRequest{Text: "hi", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["instructions"]; ok || got["voice"] != "nova" {
		t.Fatalf("tts-1 request wrong: %v", got)
	}

	bad := NewOpenAITTS(OpenAITTSConfig{BaseURL: srv.URL + "/v1", APIKey: "nope"})
	if _, err := bad.Synthesize(context.Background(), TTSRequest{Text: "x"}); err == nil || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("expected API error, got %v", err)
	}
	if _, err := tts.Synthesize(context.Background(), TTSRequest{Text: strings.Repeat("a", 4097)}); err == nil {
		t.Fatal("expected length error")
	}
}
