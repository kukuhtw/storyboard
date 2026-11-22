# Storyboard Studio

**Lisensi:** [AGPL-3.0-only](LICENSE) **atau** [lisensi komersial](COMMERCIAL-LICENSE.md). Lihat
bagian [Lisensi](#lisensi).

Aplikasi web (Go + PostgreSQL) untuk membantu membuat video **dokumentasi**, **iklan**, dan **drama**
secara bertahap, dengan review di setiap tahap:

| # | Tahap | Dikerjakan oleh | Hasil |
|---|-------|-----------------|-------|
| 1 | Storyboard | Claude API (`claude-opus-5-5`) | logline, gaya visual, karakter, daftar scene (deskripsi, shot, durasi) |
| 2 | Voice Over | Claude API | naskah narasi/dialog per scene |
| 3 | Audio VO | Provider TTS | file audio per scene |
| 4 | Prompt Gambar & Video | Claude API (+ gambar referensi) | `image_prompt` & `video_prompt` per scene (EN) |
| 5 | Gambar | Provider gambar | keyframe per scene |
| 6 | Video | Provider video (image-to-video) | klip per scene |
| 7 | Render Final | ffmpeg | satu MP4 (H.264/AAC, faststart) + subtitle SRT |

Setelah render: **Publikasi** ke YouTube Shorts, Instagram Reels, Threads, dan TikTok, langsung
atau terjadwal, dengan persetujuan opsional dan draf caption dari Claude.

Di tiap tahap user bisa:
- **Generate** / **Generate ulang** dengan *feedback revisi*,
- **Edit** hasil per scene secara manual, **generate ulang satu scene**,
- **Setujui** (membuka tahap berikutnya) atau **Buka kembali** tahap yang sudah disetujui,
- **Unggah referensi** gambar/video di level proyek atau per scene (dengan catatan),
- **Export** seluruh proyek ke JSON, unduh media per scene.

## Menjalankan

Prasyarat: Go 1.26+, PostgreSQL 13+, ffmpeg (untuk Render Final).

```powershell
# 1. Database (pilih salah satu)
#    a) Docker:   $env:POSTGRES_PASSWORD="..."; docker compose up -d
#    b) PostgreSQL lokal: CREATE USER storyboard PASSWORD '...'; CREATE DATABASE storyboard OWNER storyboard;

# 2. Konfigurasi
Copy-Item .env.example .env   # isi DATABASE_URL, ADMIN_PASSWORD, ANTHROPIC_API_KEY

# 3. Jalankan (migrasi DB otomatis saat start)
go run ./cmd/server
# buka http://127.0.0.1:8080, masuk sebagai ADMIN_USERNAME / ADMIN_PASSWORD (wajib ganti password),
# lalu hapus ADMIN_PASSWORD dari .env dan buat akun tim di menu Admin
```

Tanpa provider gambar/video/TTS, set `*_PROVIDER=mock`: pipeline tetap jalan dengan placeholder
(SVG untuk gambar/video, WAV hening untuk audio) sehingga alur review bisa dicoba.

## Akun & peran

Login memakai **username + password per orang** (tidak ada token bersama). Ada dua role:

| | User | Admin |
|---|---|---|
| Proyek, semua tahap, referensi, render, susun & batalkan publikasi | ✅ | ✅ |
| Hubungkan / putus akun sosial media | — | ✅ |
| Setujui post (`POST_APPROVAL=true`) | — | ✅ |
| **Dashboard Admin**: tambah user, ubah role, reset password, buka kunci, paksa logout, nonaktifkan, audit log | — | ✅ |

- **Admin pertama**: `ADMIN_USERNAME` + `ADMIN_PASSWORD`, hanya dipakai saat tabel user kosong.
  Alternatif atau pemulihan darurat: `storyboard reset-password <username>` (mencetak password sementara).
- **Password**: 12–128 karakter, tidak boleh memuat username, bukan password umum, disimpan sebagai
  hash **Argon2id**. Password sementara (dibuat admin atau CLI) wajib diganti saat login pertama;
  sampai diganti, server memblokir semua endpoint lain.
- **Penguncian**: 5× salah berturut-turut → akun terkunci 15 menit. Ditambah batas 10 gagal / 15 menit per IP.
- **Sesi**: token acak di cookie `HttpOnly` + `SameSite=Strict`. Di database hanya disimpan hash-nya.
  Berakhir setelah `SESSION_TTL_HOURS` (12) atau tidak aktif `SESSION_IDLE_MINUTES` (60). Ganti
  password, reset, ubah role, dan nonaktifkan otomatis mencabut sesi.
- User tidak dihapus, hanya dinonaktifkan, agar audit log dan kepemilikan proyek tetap utuh.
- **API key provider** (Claude, BytePlus, OpenAI, aplikasi sosial media) disediakan admin platform lewat
  environment variable. Tidak ada halaman settings dan tidak ada BYOK.

## BytePlus ModelArk (gambar, foto, video)

Set `IMAGE_PROVIDER=byteplus` dan/atau `VIDEO_PROVIDER=byteplus`, lalu isi `BYTEPLUS_API_KEY`,
`BYTEPLUS_IMAGE_MODEL` (Seedream), dan `BYTEPLUS_VIDEO_MODEL` (Seedance). Nilainya model ID atau
endpoint ID (`ep-...`) yang sudah diaktifkan di console ModelArk.

- **Gambar/foto**: `POST {BASE}/images/generations`. Gambar referensi proyek/scene (maks 4) dikirim
  sebagai `image` (data URL). Ukuran mengikuti rasio proyek. Pilih **Gaya visual = Foto realistis**
  di proyek untuk hasil fotografi.
- **Video**: `POST {BASE}/contents/generations/tasks` (async), lalu polling `GET .../tasks/{id}`
  sampai `succeeded`. Keyframe scene yang sudah disetujui dipakai sebagai `first_frame`
  (image-to-video). `ratio`, `duration` (dibulatkan & dibatasi `BYTEPLUS_VIDEO_MIN_SEC..MAX_SEC`),
  `resolution`, dan `watermark` dikirim sebagai field JSON.
- URL hasil BytePlus bersifat sementara, jadi file langsung diunduh ke storage aplikasi. API key
  tidak pernah dikirim ke host download.
- Video referensi yang diunggah user **tidak** dikirim ke BytePlus (API menerima URL publik, bukan
  upload). Video referensi tetap dipakai Claude sebagai catatan saat menulis prompt.

## OpenAI TTS (voice over audio)

`TTS_PROVIDER=openai` + `OPENAI_API_KEY`. Memanggil `POST /v1/audio/speech` (model default
`gpt-4o-mini-tts`, output MP3). Bahasa proyek dan `OPENAI_TTS_INSTRUCTIONS` dikirim sebagai
`instructions` (hanya model `gpt-4o-*`; `tts-1`/`tts-1-hd` tidak menerimanya). Batas 4096 karakter
per scene. Catatan: Whisper adalah speech-to-text dan tidak dipakai untuk TTS.

## Render Final

Setiap scene dirender menjadi segmen dengan resolusi rasio proyek (1080×1920, 1920×1080,
1080×1080, 1080×1350), 30 fps, fade in/out singkat:
- visual: klip video scene → gambar diam (jika belum ada video) → layar polos (jika hanya mock SVG)
- durasi: `max(panjang klip / durasi scene, durasi audio VO + 0,4 dtk)`. Frame terakhir di-freeze bila VO lebih panjang.
- audio: VO scene (AAC 48 kHz stereo) atau hening

Segmen digabung tanpa re-encode (concat) dengan `+faststart`, sesuai spesifikasi Reels/Threads.
Subtitle SRT dibuat dari naskah VO (potongan ±10 kata, timing proporsional terhadap durasi audio).
Butuh `ffmpeg`/`ffprobe` (sudah ada di image Docker; lokal: set `FFMPEG_PATH`/`FFPROBE_PATH`).

## Publikasi sosial media

| | YouTube Shorts | Instagram Reels | Threads | TikTok |
|---|---|---|---|---|
| API | Data API v3 (resumable upload) | Instagram API with Instagram Login | Threads API | Content Posting API (Direct Post, FILE_UPLOAD) |
| Cara kirim video | upload byte | platform mengunduh URL bertanda tangan | platform mengunduh URL bertanda tangan | upload chunk |
| Label AI | `containsSyntheticMedia` | `is_ai_generated` | `is_made_with_ai` | `is_aigc` |
| Visibilitas | public / unlisted / private | public | public | public / private (SELF_ONLY) |

- **Scheduler** berjalan di dalam aplikasi (cek tiap 15 dtk, `FOR UPDATE SKIP LOCKED` sehingga aman
  untuk beberapa instance). Jadwal disimpan di PostgreSQL, jadi tetap jalan setelah restart/redeploy.
- **Retry** (maks 3, backoff) hanya untuk kegagalan *sebelum* konten sampai ke platform. Kegagalan
  setelahnya, atau post yang terputus karena restart, ditandai *Gagal* untuk dicek manual. Ini
  mencegah post ganda.
- **Persetujuan** (`POST_APPROVAL=true`): post masuk status *Menunggu persetujuan* sampai disetujui.
- **Token** OAuth dienkripsi AES-256-GCM (`TOKEN_ENC_KEY`), diperbarui otomatis (Meta 60 hari,
  TikTok refresh token berotasi, Google access token 1 jam). Pesan error disanitasi agar token tidak
  bocor ke log.
- Status platform: post publik baru bisa setelah aplikasi Anda lolos **App Review Meta**, **audit
  TikTok** (sebelumnya hanya privat), dan **verifikasi Google OAuth**.

Setup detail: [DEPLOY-DOKPLOY.md](DEPLOY-DOKPLOY.md#setup-aplikasi-developer-sosial-media).

## Deploy

Panduan lengkap (HTML, dengan diagram arsitektur): **[documentation/deploy-dokploy.html](documentation/deploy-dokploy.html)**.
Ringkasan teks: [DEPLOY-DOKPLOY.md](DEPLOY-DOKPLOY.md) (Dockerfile + Dokploy, health check, volume, domain/HTTPS).

## Dokumentasi

| File | Untuk | Isi |
|---|---|---|
| [documentation/tutorial.html](documentation/tutorial.html) | Pengguna | Login, buat proyek, 7 tahap pipeline, referensi, revisi, render, publikasi (bergambar) |
| [documentation/tutorial-admin.html](documentation/tutorial-admin.html) | Admin | Admin pertama, kelola user, audit log, akun sosial, persetujuan, API key, akses darurat |
| [documentation/deploy-dokploy.html](documentation/deploy-dokploy.html) | DevOps | Deploy ke Dokploy step by step, environment (termasuk penjelasan `DATABASE_URL`), aplikasi developer sosial media, checklist |
| [documentation/user-journey.html](documentation/user-journey.html) | Product/UX | Persona, peta perjalanan pengguna, pola revisi |
| [documentation/lisensi.html](documentation/lisensi.html) | Semua / Legal | Dual licensing AGPL-3.0 + komersial, kewajiban per skenario, CLA, komponen pihak ketiga, FAQ |

Semua file HTML mandiri (gambar tertanam), bisa dibuka langsung di browser atau dibagikan.

## Provider contract (`*_PROVIDER=http`)

Server mengirim `POST {*_API_URL}` dengan `Authorization: Bearer {*_API_KEY}` dan body JSON:

```jsonc
// Gambar
{ "prompt": "...", "aspect_ratio": "16:9", "reference_images": ["data:image/png;base64,..."] }
// Video
{ "prompt": "...", "aspect_ratio": "16:9", "duration_sec": 5,
  "start_image": "data:image/png;base64,...", "reference_videos": ["data:video/mp4;base64,..."] }
// TTS
{ "text": "...", "voice": "default", "language": "id" }
```

Respons yang diterima:

```jsonc
{ "b64": "<base64>", "mime": "image/png" }        // hasil langsung
{ "url": "https://..." }                          // hasil untuk diunduh
{ "status": "queued", "status_url": "https://..." } // async: di-GET tiap 5 dtk (maks 20 mnt)
                                                    // sampai {"status":"succeeded","url"|"b64"} atau {"status":"failed","error"}
```

Jika vendor Anda memakai format lain, buat proxy tipis yang menerjemahkan ke kontrak ini, atau tambahkan
implementasi `ImageGen` / `VideoGen` / `TTS` baru di `internal/providers`.

## Struktur

```
cmd/server/            entrypoint
internal/config        konfigurasi dari env (.env)
internal/db            koneksi + migrasi (embed SQL)
internal/store         semua query SQL (parameterized)
internal/auth          hash password (Argon2id), kebijakan password, token sesi
internal/ai            klien Claude (structured outputs, vision, refusal fallback)
internal/providers     adapter gambar/video/TTS (mock + generic HTTP)
internal/pipeline      logika tiap tahap + prompt
internal/media         penyimpanan file (nama acak, batas ukuran)
internal/render        render final (ffmpeg) + subtitle SRT
internal/social        YouTube / Instagram / Threads / TikTok + scheduler
internal/secure        enkripsi token (AES-GCM) + signed URL
internal/api           HTTP API, auth, upload, audit
web/static             frontend (vanilla JS, tanpa build): app.js, social.js, admin.js
documentation/         panduan HTML (lihat bagian Dokumentasi)
scripts/               third-party-licenses.sh (membuat ulang THIRD_PARTY_LICENSES.txt)
```

## Keamanan & perlindungan data (ISO/IEC 27001:2022, UU PDP)

Kontrol yang sudah diterapkan:

| Kontrol | Implementasi |
|---|---|
| A.5.15–A.5.18/A.8.5 Akses & autentikasi | Akun per orang + role (admin/user, dicek di server), password Argon2id + kebijakan panjang, wajib ganti password sementara, penguncian akun (5×/15 mnt) + limit per IP, sesi server-side (hash token, idle & absolute timeout, dicabut saat reset/nonaktif), pengaman "minimal satu admin aktif" |
| A.8.24 Kriptografi / secrets | Semua secret hanya dari env; tidak disimpan di DB / log; `.env` di `.gitignore`. Gunakan TLS (reverse proxy) + `COOKIE_SECURE=true` di produksi |
| A.8.15 Logging | Tabel `audit_log` dengan **pelaku = username individu** (login/gagal, aksi admin, proyek, upload, generate, approve, publikasi), bisa dilihat & difilter admin. Username tak dikenal tidak dicatat. Log HTTP hanya method/path/status, tanpa body/token |
| A.8.28 Secure coding | SQL parameterized, validasi input & panjang, CSP ketat, `nosniff`, `X-Frame-Options`, header CSRF, output via `textContent` (anti-XSS), media disajikan dengan CSP `sandbox` |
| A.8.12 Upload | Tipe file dideteksi dari isi (bukan ekstensi), allow-list, batas ukuran, nama file acak di server (anti path traversal) |
| A.5.34 Privasi & PII | Konfirmasi hak/persetujuan wajib saat unggah referensi berisi orang; peringatan agar tidak memasukkan data pribadi nasabah di brief; system prompt melarang memunculkan PII |
| A.8.10 Penghapusan data | Hapus proyek = hapus semua baris DB **dan** file media terkait |

Yang **harus** dilakukan organisasi sebelum produksi (tidak bisa ditangani oleh kode saja):

1. **Transfer data ke pihak ketiga**: brief, naskah, dan gambar referensi dikirim ke Anthropic (Claude API)
   dan ke provider gambar/video/TTS yang Anda pasang. Pastikan ada DPA/penilaian vendor dan dasar hukum
   transfer data lintas negara sesuai UU PDP & kebijakan perusahaan.
2. Tetapkan prosedur akun: siapa yang boleh menjadi admin (minimal 2), offboarding (nonaktifkan di hari
   terakhir), review akses per kuartal, dan prinsip 4 mata untuk persetujuan post (aplikasi belum mencegah
   admin menyetujui post buatannya sendiri). Opsional: SSO/OIDC ke direktori perusahaan.
3. Jalankan di belakang reverse proxy HTTPS; enkripsi disk untuk `DATA_DIR` dan PostgreSQL; backup terenkripsi.
4. Tetapkan kebijakan retensi (mis. hapus proyek > N hari) dan retensi `audit_log`.
5. Simpan secret di vault (mis. Azure Key Vault / HashiCorp Vault), bukan file `.env`, di produksi.

## Lisensi

Storyboard Studio memakai **dual licensing**. Pilih salah satu:

| Lisensi | Untuk siapa | Kewajiban utama |
|---|---|---|
| **[AGPL-3.0-only](LICENSE)** (gratis) | Siapa pun yang mau memakai, mengubah, dan self-host | Jika Anda **mengubah** kode lalu menjalankannya sebagai layanan jaringan, atau mendistribusikannya, source code versi Anda wajib dibuka di bawah AGPL-3.0 (pasal 13). |
| **[Lisensi komersial](COMMERCIAL-LICENSE.md)** (berbayar) | Perusahaan yang ingin modifikasi tertutup, white-label/OEM, atau menjual SaaS tanpa membuka kode | Sesuai perjanjian tertulis. |

Penjelasan lengkap dengan contoh skenario dan FAQ: **[documentation/lisensi.html](documentation/lisensi.html)**.

| File | Isi |
|---|---|
| [LICENSE](LICENSE) | Teks resmi GNU AGPL v3 (dari gnu.org, tidak diubah) |
| [NOTICE](NOTICE) | Pemegang hak cipta, pernyataan dual licensing, merek dagang |
| [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md) | Kapan lisensi komersial dibutuhkan dan cara mendapatkannya |
| [CLA.md](CLA.md) | Contributor License Agreement, wajib bagi kontributor |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Aturan kontribusi (CLA, lisensi dependency, larangan secret/PII) |
| [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) | Daftar komponen pihak ketiga dan lisensinya (termasuk FFmpeg di image Docker) |
| [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt) | Teks lisensi lengkap semua modul Go yang ikut ter-compile |

- **Link source code di UI** (AGPL pasal 13): footer setiap halaman, termasuk halaman login, menampilkan
  link "Source code" ke `SOURCE_URL` (default `https://github.com/kukuhtw/storyboard/`). Informasi ini
  juga tersedia lewat endpoint publik `GET /api/about`. Jika Anda **memodifikasi** kode dan
  menjalankannya untuk orang lain, arahkan `SOURCE_URL` ke source code versi Anda. Nilai `off`
  menyembunyikan link, dan hanya boleh dipakai pada deployment berlisensi komersial.
- Setiap file sumber memakai header `SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial`.
- Image Docker menyertakan file lisensi di `/app/licenses/`.
- Setelah menambah atau mengubah dependency, jalankan `go mod download && bash scripts/third-party-licenses.sh`,
  lalu perbarui tabel di `THIRD_PARTY_NOTICES.md`.
- Nama "Storyboard Studio" adalah merek pemegang hak cipta. Fork wajib memakai nama lain.
