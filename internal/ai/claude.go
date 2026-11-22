// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package ai wraps the Claude API (text + vision) for structured generation.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

type Image struct {
	Mime string // image/jpeg|png|gif|webp
	Data []byte
	Note string
}

type Claude struct {
	client anthropic.Client
	model  string
}

// New uses the SDK's default credential chain (ANTHROPIC_API_KEY, or a profile from `ant auth login`).
func New(model string) *Claude {
	return &Claude{client: anthropic.NewClient(), model: model}
}

// GenerateJSON asks Claude for output that conforms to schema (structured outputs)
// and unmarshals it into out. Reference images are attached for visual grounding.
func (c *Claude) GenerateJSON(ctx context.Context, system, prompt string, images []Image, schema map[string]any, out any) error {
	blocks := []anthropic.BetaContentBlockParamUnion{}
	for i, img := range images {
		blocks = append(blocks,
			anthropic.NewBetaTextBlock(fmt.Sprintf("Gambar referensi #%d. Catatan user: %s", i+1, nz(img.Note, "-"))),
			anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
				Data:      base64.StdEncoding.EncodeToString(img.Data),
				MediaType: anthropic.BetaBase64ImageSourceMediaType(img.Mime),
			}),
		)
	}
	blocks = append(blocks, anthropic.NewBetaTextBlock(prompt))

	params := anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 64000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortHigh,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
		// Server-side refusal fallback: a policy decline is re-served by a fallback model in the same call.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
	}

	stream := c.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return errors.New("Claude API: kredensial tidak valid / tidak ada akses")
			case 429:
				return errors.New("Claude API: rate limit, coba lagi sebentar")
			default:
				return fmt.Errorf("Claude API error (HTTP %d)", apiErr.StatusCode)
			}
		}
		return fmt.Errorf("Claude API: %w", err)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return errors.New("Claude menolak permintaan ini (kebijakan konten). Ubah brief/feedback lalu coba lagi")
	case anthropic.BetaStopReasonMaxTokens:
		return errors.New("output terpotong (max_tokens); kurangi jumlah scene atau durasi")
	}
	var sb strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	if err := json.Unmarshal([]byte(sb.String()), out); err != nil {
		return fmt.Errorf("gagal membaca JSON dari Claude: %w", err)
	}
	return nil
}

func nz(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
