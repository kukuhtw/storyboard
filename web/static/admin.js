// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

"use strict";
// Admin dashboard (users + audit log) and "Akun Saya" (change password).
// Uses h(), api(), guard(), toast(), setKids(), state from app.js (loaded after this file).

const ROLE_LABEL = { admin: "Admin", user: "User" };
const fmtTime = (t) => (t ? new Date(t).toLocaleString() : "—");

function userStatus(u) {
  if (!u.active) return ["Nonaktif", "locked"];
  if (u.locked) return ["Terkunci", "review"];
  if (u.must_change_password) return ["Wajib ganti password", "ready"];
  return ["Aktif", "approved"];
}

// Shows a one-time temporary password with a copy button.
function tempPasswordCard(username, pw) {
  const code = h("code", { class: "temp-pw" }, pw);
  const copy = h("button", { class: "ghost", type: "button" }, "Salin");
  copy.onclick = () => guard(async () => { await navigator.clipboard.writeText(pw); toast("Password sementara disalin"); }, copy);
  return h("div", { class: "notice temp-box" },
    h("div", {}, h("b", {}, `Password sementara untuk ${username}`)),
    h("div", { class: "row", style: "margin:8px 0" }, code, copy),
    h("div", { class: "small muted" }, "Hanya ditampilkan sekali dan tidak disimpan dalam bentuk asli. Serahkan langsung ke pengguna lewat kanal aman (bukan grup chat/email terbuka). Pengguna wajib menggantinya saat login pertama."));
}

// ---------- #/admin ----------
async function renderAdminPage() {
  if (!state.me?.is_admin) { $app().replaceChildren(h("div", { class: "card error" }, "Halaman ini hanya untuk admin.")); return; }
  const tab = location.hash.startsWith("#/admin/audit") ? "audit" : "users";
  const tabs = h("div", { class: "tabs" },
    h("a", { href: "#/admin", class: tab === "users" ? "active" : "" }, "👥 Pengguna"),
    h("a", { href: "#/admin/audit", class: tab === "audit" ? "active" : "" }, "📜 Audit log"));
  const body = h("div", {}, h("div", { class: "muted" }, "Memuat…"));
  $app().replaceChildren(h("div", { class: "card" }, h("h1", {}, "Dashboard Admin"),
    h("p", { class: "muted" }, "Kelola akun pengguna dan pantau aktivitas (ISO/IEC 27001 A.5.15–A.5.18, A.8.15)."), tabs), body);
  if (tab === "audit") return renderAuditTab(body);
  return renderUsersTab(body);
}

async function renderUsersTab(body) {
  let users;
  try { users = await api("GET", "/api/admin/users"); } catch (e) { body.replaceChildren(h("div", { class: "error card" }, e.message)); return; }
  const result = h("div", {});

  // create form
  const username = h("input", { required: true, maxlength: 50, placeholder: "mis. budi.santoso", autocomplete: "off" });
  const name = h("input", { maxlength: 100, placeholder: "mis. Budi Santoso" });
  const role = h("select", {}, h("option", { value: "user" }, "User: membuat & mengelola proyek"), h("option", { value: "admin" }, "Admin: + kelola pengguna, akun sosial, persetujuan post"));
  const mode = h("select", {}, h("option", { value: "temp" }, "Buat password sementara otomatis (disarankan)"), h("option", { value: "manual" }, "Tentukan password awal"));
  const pw = h("input", { type: "password", minlength: 12, maxlength: 128, placeholder: "min. 12 karakter", autocomplete: "new-password", disabled: true });
  mode.onchange = () => { pw.disabled = mode.value !== "manual"; if (pw.disabled) pw.value = ""; };
  const btn = h("button", { type: "submit" }, "Tambah pengguna");
  const form = h("form", { class: "card", onsubmit: (e) => {
    e.preventDefault();
    guard(async () => {
      const r = await api("POST", "/api/admin/users", { username: username.value, display_name: name.value, role: role.value, password: mode.value === "manual" ? pw.value : "" });
      toast(`Pengguna ${r.user.username} dibuat`);
      const created = r.user.username;
      await renderUsersTab(body);
      if (r.temp_password) body.querySelector("#create-result").replaceChildren(tempPasswordCard(created, r.temp_password));
      else body.querySelector("#create-result").replaceChildren(h("div", { class: "notice" }, `Pengguna ${created} dibuat dengan password yang Anda tentukan. Pengguna wajib menggantinya saat login pertama.`));
    }, btn);
  } },
    h("h2", {}, "Tambah pengguna"),
    h("div", { class: "grid cols-2" },
      h("div", {}, h("label", {}, "Username"), username, h("div", { class: "small muted" }, "Huruf kecil, angka, titik, minus. Tidak bisa diubah.")),
      h("div", {}, h("label", {}, "Nama tampilan"), name),
      h("div", {}, h("label", {}, "Role"), role),
      h("div", {}, h("label", {}, "Password awal"), mode, h("div", { style: "height:6px" }), pw)),
    h("div", { class: "row", style: "margin-top:12px" }, btn, h("span", { class: "small muted" }, "Prinsip least privilege: beri role Admin hanya bila diperlukan.")));

  // users table
  const rows = users.map((u) => {
    const isMe = u.id === state.me.id;
    const [label, cls] = userStatus(u);
    const nameIn = h("input", { value: u.display_name, maxlength: 100, style: "min-width:140px" });
    const roleSel = h("select", { disabled: isMe, style: "width:auto" }, ...Object.entries(ROLE_LABEL).map(([v, l]) => h("option", { value: v, selected: v === u.role }, l)));
    const save = h("button", { class: "ghost" }, "Simpan");
    save.onclick = () => guard(async () => {
      if (roleSel.value !== u.role && !confirm(`Ubah role ${u.username} menjadi ${ROLE_LABEL[roleSel.value]}? Sesi aktif pengguna akan dicabut.`)) return;
      await api("PUT", `/api/admin/users/${u.id}`, { display_name: nameIn.value, role: roleSel.value });
      toast("Tersimpan"); renderUsersTab(body);
    }, save);
    const reset = isMe ? null : h("button", { class: "ghost" }, "Reset password");
    if (reset) reset.onclick = () => guard(async () => {
      if (!confirm(`Reset password ${u.username}? Password lama tidak berlaku dan semua sesinya dicabut.`)) return;
      const r = await api("POST", `/api/admin/users/${u.id}/reset-password`);
      await renderUsersTab(body);
      body.querySelector("#create-result").replaceChildren(tempPasswordCard(u.username, r.temp_password));
      body.querySelector("#create-result").scrollIntoView({ behavior: "smooth", block: "center" });
    }, reset);
    const unlock = u.locked ? h("button", { class: "secondary" }, "Buka kunci") : null;
    if (unlock) unlock.onclick = () => guard(async () => { await api("POST", `/api/admin/users/${u.id}/unlock`); toast("Akun dibuka"); renderUsersTab(body); }, unlock);
    const revoke = u.sessions > 0 ? h("button", { class: "ghost", title: "Paksa logout dari semua perangkat" }, isMe ? "Logout perangkat lain" : "Paksa logout") : null;
    if (revoke) revoke.onclick = () => guard(async () => { await api("POST", `/api/admin/users/${u.id}/revoke-sessions`); toast("Sesi dicabut"); renderUsersTab(body); }, revoke);
    const toggle = isMe ? null : h("button", { class: u.active ? "danger" : "secondary" }, u.active ? "Nonaktifkan" : "Aktifkan");
    if (toggle) toggle.onclick = () => guard(async () => {
      if (u.active && !confirm(`Nonaktifkan ${u.username}? Pengguna langsung dikeluarkan dan tidak bisa login. Data & audit log tetap tersimpan.`)) return;
      await api("PUT", `/api/admin/users/${u.id}`, { active: !u.active });
      toast(u.active ? "Pengguna dinonaktifkan" : "Pengguna diaktifkan"); renderUsersTab(body);
    }, toggle);
    return h("tr", { class: u.active ? "" : "inactive" },
      h("td", {}, h("b", {}, u.username), isMe ? h("span", { class: "muted small" }, " (Anda)") : null),
      h("td", {}, nameIn),
      h("td", {}, roleSel),
      h("td", {}, h("span", { class: "badge " + cls }, label), u.failed_attempts > 0 && !u.locked ? h("div", { class: "small muted" }, `${u.failed_attempts}× gagal`) : null),
      h("td", { class: "small" }, fmtTime(u.last_login_at)),
      h("td", { class: "small" }, String(u.sessions)),
      h("td", { class: "small muted" }, u.created_by || "—", h("br"), fmtTime(u.created_at)),
      h("td", { class: "actions" }, save, reset, unlock, revoke, toggle));
  });
  const table = h("div", { class: "card" },
    h("div", { class: "row" }, h("h2", { class: "spacer" }, `Pengguna (${users.length})`),
      h("span", { class: "small muted" }, `${users.filter((u) => u.active).length} aktif · ${users.filter((u) => u.role === "admin" && u.active).length} admin`)),
    h("div", { class: "tbl-wrap" }, h("table", { class: "tbl" },
      h("thead", {}, h("tr", {}, ...["Username", "Nama", "Role", "Status", "Login terakhir", "Sesi", "Dibuat oleh", "Aksi"].map((t) => h("th", {}, t)))),
      h("tbody", {}, ...rows))),
    h("p", { class: "small muted" }, "Pengguna tidak dihapus, hanya dinonaktifkan, agar jejak audit tetap utuh. Akun terkunci otomatis 15 menit setelah 5 kali salah password."));
  result.id = "create-result";
  body.replaceChildren(form, result, table);
}

async function renderAuditTab(body) {
  const actor = h("input", { placeholder: "username / scheduler / cli", maxlength: 50, style: "width:auto" });
  const action = h("select", { style: "width:auto" }, ...["", "login", "login_failed", "logout", "password_changed", "password_change_failed", "forbidden", "user_create", "user_update", "user_password_reset", "user_unlock", "user_sessions_revoked", "audit_view",
    "create", "update", "delete", "export", "upload_reference", "run_stage", "approve_stage", "reopen_stage", "edit_scene", "connect", "disconnect", "post_create", "post_approve", "post_cancel", "published", "publish_failed", "public_media_fetch"]
    .map((a) => h("option", { value: a }, a || "Semua aksi")));
  const list = h("div", {}, h("div", { class: "muted" }, "Memuat…"));
  const load = () => guard(async () => {
    const q = new URLSearchParams({ limit: "300" });
    if (actor.value.trim()) q.set("actor", actor.value.trim());
    if (action.value) q.set("action", action.value);
    const rows = await api("GET", "/api/admin/audit?" + q);
    list.replaceChildren(rows.length ? h("div", { class: "tbl-wrap" }, h("table", { class: "tbl" },
      h("thead", {}, h("tr", {}, ...["Waktu", "Pelaku", "Aksi", "Objek", "IP", "Detail"].map((t) => h("th", {}, t)))),
      h("tbody", {}, ...rows.map((e) => h("tr", { class: e.action === "login_failed" || e.action === "forbidden" ? "warnrow" : "" },
        h("td", { class: "small", style: "white-space:nowrap" }, fmtTime(e.at)),
        h("td", {}, h("b", {}, e.actor)),
        h("td", {}, h("code", {}, e.action)),
        h("td", { class: "small" }, e.entity, e.entity_id ? h("span", { class: "muted" }, " " + e.entity_id.slice(0, 8)) : null),
        h("td", { class: "small muted" }, e.remote_addr || "—"),
        h("td", { class: "small muted detail" }, e.detail && Object.keys(e.detail).length ? JSON.stringify(e.detail) : "")))))) : h("div", { class: "muted" }, "Tidak ada catatan."));
  });
  const btn = h("button", { class: "secondary" }, "Terapkan filter");
  btn.onclick = load;
  body.replaceChildren(h("div", { class: "card" },
    h("div", { class: "row" }, h("h2", { class: "spacer" }, "Audit log"), h("label", { style: "margin:0" }, "Pelaku"), actor, h("label", { style: "margin:0" }, "Aksi"), action, btn),
    h("p", { class: "small muted" }, "300 catatan terbaru. Audit log tidak memuat password, token, atau isi file. Melihat audit log juga tercatat."),
    list));
  load();
}

// ---------- #/account ----------
function renderAccountPage(forced) {
  const me = state.me;
  const cur = h("input", { type: "password", autocomplete: "current-password", required: true });
  const nw = h("input", { type: "password", autocomplete: "new-password", minlength: 12, maxlength: 128, required: true });
  const nw2 = h("input", { type: "password", autocomplete: "new-password", required: true });
  const meter = h("div", { class: "small muted" }, "Minimal 12 karakter. Disarankan kalimat sandi, mis. 4 kata acak.");
  nw.addEventListener("input", () => {
    const n = [...nw.value].length;
    meter.textContent = n < 12 ? `${n}/12 karakter, belum cukup` : n < 16 ? `${n} karakter: cukup` : `${n} karakter: kuat`;
    meter.style.color = n < 12 ? "var(--err)" : "var(--ok)";
  });
  const btn = h("button", { type: "submit" }, "Simpan password baru");
  const form = h("form", { class: "card", style: "max-width:520px", onsubmit: (e) => {
    e.preventDefault();
    guard(async () => {
      if (nw.value !== nw2.value) throw new Error("Konfirmasi password tidak sama");
      await api("POST", "/api/me/password", { current_password: cur.value, new_password: nw.value });
      cur.value = nw.value = nw2.value = "";
      toast("Password diganti. Sesi di perangkat lain telah dikeluarkan.");
      if (forced) boot(); else renderAccountPage(false);
    }, btn);
  } },
    h("h2", {}, forced ? "Ganti password sementara" : "Ganti password"),
    forced ? h("div", { class: "notice", style: "margin-bottom:12px" }, "Akun Anda memakai password sementara. Buat password baru sebelum melanjutkan.") : null,
    h("label", {}, forced ? "Password sementara" : "Password saat ini"), cur,
    h("label", {}, "Password baru"), nw, meter,
    h("label", {}, "Ulangi password baru"), nw2,
    h("div", { style: "height:12px" }), btn,
    h("p", { class: "small muted" }, "Password tidak boleh memuat username dan tidak boleh password umum. Mengganti password akan mengeluarkan sesi Anda di perangkat lain."));
  const profile = h("div", { class: "card", style: "max-width:520px" },
    h("h1", {}, "Akun Saya"),
    h("table", { class: "kv" },
      h("tr", {}, h("td", {}, "Username"), h("td", {}, h("b", {}, me.username))),
      h("tr", {}, h("td", {}, "Nama"), h("td", {}, me.display_name || "—")),
      h("tr", {}, h("td", {}, "Role"), h("td", {}, ROLE_LABEL[me.role])),
      h("tr", {}, h("td", {}, "Login terakhir"), h("td", {}, fmtTime(me.last_login_at))),
      h("tr", {}, h("td", {}, "Password diganti"), h("td", {}, fmtTime(me.password_changed_at)))));
  setKids($app(), forced ? null : profile, form);
  (forced ? cur : null)?.focus();
}
