# Berkontribusi ke Storyboard Studio

Terima kasih atas minat Anda.

## Syarat lisensi
- Proyek ini berlisensi **AGPL-3.0-only atau lisensi komersial** (lihat `NOTICE`).
- Setiap kontributor wajib menandatangani **[CLA](CLA.md)** sebelum pull request pertama di-merge.
  Caranya: tulis komentar berikut di pull request Anda:
  > I have read the CLA Document and I hereby sign the CLA.
- Jika Anda berkontribusi atas nama perusahaan, atau membuat kontribusi di jam kerja atau dengan
  perangkat milik kantor, pastikan atasan atau perusahaan sudah mengizinkan.
- Kode atau aset dari pihak lain hanya boleh disertakan jika lisensinya kompatibel dengan AGPL-3.0
  **dan** dengan lisensi komersial (MIT, BSD, Apache-2.0, ISC). Kode GPL/AGPL milik pihak lain
  tidak bisa diterima, karena tidak bisa dilisensikan ulang secara komersial. Sebutkan sumber dan
  lisensinya di pull request.

## Keamanan dan data pribadi
- **Jangan** menaruh API key, password, token, atau data pribadi (nama, email, nomor telepon,
  data nasabah) di issue, pull request, log, screenshot, atau data uji. Gunakan data fiktif.
- Laporkan celah keamanan secara privat ke **[EMAIL KEAMANAN / SECURITY EMAIL]**, jangan lewat
  issue publik.

## Standar kode
- Setiap file sumber baru diawali header SPDX:
  ```go
  // SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
  // Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.
  ```
- Sebelum mengirim pull request, jalankan `gofmt -l .`, `go vet ./...`, dan `go test ./...`.
- Dependency baru wajib berlisensi permissive. Perbarui juga `THIRD_PARTY_NOTICES.md` dan
  `THIRD_PARTY_LICENSES.txt` (lihat README, bagian Lisensi).
