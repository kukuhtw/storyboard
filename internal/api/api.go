// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"storyboard/internal/config"
	"storyboard/internal/media"
	"storyboard/internal/pipeline"
	"storyboard/internal/social"
	"storyboard/internal/store"
)

type Server struct {
	Cfg    *config.Config
	Store  *store.Store
	Media  *media.Storage
	Engine *pipeline.Engine
	Social *social.Service
	Web    fs.FS

	loginMu       sync.Mutex
	loginFailures map[string][]time.Time
}

const sessionCookie = "sb_session"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /healthz", s.healthz) // unauthenticated; reveals nothing but up/down

	auth := func(h http.HandlerFunc) http.Handler { return s.requireAuth(h) }
	admin := func(h http.HandlerFunc) http.Handler { return s.requireAuth(s.adminOnly(h)) }
	mux.Handle("GET /api/me", auth(s.me))
	mux.Handle("POST /api/me/password", auth(s.changePassword))
	mux.Handle("GET /api/admin/users", admin(s.adminListUsers))
	mux.Handle("POST /api/admin/users", admin(s.adminCreateUser))
	mux.Handle("PUT /api/admin/users/{uid}", admin(s.adminUpdateUser))
	mux.Handle("POST /api/admin/users/{uid}/reset-password", admin(s.adminResetPassword))
	mux.Handle("POST /api/admin/users/{uid}/unlock", admin(s.adminUnlockUser))
	mux.Handle("POST /api/admin/users/{uid}/revoke-sessions", admin(s.adminRevokeSessions))
	mux.Handle("GET /api/admin/audit", admin(s.adminAudit))
	mux.Handle("GET /api/config", auth(s.getConfig))
	mux.Handle("GET /api/projects", auth(s.listProjects))
	mux.Handle("POST /api/projects", auth(s.createProject))
	mux.Handle("GET /api/projects/{id}", auth(s.getProject))
	mux.Handle("PUT /api/projects/{id}", auth(s.updateProject))
	mux.Handle("DELETE /api/projects/{id}", auth(s.deleteProject))
	mux.Handle("GET /api/projects/{id}/export", auth(s.exportProject))
	mux.Handle("POST /api/projects/{id}/stages/{kind}/run", auth(s.runStage))
	mux.Handle("POST /api/projects/{id}/stages/{kind}/approve", auth(s.approveStage))
	mux.Handle("POST /api/projects/{id}/stages/{kind}/reopen", auth(s.reopenStage))
	mux.Handle("PUT /api/projects/{id}/stages/{kind}/feedback", auth(s.setFeedback))
	mux.Handle("PUT /api/projects/{id}/scenes/{sid}", auth(s.updateScene))
	mux.Handle("POST /api/projects/{id}/assets", auth(s.uploadAsset))
	mux.Handle("DELETE /api/projects/{id}/assets/{aid}", auth(s.deleteAsset))
	mux.Handle("GET /media/{aid}", auth(s.serveMedia))

	s.socialRoutes(mux, auth, admin)

	mux.Handle("/", http.FileServerFS(s.Web))
	return s.securityHeaders(s.logRequests(mux))
}

// ---------------- middleware ----------------

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		if s.Cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// logRequests logs method, path and status only — never bodies, headers, tokens or query strings.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || sw.code >= 400 {
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Store.DB.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// TrustProxy: when true (behind Dokploy/Traefik), the client IP is the last
// X-Forwarded-For entry, i.e. the address the reverse proxy itself saw.
var TrustProxy bool

func clientIP(r *http.Request) string {
	if TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------- helpers ----------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func (s *Server) storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	slog.Error("store error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := r.PathValue(name)
	if !uuidRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return "", false
	}
	return id, true
}

func validStage(kind string) bool {
	for _, k := range store.StageOrder {
		if k == kind {
			return true
		}
	}
	return false
}

// ---------------- auth handlers ----------------

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"stages":        store.StageOrder,
		"claude_model":  s.Cfg.ClaudeModel,
		"claude":        s.Cfg.ClaudeEnabled,
		"providers":     s.Engine.Providers.Names,
		"max_upload_mb": s.Cfg.MaxUploadMB,
		"social":        s.Social.Status(),
	})
}

// ---------------- projects ----------------

var validRatios = map[string]bool{"16:9": true, "9:16": true, "1:1": true, "4:5": true}

func validateProject(p *store.Project) string {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len(p.Title) > 200 {
		return "judul wajib diisi (maks 200 karakter)"
	}
	if len(p.Brief) > 20000 || len(p.Tone) > 500 {
		return "brief/tone terlalu panjang"
	}
	if p.TargetDurationSec < 5 || p.TargetDurationSec > 1800 {
		return "durasi harus 5–1800 detik"
	}
	if p.AspectRatio == "" {
		p.AspectRatio = "16:9"
	}
	if !validRatios[p.AspectRatio] {
		return "rasio tidak valid"
	}
	if p.Language == "" {
		p.Language = "id"
	}
	if p.ImageStyle == "" {
		p.ImageStyle = "cinematic"
	}
	if _, ok := pipeline.ImageStyles[p.ImageStyle]; !ok {
		return "gaya visual tidak valid"
	}
	if len(p.Language) > 20 {
		return "bahasa tidak valid"
	}
	return ""
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListProjects(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ps)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var p store.Project
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	switch p.VideoType {
	case "documentation", "advertisement", "drama":
	default:
		writeErr(w, 400, "video_type harus documentation, advertisement, atau drama")
		return
	}
	if msg := validateProject(&p); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	p.CreatedBy = actorName(r)
	if err := s.Store.CreateProject(r.Context(), &p); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "create", "project", p.ID, map[string]any{"video_type": p.VideoType})
	writeJSON(w, 201, p)
}

func (s *Server) projectBundle(r *http.Request, id string) (map[string]any, error) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	stages, err := s.Store.ListStages(ctx, id)
	if err != nil {
		return nil, err
	}
	scenes, err := s.Store.ListScenes(ctx, id)
	if err != nil {
		return nil, err
	}
	assets, err := s.Store.ListAssets(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": p, "stages": stages, "scenes": scenes, "assets": assets}, nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	b, err := s.projectBundle(r, id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var p store.Project
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	p.ID = id
	if msg := validateProject(&p); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.Store.UpdateProject(r.Context(), &p); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "update", "project", id, nil)
	writeJSON(w, 200, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	names, err := s.Store.DeleteProject(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Media.Remove(names...) // erase files too (right to erasure / data minimisation)
	s.audit(r, "delete", "project", id, map[string]any{"files_erased": len(names)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) exportProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	b, err := s.projectBundle(r, id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "export", "project", id, nil)
	w.Header().Set("Content-Disposition", `attachment; filename="storyboard-`+id+`.json"`)
	writeJSON(w, 200, b)
}

// ---------------- stages ----------------

func (s *Server) runStage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !validStage(kind) {
		writeErr(w, 400, "invalid stage")
		return
	}
	var body struct {
		SceneID  string  `json:"scene_id"`
		Feedback *string `json:"feedback"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
	}
	if body.SceneID != "" {
		if !uuidRe.MatchString(body.SceneID) {
			writeErr(w, 400, "invalid scene_id")
			return
		}
		if kind == "storyboard" {
			writeErr(w, 400, "storyboard selalu dibuat ulang untuk seluruh scene")
			return
		}
		if _, err := s.Store.GetScene(r.Context(), id, body.SceneID); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if body.Feedback != nil {
		if len(*body.Feedback) > 5000 {
			writeErr(w, 400, "feedback terlalu panjang")
			return
		}
		if err := s.Store.SetFeedback(r.Context(), id, kind, *body.Feedback); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if err := s.Engine.Run(id, kind, body.SceneID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "run_stage", "project", id, map[string]any{"stage": kind, "scene_id": body.SceneID})
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) approveStage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !validStage(kind) {
		writeErr(w, 400, "invalid stage")
		return
	}
	if err := s.Store.Approve(r.Context(), id, kind); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "approve_stage", "project", id, map[string]any{"stage": kind})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) reopenStage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !validStage(kind) {
		writeErr(w, 400, "invalid stage")
		return
	}
	if err := s.Store.Reopen(r.Context(), id, kind); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "reopen_stage", "project", id, map[string]any{"stage": kind})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) setFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !validStage(kind) {
		writeErr(w, 400, "invalid stage")
		return
	}
	var body struct {
		Feedback string `json:"feedback"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Feedback) > 5000 {
		writeErr(w, 400, "invalid feedback")
		return
	}
	if err := s.Store.SetFeedback(r.Context(), id, kind, body.Feedback); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------------- scenes ----------------

func (s *Server) updateScene(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	sid, ok := pathID(w, r, "sid")
	if !ok {
		return
	}
	var f map[string]any
	if err := readJSON(r, &f); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	for k, v := range f {
		if k == "duration_sec" {
			n, ok := v.(float64)
			if !ok || n <= 0 || n > 120 {
				writeErr(w, 400, "duration_sec harus 0–120")
				return
			}
			continue
		}
		str, ok := v.(string)
		if !ok || len(str) > 10000 {
			writeErr(w, 400, "field "+k+" tidak valid")
			return
		}
	}
	if _, err := s.Store.GetScene(r.Context(), id, sid); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.UpdateSceneText(r.Context(), id, sid, f); err != nil {
		s.storeErr(w, err)
		return
	}
	sc, err := s.Store.GetScene(r.Context(), id, sid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "edit_scene", "scene", sid, nil)
	writeJSON(w, 200, sc)
}

// ---------------- assets ----------------

var uploadMimes = map[string]string{
	"image/png": "ref_image", "image/jpeg": "ref_image", "image/webp": "ref_image", "image/gif": "ref_image",
	"video/mp4": "ref_video", "video/webm": "ref_video", "video/quicktime": "ref_video",
}

func (s *Server) uploadAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.Store.GetProject(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	limit := s.Cfg.MaxUploadMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected multipart/form-data")
		return
	}
	fields := map[string]string{}
	var saved *store.Asset
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if part.FormName() != "file" {
			b := make([]byte, 2001)
			n, _ := part.Read(b)
			fields[part.FormName()] = string(b[:n])
			continue
		}
		if saved != nil {
			break
		}
		// Sniff actual content; never trust the client-declared type or extension.
		head := make([]byte, 512)
		n, _ := readFull(part, head)
		head = head[:n]
		sniffed := http.DetectContentType(head)
		if sniffed == "application/octet-stream" && isQuickTime(head) {
			sniffed = "video/quicktime"
		}
		kind, ok := uploadMimes[sniffed]
		if !ok {
			writeErr(w, 400, "tipe file tidak didukung ("+sniffed+"). Gunakan PNG/JPEG/WEBP/GIF atau MP4/WEBM/MOV")
			return
		}
		name, size, err := s.Media.Save(multiReader(head, part), sniffed, limit)
		if err != nil {
			writeErr(w, 400, "upload gagal: "+err.Error())
			return
		}
		orig := filepath.Base(part.FileName())
		if len(orig) > 200 {
			orig = orig[:200]
		}
		saved = &store.Asset{ProjectID: id, Kind: kind, StorageName: name, OriginalName: orig, Mime: sniffed, SizeBytes: size, Source: "upload"}
	}
	if saved == nil {
		writeErr(w, 400, "file wajib diunggah")
		return
	}
	if fields["consent"] != "true" {
		s.Media.Remove(saved.StorageName)
		writeErr(w, 400, "Anda harus mengonfirmasi hak/izin penggunaan media (termasuk persetujuan orang yang tampil)")
		return
	}
	saved.Consent = true
	saved.Note = strings.TrimSpace(fields["note"])
	if sid := fields["scene_id"]; sid != "" {
		if !uuidRe.MatchString(sid) {
			s.Media.Remove(saved.StorageName)
			writeErr(w, 400, "invalid scene_id")
			return
		}
		if _, err := s.Store.GetScene(r.Context(), id, sid); err != nil {
			s.Media.Remove(saved.StorageName)
			s.storeErr(w, err)
			return
		}
		saved.SceneID = &sid
	}
	if err := s.Store.CreateAsset(r.Context(), saved); err != nil {
		s.Media.Remove(saved.StorageName)
		s.storeErr(w, err)
		return
	}
	s.audit(r, "upload_reference", "asset", saved.ID, map[string]any{"project_id": id, "kind": saved.Kind, "mime": saved.Mime, "size": saved.SizeBytes})
	writeJSON(w, 201, saved)
}

func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	aid, ok := pathID(w, r, "aid")
	if !ok {
		return
	}
	name, err := s.Store.DeleteAsset(r.Context(), id, aid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Media.Remove(name)
	s.audit(r, "delete", "asset", aid, map[string]any{"project_id": id})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	aid, ok := pathID(w, r, "aid")
	if !ok {
		return
	}
	a, err := s.Store.GetAsset(r.Context(), aid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	p, err := s.Media.Path(a.StorageName)
	if err != nil {
		writeErr(w, 500, "invalid asset")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "file missing")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", a.Mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// Media is never executed as a document (blocks script in SVG etc.).
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if r.URL.Query().Get("download") == "1" {
		ext, _ := media.ExtFor(a.Mime)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Kind + "-" + strconv.Itoa(int(a.CreatedAt.Unix())) + "." + ext}))
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}
