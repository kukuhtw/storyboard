// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"storyboard/internal/auth"
	"storyboard/internal/store"
)

// Account lockout (per user) on top of the per-IP limiter.
const (
	lockThreshold = 5
	lockDuration  = 15 * time.Minute
	ipMaxFailures = 10
	ipWindow      = 15 * time.Minute
)

type ctxKey int

const userKey ctxKey = 1

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func actorName(r *http.Request) string {
	if u := currentUser(r); u != nil {
		return u.Username
	}
	return "anonymous"
}

func (s *Server) audit(r *http.Request, action, entity, id string, detail map[string]any) {
	s.Store.Audit(r.Context(), actorName(r), clientIP(r), action, entity, id, detail)
}

// Endpoints reachable while the user still has to replace a temporary password.
var allowedWhileMustChange = map[string]bool{
	"GET /api/me": true, "POST /api/me/password": true, "GET /api/config": true,
}

func (s *Server) requireAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" || len(c.Value) > 128 {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		u, err := s.Store.SessionUser(r.Context(), auth.HashToken(c.Value), s.Cfg.SessionIdle)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.storeErr(w, err)
				return
			}
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// CSRF defence for cookie-authenticated state changes: require same-origin fetch header.
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "storyboard" {
			writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
			return
		}
		if u.MustChangePassword && !allowedWhileMustChange[r.Method+" "+r.URL.Path] {
			writeErr(w, http.StatusForbidden, "password_change_required")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// adminOnly must wrap a handler that already runs inside requireAuth.
func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := currentUser(r); u == nil || u.Role != store.RoleAdmin {
			s.audit(r, "forbidden", "route", r.Method+" "+r.URL.Path, nil)
			writeErr(w, http.StatusForbidden, "hanya admin")
			return
		}
		h(w, r)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: int(s.Cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	tok, hash := auth.NewSessionToken()
	if err := s.Store.CreateSession(r.Context(), hash, u.ID, clientIP(r), s.Cfg.SessionTTL); err != nil {
		return err
	}
	s.setSessionCookie(w, tok)
	return nil
}

// ---------- per-IP limiter ----------

func (s *Server) ipBlocked(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginFailures == nil {
		s.loginFailures = map[string][]time.Time{}
	}
	recent := s.loginFailures[ip][:0]
	for _, t := range s.loginFailures[ip] {
		if time.Since(t) < ipWindow {
			recent = append(recent, t)
		}
	}
	s.loginFailures[ip] = recent
	return len(recent) >= ipMaxFailures
}

func (s *Server) ipFailed(ip string) {
	s.loginMu.Lock()
	s.loginFailures[ip] = append(s.loginFailures[ip], time.Now())
	s.loginMu.Unlock()
}

// ---------- login / logout / me ----------

type meResp struct {
	*store.User
	IsAdmin bool `json:"is_admin"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") != "storyboard" {
		writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
		return
	}
	ip := clientIP(r)
	if s.ipBlocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "terlalu banyak percobaan login, coba lagi nanti")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Password) > 4*auth.MaxPasswordLen {
		writeErr(w, http.StatusBadRequest, "format salah")
		return
	}
	const generic = "username atau password salah, atau akun terkunci sementara"
	fail := func(u *store.User, reason string) {
		s.ipFailed(ip)
		actor, id := "anonymous", ""
		if u != nil {
			actor, id = u.Username, u.ID
			if reason == "bad_password" {
				s.Store.RecordLoginFailure(r.Context(), u.ID, lockThreshold, lockDuration)
			}
		}
		// The attempted username is not logged when it does not exist (it may be a mistyped password).
		s.Store.Audit(r.Context(), actor, ip, "login_failed", "user", id, map[string]any{"reason": reason})
		writeErr(w, http.StatusUnauthorized, generic)
	}
	username, err := auth.NormalizeUsername(body.Username)
	if err != nil {
		auth.DummyVerify(body.Password)
		fail(nil, "unknown_user")
		return
	}
	u, err := s.Store.GetUserByUsername(r.Context(), username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.storeErr(w, err)
			return
		}
		auth.DummyVerify(body.Password)
		fail(nil, "unknown_user")
		return
	}
	ok := auth.VerifyPassword(u.PasswordHash, body.Password)
	switch {
	case !u.Active:
		fail(u, "inactive")
		return
	case u.LockedUntil != nil && u.LockedUntil.After(time.Now()):
		fail(u, "locked")
		return
	case !ok:
		fail(u, "bad_password")
		return
	}
	if err := s.Store.RecordLoginSuccess(r.Context(), u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.Username, ip, "login", "user", u.ID, nil)
	writeJSON(w, 200, meResp{u, u.Role == store.RoleAdmin})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" && len(c.Value) <= 128 {
		h := auth.HashToken(c.Value)
		if u, err := s.Store.SessionUser(r.Context(), h, s.Cfg.SessionIdle); err == nil {
			s.Store.Audit(r.Context(), u.Username, clientIP(r), "logout", "user", u.ID, nil)
		}
		s.Store.DeleteSession(r.Context(), h)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, 200, meResp{u, u.Role == store.RoleAdmin})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "format salah")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, body.Current) {
		s.Store.RecordLoginFailure(r.Context(), u.ID, lockThreshold, lockDuration)
		s.audit(r, "password_change_failed", "user", u.ID, nil)
		writeErr(w, 400, "password saat ini salah")
		return
	}
	if body.New == body.Current {
		writeErr(w, 400, "password baru harus berbeda")
		return
	}
	if err := auth.ValidatePassword(body.New, u.Username); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	// SetPassword revokes every session (incl. this one); issue a fresh session cookie.
	if err := s.Store.SetPassword(r.Context(), u.ID, hash, false); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "password_changed", "user", u.ID, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- admin: users ----------

type adminUser struct {
	store.User
	Locked   bool `json:"locked"`
	Sessions int  `json:"sessions"`
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	counts, err := s.Store.CountUserSessions(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]adminUser, len(users))
	now := time.Now()
	for i, u := range users {
		out[i] = adminUser{User: u, Locked: u.LockedUntil != nil && u.LockedUntil.After(now), Sessions: counts[u.ID]}
	}
	writeJSON(w, 200, out)
}

func cleanDisplayName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 100 {
		return "", errors.New("nama tampilan maksimal 100 karakter")
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return "", errors.New("nama tampilan berisi karakter tidak valid")
		}
	}
	return s, nil
}

func validRole(role string) bool { return role == store.RoleAdmin || role == store.RoleUser }

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		Password    string `json:"password"` // optional; empty = generate a temporary password
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "format salah")
		return
	}
	username, err := auth.NormalizeUsername(body.Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name, err := cleanDisplayName(body.DisplayName)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !validRole(body.Role) {
		writeErr(w, 400, "role harus admin atau user")
		return
	}
	pw, generated := body.Password, false
	if pw == "" {
		pw, generated = auth.TempPassword(), true
	} else if err := auth.ValidatePassword(pw, username); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	u := &store.User{Username: username, DisplayName: name, Role: body.Role, PasswordHash: hash,
		MustChangePassword: true, CreatedBy: actorName(r)}
	if err := s.Store.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			writeErr(w, 409, err.Error())
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user_create", "user", u.ID, map[string]any{"username": username, "role": body.Role})
	resp := map[string]any{"user": u}
	if generated {
		resp["temp_password"] = pw // shown once to the admin, never stored in plaintext
	}
	writeJSON(w, 201, resp)
}

// guardSelfAndLastAdmin prevents an admin from locking themselves (or everyone) out.
func (s *Server) guardSelfAndLastAdmin(w http.ResponseWriter, r *http.Request, target *store.User, newRole string, newActive bool) bool {
	me := currentUser(r)
	if target.ID == me.ID && (!newActive || newRole != store.RoleAdmin) {
		writeErr(w, 400, "tidak bisa menonaktifkan atau menurunkan role akun sendiri")
		return false
	}
	if target.Role == store.RoleAdmin && target.Active && (!newActive || newRole != store.RoleAdmin) {
		n, err := s.Store.CountOtherActiveAdmins(r.Context(), target.ID)
		if err != nil {
			s.storeErr(w, err)
			return false
		}
		if n == 0 {
			writeErr(w, 400, "harus ada minimal satu admin aktif")
			return false
		}
	}
	return true
}

func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return nil, false
	}
	u, err := s.Store.GetUser(r.Context(), uid)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return u, true
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	var body struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Active      *bool   `json:"active"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "format salah")
		return
	}
	name, role, active := target.DisplayName, target.Role, target.Active
	if body.DisplayName != nil {
		n, err := cleanDisplayName(*body.DisplayName)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		name = n
	}
	if body.Role != nil {
		if !validRole(*body.Role) {
			writeErr(w, 400, "role harus admin atau user")
			return
		}
		role = *body.Role
	}
	if body.Active != nil {
		active = *body.Active
	}
	if !s.guardSelfAndLastAdmin(w, r, target, role, active) {
		return
	}
	if err := s.Store.UpdateUser(r.Context(), target.ID, name, role, active); err != nil {
		s.storeErr(w, err)
		return
	}
	if !active || role != target.Role {
		s.Store.DeleteUserSessions(r.Context(), target.ID, nil) // force re-login with new rights
	}
	s.audit(r, "user_update", "user", target.ID, map[string]any{
		"username": target.Username, "role": role, "active": active,
		"role_changed": role != target.Role, "active_changed": active != target.Active})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.ID == currentUser(r).ID {
		writeErr(w, 400, "gunakan menu Akun Saya untuk mengganti password sendiri")
		return
	}
	pw := auth.TempPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), target.ID, hash, true); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user_password_reset", "user", target.ID, map[string]any{"username": target.Username})
	writeJSON(w, 200, map[string]string{"temp_password": pw})
}

func (s *Server) adminUnlockUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if err := s.Store.UnlockUser(r.Context(), target.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user_unlock", "user", target.ID, map[string]any{"username": target.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	var keep []byte
	if target.ID == currentUser(r).ID {
		if c, err := r.Cookie(sessionCookie); err == nil {
			keep = auth.HashToken(c.Value) // keep the admin's own current session
		}
	}
	if err := s.Store.DeleteUserSessions(r.Context(), target.ID, keep); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user_sessions_revoked", "user", target.ID, map[string]any{"username": target.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	actor := strings.TrimSpace(q.Get("actor"))
	action := strings.TrimSpace(q.Get("action"))
	if len(actor) > 50 || len(action) > 50 {
		writeErr(w, 400, "filter terlalu panjang")
		return
	}
	entries, err := s.Store.ListAudit(r.Context(), actor, action, limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "audit_view", "audit_log", "", map[string]any{"actor": actor, "action": action})
	writeJSON(w, 200, entries)
}
