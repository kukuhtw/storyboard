// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package config loads settings from environment variables only.
// Secrets (API keys, DB password, admin bootstrap password) must never be hard-coded,
// committed, stored in the database, or written to logs.
package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Provider struct {
	Kind   string // "mock" | "http"
	URL    string
	APIKey string
}

// DefaultSourceURL is the public repository of the upstream project.
const DefaultSourceURL = "https://github.com/kukuhtw/storyboard/"

type Config struct {
	Addr        string
	DatabaseURL string
	// Authentication (individual accounts). AdminUsername/AdminPassword only bootstrap
	// the first admin when the users table is empty.
	SessionTTL        time.Duration
	SessionIdle       time.Duration
	AdminUsername     string
	AdminPassword     string
	LegacyAccessToken bool // APP_ACCESS_TOKEN is set but no longer used
	DataDir           string
	MaxUploadMB       int64
	CookieSecure      bool
	TrustProxy        bool
	ClaudeModel       string
	ClaudeEnabled     bool
	Image             Provider
	Video             Provider
	TTS               Provider
	TTSVoice          string
	BytePlus          BytePlus
	OpenAITTS         OpenAITTS

	FFmpegPath    string
	FFprobePath   string
	PublicBaseURL string // e.g. https://storyboard.example.com (OAuth redirects + signed media URLs)
	// SourceURL is shown in the UI footer so network users can get the Corresponding
	// Source (AGPL-3.0 section 13). Forks that modify the code must point it at their
	// own source. "off" hides the link (only for commercially licensed deployments).
	SourceURL    string
	TokenEncKey  string // base64 32 bytes; required for social publishing
	PostApproval bool   // posts need explicit approval before the scheduler publishes them
	Social       SocialApps
}

type SocialApps struct {
	GoogleClientID, GoogleClientSecret string
	TikTokClientKey, TikTokSecret      string
	InstagramAppID, InstagramSecret    string
	InstagramGraphVersion              string
	ThreadsAppID, ThreadsSecret        string
	ThreadsGraphVersion                string
}

// OpenAI text-to-speech settings (TTS_PROVIDER=openai).
type OpenAITTS struct {
	BaseURL      string
	APIKey       string
	Model        string
	Voice        string
	Instructions string
	Speed        float64
}

// BytePlus ModelArk settings (IMAGE_PROVIDER / VIDEO_PROVIDER = byteplus).
type BytePlus struct {
	BaseURL         string
	APIKey          string
	ImageModel      string
	VideoModel      string
	ImageSize       string
	VideoResolution string
	VideoMinSec     int
	VideoMaxSec     int
	Watermark       bool
}

// LoadDotEnv reads KEY=VALUE lines from path into the process env (without
// overriding values already set). Convenience for local development only.
func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func provider(prefix string) Provider {
	p := Provider{
		Kind:   strings.ToLower(env(prefix+"_PROVIDER", "mock")),
		URL:    env(prefix+"_API_URL", ""),
		APIKey: env(prefix+"_API_KEY", ""),
	}
	if p.Kind == "http" && p.URL == "" {
		p.Kind = "mock"
	}
	return p
}

func Load() (*Config, error) {
	maxMB, _ := strconv.ParseInt(env("MAX_UPLOAD_MB", "100"), 10, 64)
	c := &Config{
		Addr:          env("APP_ADDR", "127.0.0.1:8080"),
		DatabaseURL:   env("DATABASE_URL", ""),
		DataDir:       env("DATA_DIR", "./data"),
		MaxUploadMB:   maxMB,
		CookieSecure:  env("COOKIE_SECURE", "false") == "true",
		TrustProxy:    env("TRUST_PROXY", "false") == "true",
		ClaudeModel:   env("CLAUDE_MODEL", "claude-opus-5-5"),
		ClaudeEnabled: env("CLAUDE_DISABLED", "false") != "true",
		Image:         provider("IMAGE"),
		Video:         provider("VIDEO"),
		TTS:           provider("TTS"),
		TTSVoice:      env("TTS_VOICE", "default"),
	}
	minSec, _ := strconv.Atoi(env("BYTEPLUS_VIDEO_MIN_SEC", "5"))
	maxSec, _ := strconv.Atoi(env("BYTEPLUS_VIDEO_MAX_SEC", "10"))
	c.BytePlus = BytePlus{
		BaseURL:         env("BYTEPLUS_BASE_URL", "https://ark.ap-southeast.bytepluses.com/api/v3"),
		APIKey:          env("BYTEPLUS_API_KEY", env("ARK_API_KEY", "")),
		ImageModel:      env("BYTEPLUS_IMAGE_MODEL", ""),
		VideoModel:      env("BYTEPLUS_VIDEO_MODEL", ""),
		ImageSize:       env("BYTEPLUS_IMAGE_SIZE", ""),
		VideoResolution: env("BYTEPLUS_VIDEO_RESOLUTION", "720p"),
		VideoMinSec:     minSec,
		VideoMaxSec:     maxSec,
		Watermark:       env("BYTEPLUS_WATERMARK", "false") == "true",
	}
	speed, _ := strconv.ParseFloat(env("OPENAI_TTS_SPEED", "1.0"), 64)
	if speed < 0.25 || speed > 4.0 {
		speed = 1.0
	}
	c.OpenAITTS = OpenAITTS{
		BaseURL:      env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		APIKey:       env("OPENAI_API_KEY", ""),
		Model:        env("OPENAI_TTS_MODEL", "gpt-4o-mini-tts"),
		Voice:        env("OPENAI_TTS_VOICE", "alloy"),
		Instructions: env("OPENAI_TTS_INSTRUCTIONS", ""),
		Speed:        speed,
	}
	c.SessionTTL = time.Duration(envInt("SESSION_TTL_HOURS", 12, 1, 24*30)) * time.Hour
	c.SessionIdle = time.Duration(envInt("SESSION_IDLE_MINUTES", 60, 5, 24*60)) * time.Minute
	c.AdminUsername = env("ADMIN_USERNAME", "admin")
	c.AdminPassword = env("ADMIN_PASSWORD", "")
	c.LegacyAccessToken = env("APP_ACCESS_TOKEN", "") != ""
	c.FFmpegPath = env("FFMPEG_PATH", "ffmpeg")
	c.FFprobePath = env("FFPROBE_PATH", "ffprobe")
	c.PublicBaseURL = strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/")
	c.SourceURL = env("SOURCE_URL", DefaultSourceURL)
	if strings.EqualFold(c.SourceURL, "off") {
		c.SourceURL = ""
	} else if u, err := url.Parse(c.SourceURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("SOURCE_URL must be an https:// URL (or \"off\")")
	}
	c.TokenEncKey = env("TOKEN_ENC_KEY", "")
	c.PostApproval = env("POST_APPROVAL", "true") != "false"
	c.Social = SocialApps{
		GoogleClientID: env("GOOGLE_CLIENT_ID", ""), GoogleClientSecret: env("GOOGLE_CLIENT_SECRET", ""),
		TikTokClientKey: env("TIKTOK_CLIENT_KEY", ""), TikTokSecret: env("TIKTOK_CLIENT_SECRET", ""),
		InstagramAppID: env("INSTAGRAM_APP_ID", ""), InstagramSecret: env("INSTAGRAM_APP_SECRET", ""),
		InstagramGraphVersion: env("INSTAGRAM_GRAPH_VERSION", "v27.0"),
		ThreadsAppID:          env("THREADS_APP_ID", ""), ThreadsSecret: env("THREADS_APP_SECRET", ""),
		ThreadsGraphVersion: env("THREADS_GRAPH_VERSION", "v1.0"),
	}
	if c.PublicBaseURL != "" && !strings.HasPrefix(c.PublicBaseURL, "https://") && !strings.HasPrefix(c.PublicBaseURL, "http://localhost") && !strings.HasPrefix(c.PublicBaseURL, "http://127.0.0.1") {
		return nil, fmt.Errorf("PUBLIC_BASE_URL must use https://")
	}
	if c.TTS.Kind == "openai" && c.OpenAITTS.APIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required when TTS_PROVIDER=openai")
	}
	for _, p := range []*Provider{&c.Image, &c.Video} {
		if p.Kind == "byteplus" && c.BytePlus.APIKey == "" {
			return nil, fmt.Errorf("BYTEPLUS_API_KEY is required when a provider is set to byteplus")
		}
	}
	if c.Image.Kind == "byteplus" && c.BytePlus.ImageModel == "" {
		return nil, fmt.Errorf("BYTEPLUS_IMAGE_MODEL is required (model ID or endpoint ID from ModelArk console)")
	}
	if c.Video.Kind == "byteplus" && c.BytePlus.VideoModel == "" {
		return nil, fmt.Errorf("BYTEPLUS_VIDEO_MODEL is required (model ID or endpoint ID from ModelArk console)")
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.MaxUploadMB <= 0 {
		c.MaxUploadMB = 100
	}
	return c, nil
}

// envInt reads an integer env var, clamped to [lo, hi].
func envInt(key string, def, lo, hi int) int {
	n, err := strconv.Atoi(env(key, ""))
	if err != nil {
		n = def
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}
