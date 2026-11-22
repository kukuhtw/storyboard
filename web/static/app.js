// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

"use strict";
// Storyboard Studio frontend — vanilla JS, no build step.
// All user/AI text is inserted with textContent (never innerHTML) to prevent XSS.

const STAGE_META = {
  storyboard: { title: "Storyboard", desc: "Konsep, gaya visual, karakter & daftar scene dari Claude." },
  voiceover: { title: "Voice Over", desc: "Naskah narasi/dialog per scene." },
  audio: { title: "Audio VO", desc: "Text-to-speech dari naskah voice over." },
  prompts: { title: "Prompt Gambar & Video", desc: "Prompt AI per scene (bahasa Inggris)." },
  images: { title: "Gambar", desc: "Keyframe per scene dari provider gambar." },
  videos: { title: "Video", desc: "Klip per scene (image-to-video)." },
  render: { title: "Render Final", desc: "Gabungkan semua klip + audio VO menjadi satu video MP4 + subtitle SRT." },
};
const STYLE_LABEL = { photo: "Foto realistis", cinematic: "Sinematik", documentary: "Foto dokumenter", illustration: "Ilustrasi", "3d": "3D render" };
const styleSelect = (cur) => h("select", {}, ...Object.entries(STYLE_LABEL).map(([v, l]) => h("option", { value: v, selected: v === cur }, l)));
const TYPE_LABEL = { documentation: "Dokumentasi", advertisement: "Iklan", drama: "Drama" };
const STATUS_LABEL = { locked: "Terkunci", ready: "Siap", generating: "Memproses…", review: "Perlu review", approved: "Disetujui" };

const state = { me: null, config: null, bundle: null, activeStage: null, pollTimer: null };

// ---------- helpers ----------
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
const $app = () => document.getElementById("app");
// replaceChildren() would stringify null/false into text nodes, so filter them first.
const setKids = (el, ...kids) => el.replaceChildren(...kids.flat().filter((c) => c != null && c !== false));

async function api(method, path, body, isForm) {
  const opts = { method, headers: { "X-Requested-With": "storyboard" }, credentials: "same-origin" };
  if (body !== undefined) {
    if (isForm) opts.body = body;
    else { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  }
  const res = await fetch(path, opts);
  if (res.status === 401 && path !== "/api/login") { renderLogin(); throw new Error("Sesi berakhir, silakan login"); }
  const data = await res.json().catch(() => ({}));
  if (res.status === 403 && data.error === "password_change_required") { renderAccountPage(true); throw new Error("Ganti password sementara terlebih dahulu"); }
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

function toast(msg, isErr) {
  const t = document.getElementById("toast");
  t.textContent = msg;
  t.className = "toast" + (isErr ? " err" : "");
  clearTimeout(t._timer);
  t._timer = setTimeout(() => t.classList.add("hidden"), isErr ? 7000 : 3000);
}

async function guard(fn, btn) {
  if (btn) btn.disabled = true;
  try { return await fn(); }
  catch (e) { toast(e.message, true); }
  finally { if (btn) btn.disabled = false; }
}

function badge(status) { return h("span", { class: "badge " + status }, STATUS_LABEL[status] || status); }
function ratioClass() {
  const r = state.bundle?.project.aspect_ratio;
  return { "9:16": "v916", "1:1": "v11", "4:5": "v45" }[r] || "";
}
function asset(id) { return state.bundle?.assets.find((a) => a.id === id); }

function mediaEl(a, cls) {
  if (!a) return null;
  const src = "/media/" + a.id;
  if (a.mime.startsWith("video/")) return h("video", { class: cls, src, controls: true, preload: "metadata" });
  if (a.mime.startsWith("audio/")) return h("audio", { src, controls: true, preload: "metadata" });
  return h("img", { class: cls, src, alt: a.kind, loading: "lazy" });
}

// ---------- routing ----------
function route() {
  clearInterval(state.pollTimer);
  if (location.hash.startsWith("#/social")) { renderSocialPage(); return; }
  if (location.hash.startsWith("#/admin")) { renderAdminPage(); return; }
  if (location.hash.startsWith("#/account")) { renderAccountPage(false); return; }
  const m = location.hash.match(/^#\/p\/([0-9a-f-]{36})(?:\/(\w+))?/i);
  if (m) { state.activeStage = m[2] || null; renderProject(m[1]); }
  else renderHome();
}
window.addEventListener("hashchange", route);

// ---------- login ----------
function renderLogin() {
  clearInterval(state.pollTimer);
  state.me = null;
  document.getElementById("userNav").classList.add("hidden");
  const user = h("input", { autocomplete: "username", required: true, maxlength: 50, autocapitalize: "none", spellcheck: "false" });
  const pass = h("input", { type: "password", autocomplete: "current-password", required: true, maxlength: 512 });
  const btn = h("button", { type: "submit" }, "Masuk");
  const form = h("form", { class: "card login", onsubmit: (e) => {
    e.preventDefault();
    guard(async () => { await api("POST", "/api/login", { username: user.value, password: pass.value }); pass.value = ""; boot(); }, btn);
  } },
    h("h1", {}, "Masuk"),
    h("p", { class: "muted" }, "Gunakan username dan password dari admin."),
    h("label", {}, "Username"), user,
    h("label", {}, "Password"), pass,
    h("div", { style: "height:14px" }), btn,
    h("p", { class: "small muted", style: "margin-bottom:0" }, "Lupa password atau akun terkunci? Hubungi admin untuk reset. Akun terkunci 15 menit setelah 5 kali salah."));
  $app().replaceChildren(form);
  user.focus();
}

// ---------- home ----------
async function renderHome() {
  const list = h("div", { class: "plist" }, h("div", { class: "muted" }, "Memuat…"));
  const title = h("input", { required: true, maxlength: 200, placeholder: "mis. Tutorial Klaim Asuransi Online" });
  const type = h("select", {}, ...Object.entries(TYPE_LABEL).map(([v, l]) => h("option", { value: v }, l)));
  const dur = h("input", { type: "number", min: 5, max: 1800, value: 60 });
  const ratio = h("select", {}, ...["16:9", "9:16", "1:1", "4:5"].map((r) => h("option", { value: r }, r)));
  const lang = h("select", {}, h("option", { value: "id" }, "Bahasa Indonesia"), h("option", { value: "en" }, "English"));
  const tone = h("input", { maxlength: 500, placeholder: "mis. hangat, profesional, sinematik" });
  const style = styleSelect("photo");
  const brief = h("textarea", { rows: 6, maxlength: 20000, placeholder: "Ceritakan tujuan video, audiens, pesan utama, produk, alur cerita, dll." });
  const btn = h("button", { type: "submit" }, "Buat Proyek");
  const form = h("form", { class: "card", onsubmit: (e) => {
    e.preventDefault();
    guard(async () => {
      const p = await api("POST", "/api/projects", { title: title.value, video_type: type.value, target_duration_sec: +dur.value, aspect_ratio: ratio.value, language: lang.value, tone: tone.value, image_style: style.value, brief: brief.value });
      location.hash = "#/p/" + p.id;
    }, btn);
  } },
    h("h2", {}, "Proyek Video Baru"),
    h("div", { class: "grid cols-2" },
      h("div", {}, h("label", {}, "Judul"), title),
      h("div", {}, h("label", {}, "Jenis video"), type),
      h("div", {}, h("label", {}, "Durasi target (detik)"), dur),
      h("div", {}, h("label", {}, "Rasio"), ratio),
      h("div", {}, h("label", {}, "Bahasa konten"), lang),
      h("div", {}, h("label", {}, "Tone / gaya"), tone),
      h("div", {}, h("label", {}, "Gaya visual gambar/video"), style)),
    h("div", { style: "margin-top:12px" }, h("label", {}, "Brief"), brief),
    h("p", { class: "notice small" }, "⚠️ Perlindungan data: jangan masukkan data pribadi nasabah/karyawan (nama lengkap, NIK, no. polis, no. HP, alamat) ke brief. Brief dikirim ke Claude API (Anthropic) untuk diproses."),
    btn);
  $app().replaceChildren(h("div", { class: "grid cols-2", style: "align-items:start" }, form, h("div", { class: "card" }, h("h2", {}, "Proyek"), list)));
  try {
    const ps = await api("GET", "/api/projects");
    list.replaceChildren(...(ps.length ? ps.map((p) => h("div", { class: "item" },
      h("div", { class: "spacer" },
        h("a", { href: "#/p/" + p.id }, p.title), h("br"),
        h("span", { class: "small type-" + p.video_type }, TYPE_LABEL[p.video_type]), h("span", { class: "small muted" }, ` · ${p.target_duration_sec}s · ${p.aspect_ratio} · ${new Date(p.updated_at).toLocaleString()}${p.created_by ? " · oleh " + p.created_by : ""}`)),
    )) : [h("div", { class: "muted" }, "Belum ada proyek.")]));
  } catch (e) { list.replaceChildren(h("div", { class: "error" }, e.message)); }
}

// ---------- project ----------
async function loadBundle(id) { state.bundle = await api("GET", "/api/projects/" + id); return state.bundle; }

async function renderProject(id) {
  try { await loadBundle(id); } catch (e) { $app().replaceChildren(h("div", { class: "error card" }, e.message)); return; }
  drawProject();
  schedulePoll();
}

function schedulePoll() {
  clearInterval(state.pollTimer);
  if (!state.bundle?.stages.some((s) => s.status === "generating")) return;
  state.pollTimer = setInterval(async () => {
    const id = state.bundle.project.id;
    const before = JSON.stringify(state.bundle.stages.map((s) => s.status + s.version));
    try { await loadBundle(id); } catch { return; }
    const after = JSON.stringify(state.bundle.stages.map((s) => s.status + s.version));
    if (before !== after) {
      drawProject();
      const failed = state.bundle.stages.find((s) => s.error && s.status === "ready");
      if (failed) toast(`${STAGE_META[failed.kind].title}: ${failed.error}`, true);
    }
    if (!state.bundle.stages.some((s) => s.status === "generating")) clearInterval(state.pollTimer);
  }, 2500);
}

function currentStage() {
  const stages = state.bundle.stages;
  if (state.activeStage && stages.find((s) => s.kind === state.activeStage)) return stages.find((s) => s.kind === state.activeStage);
  return stages.find((s) => s.status === "generating" || s.status === "review" || s.status === "ready") || stages[stages.length - 1];
}

function drawProject() {
  const { project: p, stages } = state.bundle;
  const cur = currentStage();
  const stepper = h("div", { class: "stepper" }, ...stages.map((s, i) => h("div", {
    class: "step" + (s.kind === cur.kind ? " active" : ""),
    onclick: () => { location.hash = `#/p/${p.id}/${s.kind}`; },
  }, h("div", { class: "n" }, `Tahap ${i + 1}`), h("div", { class: "t" }, STAGE_META[s.kind].title), badge(s.status))));
  const finalId = stages.find((s) => s.kind === "render")?.output?.final_asset_id;
  setKids($app(), projectHeader(p), referencesCard(null), stepper, stagePanel(cur),
    cur.kind === "render" ? null : scenesSection(cur),
    finalId && asset(finalId) ? publishSection(p, asset(finalId)) : null);
}

function projectHeader(p) {
  const details = h("details", {},
    h("summary", { class: "muted" }, "Lihat / ubah brief"),
    briefForm(p));
  return h("div", { class: "card" },
    h("div", { class: "row" },
      h("div", { class: "spacer" },
        h("h1", {}, p.title),
        h("div", { class: "small" }, h("span", { class: "type-" + p.video_type }, TYPE_LABEL[p.video_type]), h("span", { class: "muted" }, ` · ${p.target_duration_sec} detik · ${p.aspect_ratio} · ${p.language} · ${STYLE_LABEL[p.image_style] || p.image_style}`))),
      h("a", { href: `/api/projects/${p.id}/export` }, h("button", { class: "ghost" }, "⬇ Export JSON")),
      h("button", { class: "danger", onclick: (e) => {
        if (!confirm("Hapus proyek beserta seluruh file (referensi & hasil generate)? Tindakan ini permanen.")) return;
        guard(async () => { await api("DELETE", "/api/projects/" + p.id); location.hash = "#/"; toast("Proyek dan file terhapus"); }, e.target);
      } }, "Hapus")),
    details);
}

function briefForm(p) {
  const title = h("input", { value: p.title, maxlength: 200 });
  const dur = h("input", { type: "number", value: p.target_duration_sec, min: 5, max: 1800 });
  const ratio = h("select", {}, ...["16:9", "9:16", "1:1", "4:5"].map((r) => h("option", { value: r, selected: r === p.aspect_ratio }, r)));
  const lang = h("input", { value: p.language, maxlength: 20 });
  const tone = h("input", { value: p.tone, maxlength: 500 });
  const style = styleSelect(p.image_style);
  const brief = h("textarea", { rows: 6, maxlength: 20000, value: p.brief });
  const btn = h("button", { class: "secondary" }, "Simpan brief");
  btn.onclick = () => guard(async () => {
    await api("PUT", "/api/projects/" + p.id, { title: title.value, target_duration_sec: +dur.value, aspect_ratio: ratio.value, language: lang.value, tone: tone.value, image_style: style.value, brief: brief.value });
    await loadBundle(p.id); drawProject(); toast("Brief tersimpan. Regenerate storyboard agar perubahan berlaku.");
  }, btn);
  return h("div", { class: "grid", style: "margin-top:10px" },
    h("div", { class: "grid cols-2" },
      h("div", {}, h("label", {}, "Judul"), title), h("div", {}, h("label", {}, "Durasi (detik)"), dur),
      h("div", {}, h("label", {}, "Rasio"), ratio), h("div", {}, h("label", {}, "Bahasa"), lang),
      h("div", {}, h("label", {}, "Tone"), tone),
      h("div", {}, h("label", {}, "Gaya visual"), style)),
    h("div", {}, h("label", {}, "Brief"), brief), h("div", {}, btn));
}

// References (project-level when scene == null)
function referencesCard(scene) {
  const p = state.bundle.project;
  const refs = state.bundle.assets.filter((a) => a.source === "upload" && (scene ? a.scene_id === scene.id : !a.scene_id));
  const file = h("input", { type: "file", accept: "image/png,image/jpeg,image/webp,image/gif,video/mp4,video/webm,video/quicktime" });
  const note = h("input", { placeholder: "Catatan: apa yang diambil dari referensi ini? (gaya, karakter, produk, gerakan kamera…)", maxlength: 2000 });
  const consent = h("input", { type: "checkbox", style: "width:auto" });
  const btn = h("button", { class: "secondary" }, "Unggah");
  btn.onclick = () => guard(async () => {
    if (!file.files[0]) throw new Error("Pilih file dulu");
    if (!consent.checked) throw new Error("Centang konfirmasi hak & persetujuan");
    const fd = new FormData();
    fd.append("note", note.value);
    fd.append("consent", "true");
    if (scene) fd.append("scene_id", scene.id);
    fd.append("file", file.files[0]);
    await api("POST", `/api/projects/${p.id}/assets`, fd, true);
    await loadBundle(p.id); drawProject(); toast("Referensi diunggah");
  }, btn);
  const list = h("div", { class: "refs" }, ...refs.map((a) => h("div", { class: "ref" },
    mediaEl(a), h("div", { class: "muted", title: a.note }, a.note || a.original_name),
    h("button", { class: "danger", title: "Hapus", onclick: (e) => guard(async () => {
      if (!confirm("Hapus referensi ini?")) return;
      await api("DELETE", `/api/projects/${p.id}/assets/${a.id}`); await loadBundle(p.id); drawProject();
    }, e.target) }, "×"))));
  const body = h("div", { class: "grid" },
    refs.length ? list : h("div", { class: "muted small" }, "Belum ada referensi."),
    h("div", { class: "row" }, h("div", { style: "flex:1;min-width:200px" }, file), h("div", { style: "flex:2;min-width:220px" }, note), btn),
    h("label", { class: "row small", style: "color:var(--text)" }, consent,
      "Saya memiliki hak atas media ini dan telah memperoleh persetujuan dari setiap orang yang dapat dikenali di dalamnya (UU PDP / ISO 27001)."));
  if (scene) return h("details", {}, h("summary", { class: "small muted" }, `Referensi scene (${refs.length})`), body);
  return h("div", { class: "card" },
    h("h3", {}, "Referensi Proyek ", h("span", { class: "muted small" }, "— gambar dipakai Claude & provider gambar; video diteruskan ke provider video")),
    body);
}

function stagePanel(st) {
  const p = state.bundle.project;
  const meta = STAGE_META[st.kind];
  const fb = h("textarea", { rows: 3, maxlength: 5000, value: st.feedback, placeholder: "Feedback / instruksi revisi untuk generate berikutnya (opsional)" });
  const runBtn = h("button", { disabled: !["ready", "review"].includes(st.status) },
    st.version > 0 ? "↻ Generate ulang (semua scene)" : "▶ Generate");
  runBtn.onclick = () => guard(async () => {
    if (st.kind === "storyboard" && st.version > 0 && !confirm("Generate ulang storyboard akan MENGGANTI semua scene beserta VO, prompt, dan media per scene. Lanjutkan?")) return;
    await api("POST", `/api/projects/${p.id}/stages/${st.kind}/run`, { feedback: fb.value });
    await loadBundle(p.id); drawProject(); schedulePoll();
  }, runBtn);
  const approveBtn = h("button", { class: "secondary", disabled: st.status !== "review" }, "✓ Setujui & lanjut");
  approveBtn.onclick = () => guard(async () => {
    await api("POST", `/api/projects/${p.id}/stages/${st.kind}/approve`);
    const idx = state.bundle.stages.findIndex((s) => s.kind === st.kind);
    const next = state.bundle.stages[idx + 1];
    location.hash = next ? `#/p/${p.id}/${next.kind}` : `#/p/${p.id}/${st.kind}`;
    if (!next) { await loadBundle(p.id); drawProject(); toast("Semua tahap selesai 🎉"); }
  }, approveBtn);
  const reopenBtn = st.status === "approved" ? h("button", { class: "ghost", onclick: (e) => guard(async () => {
    if (!confirm("Buka kembali tahap ini untuk direvisi? Tahap setelahnya akan kembali ke status review/terkunci.")) return;
    await api("POST", `/api/projects/${p.id}/stages/${st.kind}/reopen`); await loadBundle(p.id); drawProject();
  }, e.target) }, "✎ Buka kembali untuk revisi") : null;

  let hint = null;
  if (st.status === "locked") hint = h("div", { class: "notice" }, "Setujui tahap sebelumnya untuk membuka tahap ini.");
  const providerNote = { audio: "tts", images: "image", videos: "video" }[st.kind];
  const mockWarn = providerNote && state.config?.providers?.[providerNote] === "mock"
    ? h("div", { class: "notice small" }, `Provider ${providerNote} = mock (placeholder). Set ${providerNote.toUpperCase()}_PROVIDER=${providerNote === "tts" ? "openai" : "byteplus"} (atau http) di environment untuk hasil nyata.`) : null;

  return h("div", { class: "card" },
    h("div", { class: "row" }, h("h2", { class: "spacer" }, meta.title, " ", badge(st.status)),
      st.status === "generating" ? h("span", {}, h("span", { class: "spinner" }), " memproses…") : null,
      h("span", { class: "muted small" }, `versi ${st.version}`)),
    h("p", { class: "muted" }, meta.desc),
    hint, mockWarn,
    st.error ? h("div", { class: "error" }, "Gagal: " + st.error) : null,
    st.kind === "storyboard" ? storyboardSummary(st) : st.kind === "render" ? renderSummary(st) : outputSummary(st),
    h("div", { style: "margin-top:10px" }, h("label", {}, "Feedback revisi"), fb),
    h("div", { class: "row", style: "margin-top:10px" }, runBtn, approveBtn, reopenBtn));
}

function storyboardSummary(st) {
  const o = st.output || {};
  if (!o.logline) return null;
  return h("div", { class: "grid" },
    h("div", {}, h("label", {}, "Logline"), h("div", {}, o.logline)),
    h("div", {}, h("label", {}, "Gaya visual"), h("div", {}, o.visual_style)),
    o.characters?.length ? h("div", {}, h("label", {}, "Karakter"),
      h("ul", {}, ...o.characters.map((c) => h("li", {}, h("b", {}, c.name), " — ", c.appearance)))) : null);
}

function renderSummary(st) {
  const o = st.output || {};
  if (!o.final_asset_id) return h("div", { class: "muted small" }, "Render menggabungkan klip video tiap scene (atau gambar bila belum ada video) dengan audio VO, menyesuaikan durasi, lalu menghasilkan MP4 H.264/AAC yang siap diunggah ke YouTube, Instagram, Threads, dan TikTok.");
  const v = asset(o.final_asset_id);
  const mb = (o.size_bytes / 1048576).toFixed(1);
  return h("div", { class: "grid" },
    v ? h("video", { class: "thumb " + ratioClass(), src: "/media/" + v.id, controls: true, preload: "metadata", style: "max-height:70vh;width:auto;max-width:100%" }) : null,
    h("div", { class: "row small" },
      h("span", {}, `${o.duration_sec} dtk · ${o.width}×${o.height} · ${mb} MB`), h("span", { class: "spacer" }),
      v ? h("a", { href: `/media/${v.id}?download=1` }, "⬇ Unduh MP4") : null,
      o.subtitle_asset_id ? h("a", { href: `/media/${o.subtitle_asset_id}?download=1` }, "⬇ Subtitle (.srt)") : null),
    o.warnings?.length ? h("div", { class: "notice small", style: "white-space:pre-line" }, "Catatan render:\n" + o.warnings.join("\n")) : null);
}

function outputSummary(st) {
  const o = st.output || {};
  if (!st.version) return null;
  const parts = [];
  if (o.updated_scenes != null) parts.push(`${o.updated_scenes} scene diperbarui`);
  if (o.generated != null) parts.push(`${o.generated} file dibuat`);
  return h("div", {},
    h("div", { class: "small muted" }, parts.join(" · ")),
    o.failed?.length ? h("div", { class: "error small" }, o.failed.join("\n")) : null);
}

// ---------- scenes ----------
function scenesSection(st) {
  const scenes = state.bundle.scenes;
  if (!scenes.length) return h("div", { class: "card muted" }, "Scene akan muncul setelah storyboard dibuat.");
  const total = scenes.reduce((a, s) => a + s.duration_sec, 0);
  return h("div", {},
    h("div", { class: "row", style: "margin:4px 0 10px" }, h("h2", { class: "spacer" }, `Scene (${scenes.length})`),
      h("span", { class: "muted small" }, `Total ${total.toFixed(1)} dtk / target ${state.bundle.project.target_duration_sec} dtk`)),
    h("div", { class: "grid cols-3" }, ...scenes.map((s) => sceneCard(s, st))));
}

function field(label, value, key, editable, rows = 3) {
  const el = rows === 0
    ? h("input", { type: "number", step: "0.5", min: "0.5", max: "120", value, disabled: !editable })
    : h("textarea", { rows, value, disabled: !editable });
  el.dataset.key = key;
  return h("div", {}, h("label", {}, label), el);
}

function sceneCard(s, st) {
  const p = state.bundle.project;
  const editable = ["ready", "review"].includes(st.status);
  const fields = [];
  const media = [];
  switch (st.kind) {
    case "storyboard":
      fields.push(field("Judul", s.title, "title", editable, 1), field("Deskripsi", s.description, "description", editable, 4),
        field("Shot / kamera", s.shot, "shot", editable, 2), field("Durasi (dtk)", s.duration_sec, "duration_sec", editable, 0));
      break;
    case "voiceover":
      media.push(h("div", { class: "small muted" }, s.description));
      fields.push(field("Voice over", s.voiceover, "voiceover", editable, 5));
      break;
    case "audio":
      fields.push(field("Voice over", s.voiceover, "voiceover", editable, 4));
      media.push(mediaEl(asset(s.audio_asset_id)) || h("div", { class: "muted small" }, "Belum ada audio."));
      break;
    case "prompts":
      media.push(h("div", { class: "small muted" }, s.description));
      fields.push(field("Image prompt", s.image_prompt, "image_prompt", editable, 5), field("Video prompt", s.video_prompt, "video_prompt", editable, 4));
      break;
    case "images":
      media.push(mediaEl(asset(s.image_asset_id), "thumb " + ratioClass()) || h("div", { class: "thumb " + ratioClass() }));
      fields.push(field("Image prompt", s.image_prompt, "image_prompt", editable, 4));
      break;
    case "videos":
      media.push(mediaEl(asset(s.video_asset_id), "thumb " + ratioClass()) || mediaEl(asset(s.image_asset_id), "thumb " + ratioClass()) || h("div", { class: "thumb " + ratioClass() }));
      if (s.audio_asset_id) media.push(mediaEl(asset(s.audio_asset_id)));
      media.push(h("div", { class: "small" }, s.voiceover));
      fields.push(field("Video prompt", s.video_prompt, "video_prompt", editable, 4));
      break;
  }
  const card = h("div", { class: "scene" });
  const save = h("button", { class: "ghost", disabled: !editable }, "Simpan");
  save.onclick = () => guard(async () => {
    const body = {};
    card.querySelectorAll("[data-key]").forEach((el) => { body[el.dataset.key] = el.type === "number" ? parseFloat(el.value) : el.value; });
    await api("PUT", `/api/projects/${p.id}/scenes/${s.id}`, body);
    await loadBundle(p.id); toast(`Scene ${s.idx} tersimpan`);
  }, save);
  const regen = st.kind !== "storyboard" ? h("button", { class: "ghost", disabled: !editable, title: "Generate ulang hanya scene ini" }, "↻ Scene ini") : null;
  if (regen) regen.onclick = () => guard(async () => {
    const fb = card.closest("main").querySelector(".card textarea[maxlength='5000']");
    await api("POST", `/api/projects/${p.id}/stages/${st.kind}/run`, { scene_id: s.id, feedback: fb ? fb.value : undefined });
    await loadBundle(p.id); drawProject(); schedulePoll();
  }, regen);
  const dl = [s.image_asset_id, s.video_asset_id, s.audio_asset_id].filter(Boolean).length && ["images", "videos", "audio"].includes(st.kind)
    ? h("a", { class: "small", href: `/media/${{ images: s.image_asset_id, videos: s.video_asset_id, audio: s.audio_asset_id }[st.kind] || ""}?download=1` }, "⬇ unduh") : null;
  card.append(
    h("div", { class: "head" }, h("span", { class: "idx" }, s.idx), h("b", { class: "spacer" }, s.title), h("span", { class: "muted small" }, `${s.duration_sec}s`)),
    ...media.filter(Boolean), ...fields,
    h("div", { class: "row" }, save, regen, h("span", { class: "spacer" }), dl),
    ["storyboard", "prompts", "images", "videos"].includes(st.kind) ? referencesCard(s) : null);
  return card;
}

// ---------- boot ----------
// Footer: link to the Corresponding Source (AGPL-3.0 section 13). Public endpoint,
// so it also works on the login page. Only https URLs are accepted.
async function loadAbout() {
  try {
    const r = await fetch("/api/about", { headers: { "X-Requested-With": "storyboard" } });
    if (!r.ok) return;
    const a = await r.json();
    const link = document.getElementById("sourceLink");
    if (!a.source_url) { document.getElementById("siteFoot").classList.add("hidden"); return; }
    if (/^https:\/\//i.test(a.source_url)) link.href = a.source_url;
  } catch { /* keep the default link */ }
}
loadAbout();

async function boot() {
  try {
    state.me = await api("GET", "/api/me");
  } catch { return; } // renderLogin already shown on 401
  const me = state.me;
  document.getElementById("userNav").classList.remove("hidden");
  document.getElementById("adminLink").classList.toggle("hidden", !me.is_admin);
  document.getElementById("meLink").textContent = `👤 ${me.display_name || me.username}${me.is_admin ? " · admin" : ""}`;
  document.getElementById("logoutBtn").onclick = async () => { await api("POST", "/api/logout").catch(() => {}); renderLogin(); };
  if (me.must_change_password) { renderAccountPage(true); return; }
  try { state.config = await api("GET", "/api/config"); } catch (e) { toast(e.message, true); return; }
  const pv = state.config.providers;
  document.getElementById("providerInfo").textContent = `Claude: ${state.config.claude ? state.config.claude_model : "off"} · image: ${pv.image} · video: ${pv.video} · tts: ${pv.tts}`;
  route();
}
boot();
