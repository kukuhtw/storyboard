// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package providers

// OpenAI text-to-speech: POST {base}/audio/speech → raw audio bytes.
// (Whisper is speech-to-text and is not used here.)

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type OpenAITTSConfig struct {
	BaseURL      string // default https://api.openai.com/v1
	APIKey       string
	Model        string // gpt-4o-mini-tts | tts-1 | tts-1-hd
	Voice        string // e.g. alloy, coral, nova, onyx, sage, shimmer
	Instructions string // optional speaking style (gpt-4o-* models only)
	Speed        float64
}

type OpenAITTS struct {
	cfg    OpenAITTSConfig
	client *http.Client
}

const openAITTSMaxChars = 4096

func NewOpenAITTS(c OpenAITTSConfig) *OpenAITTS {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.Model == "" {
		c.Model = "gpt-4o-mini-tts"
	}
	if c.Voice == "" || c.Voice == "default" {
		c.Voice = "alloy"
	}
	return &OpenAITTS{cfg: c, client: &http.Client{Timeout: 3 * time.Minute}}
}

var languageNames = map[string]string{"id": "Indonesian (Bahasa Indonesia)", "en": "English"}

type openAISpeechReq struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Instructions   string  `json:"instructions,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

func (o *OpenAITTS) Synthesize(ctx context.Context, r TTSRequest) (*Media, error) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return nil, errors.New("teks voice over kosong")
	}
	if utf8.RuneCountInString(text) > openAITTSMaxChars {
		return nil, fmt.Errorf("voice over terlalu panjang untuk OpenAI TTS (maks %d karakter); pecah scene", openAITTSMaxChars)
	}
	voice := o.cfg.Voice
	if r.Voice != "" && r.Voice != "default" {
		voice = r.Voice
	}
	req := openAISpeechReq{Model: o.cfg.Model, Input: text, Voice: voice, ResponseFormat: "mp3", Speed: o.cfg.Speed}
	// Only gpt-4o-* TTS models accept "instructions".
	if strings.HasPrefix(o.cfg.Model, "gpt-4o") {
		instr := o.cfg.Instructions
		if lang := languageNames[r.Language]; lang != "" {
			instr = strings.TrimSpace("Speak in " + lang + " with natural native pronunciation. " + instr)
		}
		req.Instructions = instr
	}
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	hreq.Header.Set("Content-Type", "application/json")
	res, err := o.client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("OpenAI TTS request failed: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 100<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("OpenAI TTS HTTP %d: %s", res.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("OpenAI TTS HTTP %d", res.StatusCode)
	}
	if len(data) == 0 {
		return nil, errors.New("OpenAI TTS: audio kosong")
	}
	return &Media{Mime: "audio/mpeg", Data: data}, nil
}
