# Deploy ke Dokploy

Ada dua opsi. **Opsi A disarankan.** Database dikelola Dokploy, sehingga backup PostgreSQL bisa
dijadwalkan dari UI.

Prasyarat:
- Kode ada di repo Git (GitHub/GitLab/Gitea/Bitbucket) yang terhubung ke Dokploy.
- Domain mengarah (DNS A record) ke server Dokploy.
- `.env` **tidak** ikut di-commit. Semua secret diisi lewat tab *Environment* di Dokploy.

---

## Opsi A — Application (Dockerfile) + PostgreSQL dari Dokploy

### 1. Buat database
*Project → Create Service → Database → PostgreSQL*
- Name: `storyboard-db`, Database: `storyboard`, User: `storyboard`, password: generate yang kuat.
- **Jangan** isi *External Port*, supaya DB hanya bisa diakses dari jaringan internal Dokploy.
- Deploy, lalu salin **Internal Connection URL** (bentuknya `postgresql://storyboard:...@storyboard-db-xxxx:5432/storyboard`).
- Atur *Backups* (S3 destination) sesuai kebijakan retensi.

### 2. Buat aplikasi
*Project → Create Service → Application*
- **Provider**: repo Git & branch.
- **Build Type**: `Dockerfile`, Dockerfile Path: `Dockerfile`, Docker Context Path: `.`

### 3. Environment (tab *Environment*)
```env
DATABASE_URL=postgresql://storyboard:<password>@<internal-host>:5432/storyboard?sslmode=disable
# Admin pertama (hanya dipakai saat belum ada user; hapus setelah login pertama)
ADMIN_USERNAME=admin
ADMIN_PASSWORD=<≥12 karakter, tidak memuat username>
SESSION_TTL_HOURS=12
SESSION_IDLE_MINUTES=60
ANTHROPIC_API_KEY=<key>
CLAUDE_MODEL=claude-opus-5-5

IMAGE_PROVIDER=byteplus
VIDEO_PROVIDER=byteplus
BYTEPLUS_API_KEY=<ModelArk API key>
BYTEPLUS_IMAGE_MODEL=<model/endpoint ID Seedream>
BYTEPLUS_VIDEO_MODEL=<model/endpoint ID Seedance>
BYTEPLUS_VIDEO_RESOLUTION=720p

TTS_PROVIDER=openai
OPENAI_API_KEY=<key>
OPENAI_TTS_MODEL=gpt-4o-mini-tts
OPENAI_TTS_VOICE=alloy
OPENAI_TTS_INSTRUCTIONS=Warm, friendly narrator, medium pace

MAX_UPLOAD_MB=100

# Publikasi sosial media
PUBLIC_BASE_URL=https://storyboard.domainanda.com
TOKEN_ENC_KEY=<openssl rand -base64 32>
POST_APPROVAL=true
GOOGLE_CLIENT_ID=...        GOOGLE_CLIENT_SECRET=...
TIKTOK_CLIENT_KEY=...       TIKTOK_CLIENT_SECRET=...
INSTAGRAM_APP_ID=...        INSTAGRAM_APP_SECRET=...
THREADS_APP_ID=...          THREADS_APP_SECRET=...
```
`ADMIN_PASSWORD` membuat admin pertama saat tabel user kosong. Setelah login pertama (wajib ganti
password), **hapus variabel ini** lalu redeploy. User lain dibuat dari menu **Admin** di aplikasi.
`TOKEN_ENC_KEY` mengenkripsi token OAuth di database. Simpan juga cadangannya di vault, karena jika
hilang atau berubah, semua akun sosial harus dihubungkan ulang.
`APP_ADDR=0.0.0.0:8080`, `DATA_DIR=/data`, `TRUST_PROXY=true`, dan `COOKIE_SECURE=true` sudah
diset di Dockerfile. Tidak ada secret yang di-bake ke image.

### 4. Volume persisten (tab *Advanced → Mounts*)
- Type: **Volume**, Volume name: `storyboard-media`, Mount path: `/data`
  (berisi file referensi & hasil generate. Jika tidak di-mount, file hilang setiap redeploy).

### 5. Domain (tab *Domains*)
- Host: `storyboard.domainanda.com`, Container Port: `8080`, HTTPS: on, Certificate: Let's Encrypt.

### 6. Zero-downtime / health check
Tab *Advanced → Cluster Settings → Swarm Settings → Health Check*:
```json
{ "Test": ["CMD", "/app/storyboard", "healthcheck"], "Interval": 30000000000, "Timeout": 10000000000, "StartPeriod": 30000000000, "Retries": 3 }
```
Image tidak memasang `curl`, jadi health check memanggil binary itu sendiri (`GET /healthz`,
ikut mengecek koneksi DB). Image runtime = Alpine + **ffmpeg** (untuk Render Final), user non-root.

### 7. Deploy
Klik **Deploy**. Migrasi database berjalan otomatis saat container start. Aktifkan *Auto Deploy*
(webhook) jika ingin deploy otomatis setiap push.

---

## Opsi B — Docker Compose (app + Postgres dalam satu stack)

*Project → Create Service → Compose*
- Provider: repo Git, **Compose Path**: `docker-compose.dokploy.yml`
- *Environment*: isi minimal `POSTGRES_PASSWORD`, `ADMIN_PASSWORD`, `ANTHROPIC_API_KEY`,
  plus variabel `BYTEPLUS_*` dan `IMAGE_PROVIDER/VIDEO_PROVIDER` seperti di atas.
  Dokploy menulisnya ke `.env` di samping file compose, lalu compose meneruskannya secara eksplisit.
- *Domains*: service `app`, port `8080`, HTTPS on.
- Deploy.

Data disimpan di named volume `storyboard-media` dan `storyboard-pg`, sehingga bisa memakai fitur
*Volume Backups* Dokploy.
⚠️ Menghapus service dengan centang "volumes" akan **menghapus data**. Backup dulu.

---

## Akses darurat admin

Jika semua admin terkunci atau lupa password, buka *Terminal* container aplikasi di Dokploy:
```sh
/app/storyboard reset-password admin
```
Jika user belum ada, ia dibuat sebagai admin. Jika sudah ada, password direset, kunci dibuka,
akun diaktifkan, dan semua sesi dicabut. Password sementara dicetak sekali dan wajib diganti saat
login. Aksi ini tercatat di audit log (pelaku `cli`).

---

## Setup aplikasi developer sosial media

Redirect URI yang didaftarkan di tiap platform (ganti domain):

| Platform | Redirect URI | Yang perlu diaktifkan |
|---|---|---|
| YouTube | `https://<domain>/oauth/youtube/callback` | Google Cloud: YouTube Data API v3, OAuth consent screen, OAuth client *Web*. Scope `youtube.upload`, `youtube.readonly`. Verifikasi Google diperlukan untuk publik (sensitive scope). |
| TikTok | `https://<domain>/oauth/tiktok/callback` | TikTok for Developers: Login Kit + Content Posting API (Direct Post). Scope `user.info.basic`, `video.publish`, `video.upload`. **Sebelum audit TikTok, semua post hanya privat.** |
| Instagram | `https://<domain>/oauth/instagram/callback` | Meta app → *Instagram API with Instagram Login*. Scope `instagram_business_basic`, `instagram_business_content_publish`. Akun IG harus **Business/Creator**. App Review untuk akses publik. |
| Threads | `https://<domain>/oauth/threads/callback` | Meta app → Threads API. Scope `threads_basic`, `threads_content_publish`. App Review untuk akses publik. |

Instagram & Threads **mengunduh** video dari server kita. Aplikasi membuat URL bertanda tangan
(`/public/media/{id}?exp=…&sig=…`, berlaku 2 jam, hanya untuk video final), jadi domain harus
bisa diakses publik via HTTPS. Setiap pengambilan tercatat di audit log.

Batas platform (dokumentasi resmi, Nov 2026): Instagram ±100 post API/24 jam, Threads 250/24 jam,
TikTok 6 init/menit per user, durasi Reels 3 dtk–15 mnt, Threads ≤5 mnt.

---

## Verifikasi setelah deploy
1. `https://storyboard.domainanda.com/healthz` → `{"status":"ok"}`
2. Login dengan `ADMIN_USERNAME` / `ADMIN_PASSWORD`, ganti password (wajib), lalu hapus `ADMIN_PASSWORD`
   dari Environment dan redeploy. Header atas menampilkan `image: byteplus · video: byteplus`.
   Buat akun tim di menu **Admin → Pengguna**.
3. Buat proyek uji → jalankan storyboard → … → gambar & video.
4. Cek log di Dokploy: hanya method/path/status, tanpa token/isi request.
5. Tahap **Render Final**: hasil MP4 bisa diputar & diunduh (memastikan ffmpeg di image berjalan).
6. Menu **Akun Sosial** → Hubungkan → kembali dengan status "terhubung". Uji publikasi dengan
   visibilitas **Privat** (YouTube/TikTok) terlebih dahulu.

## Checklist keamanan produksi (ISO/IEC 27001 / UU PDP)
- [ ] HTTPS aktif (Let's Encrypt) dan `COOKIE_SECURE=true` (default di image).
- [ ] DB tanpa external port. Password DB & token disimpan hanya di Dokploy Environment.
- [ ] Backup DB + volume media terenkripsi & retensi ditetapkan. Restore pernah diuji.
- [ ] Akses dashboard Dokploy dibatasi (2FA, user minimal).
- [ ] **Transfer data ke pihak ketiga**: prompt & gambar referensi dikirim ke **Anthropic** (Claude) dan
      **BytePlus** (region `ap-southeast`, Singapura). Naskah voice over dikirim ke **OpenAI** (TTS). Pastikan DPA/penilaian vendor dan dasar hukum
      transfer lintas negara sudah disetujui sebelum memakai data yang memuat orang/nasabah.
- [ ] Video final & caption dikirim ke **Google (YouTube), Meta (Instagram/Threads), TikTok** saat
      dipublikasikan. Token akun resmi perusahaan tersimpan terenkripsi. Tetapkan siapa yang boleh
      menyetujui publikasi (`POST_APPROVAL=true`) dan siapa pemilik akun.
- [ ] `ADMIN_PASSWORD` sudah dihapus dari Environment setelah admin pertama dibuat. Minimal 2 admin aktif.
- [ ] Akun karyawan/vendor yang keluar dinonaktifkan. Daftar admin ditinjau per kuartal.
- [ ] Akses Terminal Dokploy dibatasi (perintah darurat `reset-password` bisa membuat admin).
- [ ] Rotasi API key berkala. Opsional: SSO (OIDC) agar akun terhubung ke direktori perusahaan.
