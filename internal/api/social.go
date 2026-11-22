// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package api

import (
	"net/http"
	"os"
	"strings"
	"time"

	"storyboard/internal/store"
)

var socialPlatforms = map[string]bool{"youtube": true, "instagram": true, "threads": true, "tiktok": true}

func (s *Server) socialRoutes(mux *http.ServeMux, auth, admin func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/social/status", auth(s.socialStatus))
	mux.Handle("GET /api/social/accounts", auth(s.listAccounts))
	mux.Handle("DELETE /api/social/accounts/{aid}", admin(s.deleteAccount))
	mux.Handle("POST /api/social/{platform}/connect", admin(s.connectAccount))
	mux.HandleFunc("GET /oauth/{platform}/callback", s.oauthCallback) // protected by one-time state
	mux.Handle("GET /api/projects/{id}/posts", auth(s.listPosts))
	mux.Handle("POST /api/projects/{id}/posts", auth(s.createPosts))
	mux.Handle("POST /api/projects/{id}/captions", auth(s.draftCaptions))
	mux.Handle("POST /api/posts/{pid}/approve", admin(s.approvePost))
	mux.Handle("POST /api/posts/{pid}/cancel", auth(s.cancelPost))
	mux.HandleFunc("GET /public/media/{aid}", s.publicMedia) // HMAC-signed, expiring; final videos only
}

func (s *Server) socialStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"platforms": s.Social.Status(), "public_base_url": s.Cfg.PublicBaseURL != "",
		"post_approval": s.Cfg.PostApproval, "encryption": s.Social.Box != nil,
	})
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accs, err := s.Store.ListAccounts(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, accs)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	aid, ok := pathID(w, r, "aid")
	if !ok {
		return
	}
	if err := s.Store.DeleteAccount(r.Context(), aid); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "disconnect", "social_account", aid, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) connectAccount(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	if !socialPlatforms[platform] {
		writeErr(w, 400, "platform tidak dikenal")
		return
	}
	if s.Cfg.PublicBaseURL == "" {
		writeErr(w, 400, "PUBLIC_BASE_URL belum diset (dibutuhkan untuk OAuth redirect)")
		return
	}
	u, err := s.Social.StartConnect(r.Context(), platform)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.audit(r, "connect_start", "social_account", "", map[string]any{"platform": platform})
	writeJSON(w, 200, map[string]string{"url": u})
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	redirect := func(q string) { http.Redirect(w, r, "/#/social?"+q, http.StatusSeeOther) }
	if !socialPlatforms[platform] {
		redirect("error=platform")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.audit(r, "connect_denied", "social_account", "", map[string]any{"platform": platform})
		redirect("error=denied&platform=" + platform)
		return
	}
	code := strings.TrimSuffix(q.Get("code"), "#_")
	acc, err := s.Social.FinishConnect(r.Context(), platform, q.Get("state"), code)
	if err != nil {
		s.audit(r, "connect_failed", "social_account", "", map[string]any{"platform": platform, "error": err.Error()})
		redirect("error=failed&platform=" + platform)
		return
	}
	s.audit(r, "connect", "social_account", acc.ID, map[string]any{"platform": platform, "name": acc.DisplayName})
	redirect("connected=" + platform)
}

// publicMedia serves a final video to platforms that must fetch it (Instagram, Threads).
func (s *Server) publicMedia(w http.ResponseWriter, r *http.Request) {
	aid := r.PathValue("aid")
	if !uuidRe.MatchString(aid) || s.Social.Box == nil || !s.Social.Box.VerifyAsset(aid, r.URL.Query().Get("exp"), r.URL.Query().Get("sig")) {
		writeErr(w, 403, "forbidden")
		return
	}
	a, err := s.Store.GetAsset(r.Context(), aid)
	if err != nil || a.Kind != "final_video" {
		writeErr(w, 404, "not found")
		return
	}
	p, err := s.Media.Path(a.StorageName)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", a.Mime)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	s.Store.Audit(r.Context(), "platform-fetch", clientIP(r), "public_media_fetch", "asset", aid, nil)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	posts, err := s.Store.ListPosts(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, posts)
}

type postTarget struct {
	AccountID string `json:"account_id"`
	Caption   string `json:"caption"`
	Title     string `json:"title"`
	Privacy   string `json:"privacy"`
}

func (s *Server) createPosts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		AssetID     string       `json:"asset_id"`
		Targets     []postTarget `json:"targets"`
		ScheduledAt *time.Time   `json:"scheduled_at"` // null → now
		AIGenerated *bool        `json:"ai_generated"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if !uuidRe.MatchString(body.AssetID) || len(body.Targets) == 0 || len(body.Targets) > 10 {
		writeErr(w, 400, "asset_id dan minimal satu target wajib diisi")
		return
	}
	asset, err := s.Store.GetAsset(r.Context(), body.AssetID)
	if err != nil || asset.ProjectID != id || asset.Kind != "final_video" {
		writeErr(w, 400, "video final tidak ditemukan untuk proyek ini")
		return
	}
	when := time.Now()
	if body.ScheduledAt != nil {
		if body.ScheduledAt.Before(time.Now().Add(-5*time.Minute)) || body.ScheduledAt.After(time.Now().AddDate(0, 6, 0)) {
			writeErr(w, 400, "jadwal harus di masa depan (maks 6 bulan)")
			return
		}
		when = *body.ScheduledAt
	}
	ai := true // default: label AI-generated content (platform policies require disclosure)
	if body.AIGenerated != nil {
		ai = *body.AIGenerated
	}
	status := "scheduled"
	if s.Cfg.PostApproval {
		status = "pending_approval"
	}
	created := []store.Post{}
	for _, t := range body.Targets {
		if !uuidRe.MatchString(t.AccountID) {
			writeErr(w, 400, "account_id tidak valid")
			return
		}
		acc, err := s.Store.GetAccount(r.Context(), t.AccountID)
		if err != nil {
			writeErr(w, 400, "akun tidak ditemukan")
			return
		}
		if len(t.Caption) > 10000 || len(t.Title) > 300 {
			writeErr(w, 400, "caption/judul terlalu panjang")
			return
		}
		if acc.Platform == "threads" && len([]rune(t.Caption)) > 500 {
			writeErr(w, 400, "caption Threads maksimal 500 karakter")
			return
		}
		if acc.Platform == "instagram" && len([]rune(t.Caption)) > 2200 {
			writeErr(w, 400, "caption Instagram maksimal 2200 karakter")
			return
		}
		privacy := t.Privacy
		switch privacy {
		case "public", "private", "unlisted":
		default:
			privacy = "public"
		}
		accID := acc.ID
		p := store.Post{ProjectID: id, AssetID: asset.ID, AccountID: &accID, Platform: acc.Platform, Title: t.Title,
			Caption: t.Caption, Privacy: privacy, AIGenerated: ai, ScheduledAt: when, Status: status, CreatedBy: actorName(r)}
		if err := s.Store.CreatePost(r.Context(), &p); err != nil {
			s.storeErr(w, err)
			return
		}
		s.audit(r, "post_create", "post", p.ID, map[string]any{"platform": p.Platform, "scheduled_at": when, "status": status})
		created = append(created, p)
	}
	writeJSON(w, 201, created)
}

func (s *Server) approvePost(w http.ResponseWriter, r *http.Request) {
	pid, ok := pathID(w, r, "pid")
	if !ok {
		return
	}
	if err := s.Store.ApprovePost(r.Context(), pid, actorName(r)); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.audit(r, "post_approve", "post", pid, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) cancelPost(w http.ResponseWriter, r *http.Request) {
	pid, ok := pathID(w, r, "pid")
	if !ok {
		return
	}
	if err := s.Store.CancelPost(r.Context(), pid); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.audit(r, "post_cancel", "post", pid, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) draftCaptions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	out, err := s.Engine.DraftCaptions(r.Context(), id)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	s.audit(r, "draft_captions", "project", id, nil)
	writeJSON(w, 200, out)
}
