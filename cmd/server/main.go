// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"storyboard/internal/ai"
	"storyboard/internal/api"
	"storyboard/internal/config"
	"storyboard/internal/db"
	"storyboard/internal/media"
	"storyboard/internal/pipeline"
	"storyboard/internal/providers"
	"storyboard/internal/render"
	"storyboard/internal/secure"
	"storyboard/internal/social"
	"storyboard/internal/store"
	"storyboard/web"
)

// healthcheck is used by Docker/Swarm HEALTHCHECK (the image has no curl).
func healthcheck() {
	addr := os.Getenv("APP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if strings.HasPrefix(addr, "0.0.0.0:") || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1:" + addr[strings.LastIndex(addr, ":")+1:]
	}
	c := http.Client{Timeout: 5 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	os.Exit(0)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		healthcheck()
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	config.LoadDotEnv(".env")
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err) // pgx errors do not include the password
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	st := &store.Store{DB: pool}
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		code := resetPassword(ctx, st, os.Args[2:])
		pool.Close()
		os.Exit(code)
	}
	if err := bootstrapAdmin(ctx, st, cfg); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}
	if err := st.ResetStuck(ctx); err != nil {
		slog.Warn("reset stuck stages", "err", err)
	}
	ms, err := media.New(cfg.DataDir)
	if err != nil {
		slog.Error("media dir", "err", err)
		os.Exit(1)
	}

	var claude *ai.Claude
	if cfg.ClaudeEnabled {
		claude = ai.New(cfg.ClaudeModel)
	}
	engine := &pipeline.Engine{
		Store: st, Media: ms, Claude: claude, Providers: providers.Build(cfg),
		Voice: cfg.TTSVoice, MaxGenMB: 500,
		Renderer: &render.Renderer{FFmpeg: cfg.FFmpegPath, FFprobe: cfg.FFprobePath, TmpDir: filepath.Join(cfg.DataDir, "tmp")},
	}
	if err := engine.Renderer.Available(); err != nil {
		slog.Warn("final render unavailable", "err", err)
	}

	var box *secure.Box
	if cfg.TokenEncKey != "" {
		if box, err = secure.New(cfg.TokenEncKey); err != nil {
			slog.Error("TOKEN_ENC_KEY", "err", err)
			os.Exit(1)
		}
	}
	redirect := func(p string) string { return cfg.PublicBaseURL + "/oauth/" + p + "/callback" }
	sc := cfg.Social
	socialSvc := &social.Service{
		Store: st, Box: box, Media: ms, PublicBaseURL: cfg.PublicBaseURL, Probe: engine.Renderer.Duration,
		Platforms: map[string]social.Platform{
			"youtube":   social.NewYouTube(sc.GoogleClientID, sc.GoogleClientSecret, redirect("youtube")),
			"tiktok":    social.NewTikTok(sc.TikTokClientKey, sc.TikTokSecret, redirect("tiktok")),
			"instagram": social.NewInstagram(sc.InstagramAppID, sc.InstagramSecret, redirect("instagram"), sc.InstagramGraphVersion),
			"threads":   social.NewThreads(sc.ThreadsAppID, sc.ThreadsSecret, redirect("threads"), sc.ThreadsGraphVersion),
		},
	}
	schedCtx, stopSched := context.WithCancel(context.Background())
	schedDone := make(chan struct{})
	go func() { socialSvc.Run(schedCtx); close(schedDone) }()
	api.TrustProxy = cfg.TrustProxy
	srv := &api.Server{Cfg: cfg, Store: st, Media: ms, Engine: engine, Social: socialSvc, Web: web.FS()}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("Storyboard Studio listening", "addr", "http://"+cfg.Addr,
			"claude_model", cfg.ClaudeModel, "image", cfg.Image.Kind, "video", cfg.Video.Kind, "tts", cfg.TTS.Kind)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpSrv.Shutdown(sctx)
	stopSched()
	select {
	case <-schedDone:
	case <-time.After(30 * time.Second):
		slog.Warn("scheduler did not stop in time")
	}
}
