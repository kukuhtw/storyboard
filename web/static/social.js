// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

"use strict";
// Social publishing UI: account connections (#/social) and the per-project publish panel.
// Uses helpers h(), api(), guard(), toast(), $app() from app.js (loaded after this file;
// these functions are only called at runtime).

const PLATFORM_META = {
  youtube: { label: "YouTube Shorts", icon: "▶", privacy: ["public", "unlisted", "private"], limit: 5000, titled: true },
  instagram: { label: "Instagram Reels", icon: "◎", privacy: ["public"], limit: 2200 },
  threads: { label: "Threads", icon: "@", privacy: ["public"], limit: 500 },
  tiktok: { label: "TikTok", icon: "♪", privacy: ["public", "private"], limit: 2200 },
};
const PRIVACY_LABEL = { public: "Publik", unlisted: "Unlisted", private: "Privat" };
const POST_STATUS = {
  pending_approval: ["Menunggu persetujuan", "review"], scheduled: ["Terjadwal", "ready"], publishing: ["Mengunggah…", "generating"],
  published: ["Terbit", "approved"], failed: ["Gagal", "locked"], cancelled: ["Dibatalkan", "locked"],
};

// ---------- #/social: connected accounts ----------
async function renderSocialPage() {
  const q = new URLSearchParams(location.hash.split("?")[1] || "");
  const wrap = h("div", {}, h("div", { class: "muted" }, "Memuat…"));
  $app().replaceChildren(
    h("div", { class: "card" },
      h("h1", {}, "Akun Sosial Media"),
      h("p", { class: "muted" }, "Hubungkan akun resmi untuk publikasi video final. Token akses disimpan terenkripsi (AES-256-GCM) dan bisa diputus kapan saja."),
      q.get("connected") ? h("div", { class: "notice" }, `✓ Akun ${PLATFORM_META[q.get("connected")]?.label || ""} terhubung.`) : null,
      q.get("error") ? h("div", { class: "error" }, `Koneksi ${PLATFORM_META[q.get("platform")]?.label || ""} gagal (${q.get("error")}). Lihat audit log / coba lagi.`) : null),
    wrap);
  try {
    const [status, accounts] = await Promise.all([api("GET", "/api/social/status"), api("GET", "/api/social/accounts")]);
    const isAdmin = !!state.me?.is_admin;
    const warn = [];
    if (!isAdmin) warn.push("Hanya admin yang bisa menghubungkan atau memutus akun.");
    if (!status.encryption) warn.push("TOKEN_ENC_KEY belum diset: publikasi nonaktif.");
    if (!status.public_base_url) warn.push("PUBLIC_BASE_URL belum diset: OAuth & Instagram/Threads tidak bisa dipakai.");
    setKids(wrap,
      warn.length ? h("div", { class: "notice", style: "margin-bottom:16px" }, warn.join(" ")) : null,
      h("div", { class: "grid cols-2" }, ...Object.entries(PLATFORM_META).map(([key, meta]) => {
        const mine = accounts.filter((a) => a.platform === key);
        const btn = h("button", { class: "secondary", disabled: !status.platforms[key] }, mine.length ? "+ Hubungkan akun lain" : "Hubungkan");
        btn.onclick = () => guard(async () => { const r = await api("POST", `/api/social/${key}/connect`); location.href = r.url; }, btn);
        return h("div", { class: "card" },
          h("h3", {}, `${meta.icon} ${meta.label}`),
          !status.platforms[key] ? h("div", { class: "muted small" }, "Belum dikonfigurasi admin (client id/secret).") : null,
          ...mine.map((a) => h("div", { class: "row", style: "padding:6px 0;border-bottom:1px solid var(--border)" },
            h("b", {}, a.display_name), h("span", { class: "muted small spacer" },
              a.expires_at ? ` · token s/d ${new Date(a.expires_at).toLocaleDateString()}` : ""),
            !isAdmin ? null : h("button", { class: "ghost", onclick: (e) => guard(async () => {
              if (!confirm(`Putuskan ${a.display_name}? Token dihapus dan post terjadwal untuk akun ini dibatalkan.`)) return;
              await api("DELETE", `/api/social/accounts/${a.id}`); renderSocialPage();
            }, e.target) }, "Putuskan"))),
          isAdmin ? h("div", { style: "margin-top:10px" }, btn) : null);
      })));
  } catch (e) { wrap.replaceChildren(h("div", { class: "error" }, e.message)); }
}

// ---------- project publish panel ----------
function publishSection(project, finalAsset) {
  const box = h("div", { class: "card", id: "publish" }, h("h2", {}, "📣 Publikasi"), h("div", { class: "muted" }, "Memuat…"));
  loadPublish(box, project, finalAsset);
  return box;
}

async function loadPublish(box, project, finalAsset) {
  let accounts, posts, status;
  try {
    [accounts, posts, status] = await Promise.all([
      api("GET", "/api/social/accounts"), api("GET", `/api/projects/${project.id}/posts`), api("GET", "/api/social/status")]);
  } catch (e) { box.replaceChildren(h("h2", {}, "📣 Publikasi"), h("div", { class: "error" }, e.message)); return; }

  const header = h("div", { class: "row" }, h("h2", { class: "spacer" }, "📣 Publikasi"), h("a", { href: "#/social", class: "small" }, "Kelola akun"));
  const dur = state.bundle.stages.find((s) => s.kind === "render")?.output?.duration_sec || 0;
  const checks = [];
  if (project.aspect_ratio !== "9:16") checks.push("Rasio proyek bukan 9:16. Shorts/Reels/TikTok paling optimal vertikal.");
  if (dur > 180) checks.push(`Durasi ${dur} dtk: YouTube Shorts maks ±3 menit, Threads maks 5 menit.`);

  if (!accounts.length) {
    box.replaceChildren(header, h("div", { class: "notice" }, "Belum ada akun terhubung. ", h("a", { href: "#/social" }, "Hubungkan akun sosial media"), " terlebih dahulu."), postsTable(posts, box, project, finalAsset));
    return;
  }

  // per-account target rows
  const rows = accounts.map((a) => {
    const meta = PLATFORM_META[a.platform];
    const on = h("input", { type: "checkbox", style: "width:auto" });
    const title = meta.titled ? h("input", { maxlength: 100, placeholder: "Judul YouTube (maks 100)" }) : null;
    const caption = h("textarea", { rows: 3, maxlength: meta.limit, placeholder: `Caption (maks ${meta.limit} karakter)` });
    const counter = h("span", { class: "muted small" }, `0/${meta.limit}`);
    caption.addEventListener("input", () => { counter.textContent = `${[...caption.value].length}/${meta.limit}`; });
    const privacy = h("select", { style: "width:auto" }, ...meta.privacy.map((p) => h("option", { value: p }, PRIVACY_LABEL[p])));
    const body = h("div", { class: "grid", style: "margin-top:8px" }, title, caption, h("div", { class: "row" }, h("label", { style: "margin:0" }, "Visibilitas"), privacy, h("span", { class: "spacer" }), counter));
    body.style.display = "none";
    on.onchange = () => { body.style.display = on.checked ? "" : "none"; };
    const el = h("div", { class: "scene" }, h("label", { class: "row", style: "color:var(--text);margin:0;font-size:14px" }, on, h("b", {}, `${meta.icon} ${meta.label}`), h("span", { class: "muted" }, a.display_name)), body);
    return { a, on, title, caption, privacy, el, counter };
  });

  const draftBtn = h("button", { class: "ghost" }, "✨ Buat draf caption dengan Claude");
  draftBtn.onclick = () => guard(async () => {
    const c = await api("POST", `/api/projects/${project.id}/captions`);
    for (const r of rows) {
      const map = { youtube: c.youtube_description, instagram: c.instagram, threads: c.threads, tiktok: c.tiktok };
      r.caption.value = map[r.a.platform] || "";
      r.caption.dispatchEvent(new Event("input"));
      if (r.title) r.title.value = c.youtube_title || "";
    }
    toast("Draf caption terisi. Review sebelum publikasi.");
  }, draftBtn);

  const mode = h("select", { style: "width:auto" }, h("option", { value: "now" }, "Sekarang"), h("option", { value: "schedule" }, "Jadwalkan"));
  const when = h("input", { type: "datetime-local", style: "width:auto", disabled: true });
  mode.onchange = () => { when.disabled = mode.value !== "schedule"; };
  const ai = h("input", { type: "checkbox", checked: true, style: "width:auto" });
  const submit = h("button", {}, "Kirim ke antrean publikasi");
  submit.onclick = () => guard(async () => {
    const targets = rows.filter((r) => r.on.checked).map((r) => ({ account_id: r.a.id, caption: r.caption.value, title: r.title ? r.title.value : "", privacy: r.privacy.value }));
    if (!targets.length) throw new Error("Pilih minimal satu akun tujuan");
    let scheduled_at = null;
    if (mode.value === "schedule") {
      if (!when.value) throw new Error("Isi tanggal & jam jadwal");
      scheduled_at = new Date(when.value).toISOString(); // local time → UTC
    }
    const names = targets.length + " akun";
    if (!confirm(`Publikasikan video final ke ${names} ${scheduled_at ? "pada " + new Date(scheduled_at).toLocaleString() : "sekarang"}?` +
      (status.post_approval ? "\n\nPost akan menunggu persetujuan admin terlebih dahulu." : ""))) return;
    await api("POST", `/api/projects/${project.id}/posts`, { asset_id: finalAsset.id, targets, scheduled_at, ai_generated: ai.checked });
    toast(status.post_approval ? "Masuk antrean, menunggu persetujuan admin." : "Masuk antrean publikasi.");
    loadPublish(box, project, finalAsset);
  }, submit);

  setKids(box, header,
    checks.length ? h("div", { class: "notice small" }, checks.join(" ")) : null,
    h("div", { class: "row", style: "margin:8px 0" }, draftBtn),
    h("div", { class: "grid cols-2" }, ...rows.map((r) => r.el)),
    h("div", { class: "row", style: "margin-top:12px" },
      h("label", { style: "margin:0" }, "Waktu"), mode, when,
      h("label", { class: "row", style: "margin:0;color:var(--text)" }, ai, "Label konten dibuat AI"),
      h("span", { class: "spacer" }), submit),
    h("p", { class: "muted small" }, "Label AI dikirim ke tiap platform (YouTube containsSyntheticMedia, TikTok is_aigc, Instagram is_ai_generated, Threads is_made_with_ai). Kebijakan platform mewajibkan pengungkapan konten AI yang realistis."),
    postsTable(posts, box, project, finalAsset));

  if (posts.some((p) => ["scheduled", "publishing"].includes(p.status) && new Date(p.scheduled_at) < new Date(Date.now() + 120000))) {
    clearTimeout(box._t);
    box._t = setTimeout(() => { if (document.body.contains(box)) loadPublish(box, project, finalAsset); }, 10000);
  }
}

function postsTable(posts, box, project, finalAsset) {
  if (!posts.length) return h("div", { class: "muted small", style: "margin-top:12px" }, "Belum ada riwayat publikasi.");
  const act = (label, path, cls, ask) => (e) => guard(async () => {
    if (ask && !confirm(ask)) return;
    await api("POST", path); loadPublish(box, project, finalAsset);
  }, e.target);
  return h("div", { style: "margin-top:16px;overflow-x:auto" },
    h("h3", {}, "Riwayat & antrean"),
    h("table", { style: "width:100%;border-collapse:collapse;font-size:13px" },
      h("thead", {}, h("tr", {}, ...["Platform", "Akun", "Jadwal", "Status", "Oleh", "Hasil", ""].map((t) => h("th", { style: "text-align:left;padding:6px;border-bottom:1px solid var(--border)" }, t)))),
      h("tbody", {}, ...posts.map((p) => {
        const [label, cls] = POST_STATUS[p.status] || [p.status, ""];
        return h("tr", {},
          h("td", { style: "padding:6px" }, PLATFORM_META[p.platform]?.label || p.platform),
          h("td", { style: "padding:6px" }, p.account_name || "—"),
          h("td", { style: "padding:6px" }, new Date(p.scheduled_at).toLocaleString()),
          h("td", { style: "padding:6px" }, h("span", { class: "badge " + cls }, label), p.attempts > 1 ? h("span", { class: "muted small" }, ` (percobaan ${p.attempts})`) : null),
          h("td", { style: "padding:6px", class: "small" }, p.created_by || "—", p.approved_by ? h("div", { class: "muted" }, "✓ " + p.approved_by) : null),
          h("td", { style: "padding:6px;max-width:320px" },
            p.external_url ? h("a", { href: p.external_url, target: "_blank", rel: "noopener noreferrer" }, "Lihat post ↗") : null,
            p.note ? h("div", { class: "muted small" }, p.note) : null,
            p.error ? h("div", { class: "error small" }, p.error) : null),
          h("td", { style: "padding:6px;white-space:nowrap" },
            p.status === "pending_approval" && state.me?.is_admin ? h("button", { class: "secondary", onclick: act("", `/api/posts/${p.id}/approve`, "", "Setujui publikasi ini ke akun resmi?") }, "Setujui") : null, " ",
            ["pending_approval", "scheduled"].includes(p.status) ? h("button", { class: "ghost", onclick: act("", `/api/posts/${p.id}/cancel`, "", "Batalkan post ini?") }, "Batal") : null));
      }))));
}
