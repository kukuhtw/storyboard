// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

// Package pipeline runs each production stage:
//
//	storyboard → voiceover → audio → prompts → images → videos
//
// Every stage ends in "review"; the user edits, regenerates with feedback, or approves.
package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"storyboard/internal/ai"
	"storyboard/internal/media"
	"storyboard/internal/providers"
	"storyboard/internal/render"
	"storyboard/internal/store"
)

type Engine struct {
	Store     *store.Store
	Media     *media.Storage
	Claude    *ai.Claude // nil when Claude is disabled
	Providers *providers.Set
	Voice     string
	MaxGenMB  int64
	Renderer  *render.Renderer

	wg sync.WaitGroup
}

var videoTypeGuide = map[string]string{
	"documentation": "Video DOKUMENTASI/edukasi/tutorial: informatif, runtut, jelas, langkah demi langkah, visual menunjukkan proses nyata.",
	"advertisement": "Video IKLAN: hook kuat di 3 detik pertama, manfaat produk, bukti/emosi, call-to-action jelas di akhir.",
	"drama":         "Video DRAMA/film pendek: struktur cerita (pembuka, konflik, klimaks, resolusi), karakter konsisten, dialog/narasi emosional.",
}

// ImageStyles: project-level visual style. The value is appended to every image prompt
// and given to Claude when writing prompts.
var ImageStyles = map[string]string{
	"photo":        "photorealistic photograph, real camera, natural skin texture, true-to-life lighting and colors, DSLR, 35mm lens, high detail, no illustration",
	"cinematic":    "cinematic film still, anamorphic lens, dramatic lighting, color graded, shallow depth of field, high detail",
	"illustration": "high-quality digital illustration, clean lines, rich flat colors, consistent character design",
	"3d":           "high-quality 3D render, stylized characters, soft global illumination, Pixar-like look",
	"documentary":  "documentary photography, candid, natural available light, authentic real-world environment",
}

const systemPrompt = `Anda adalah sutradara dan penulis naskah video profesional.
Anda membantu membuat storyboard, scene, naskah voice over, dan prompt untuk model AI gambar/video.
Aturan:
- Tulis konten (judul, deskripsi, voice over) dalam bahasa yang diminta user.
- Prompt gambar & video SELALU dalam bahasa Inggris, detail, sinematik, dan konsisten antar scene (karakter, wardrobe, palet warna, lokasi).
- Jangan menyertakan data pribadi nyata (nama lengkap, NIK, nomor telepon, alamat, data nasabah) kecuali user memberikannya secara eksplisit di brief; gunakan placeholder bila perlu.
- Jangan menyalin merek dagang/karakter berhak cipta pihak lain kecuali itu produk milik user.`

// Run starts a stage asynchronously. sceneID restricts per-scene stages to one scene.
func (e *Engine) Run(projectID, kind, sceneID string) error {
	ctx := context.Background()
	ok, err := e.Store.ClaimStage(ctx, projectID, kind)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("tahap ini belum terbuka atau sedang diproses")
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				e.Store.FailStage(ctx, projectID, kind, fmt.Sprintf("internal error: %v", r))
			}
		}()
		out, err := e.run(ctx, projectID, kind, sceneID)
		if err != nil {
			slog.Warn("stage failed", "project", projectID, "stage", kind, "err", err)
			e.Store.FailStage(ctx, projectID, kind, err.Error())
			return
		}
		if err := e.Store.FinishStage(ctx, projectID, kind, out); err != nil {
			e.Store.FailStage(ctx, projectID, kind, err.Error())
		}
		e.Store.TouchProject(ctx, projectID)
	}()
	return nil
}

func (e *Engine) Wait() { e.wg.Wait() }

func (e *Engine) run(ctx context.Context, projectID, kind, sceneID string) (any, error) {
	p, err := e.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	st, err := e.Store.GetStage(ctx, projectID, kind)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "storyboard":
		return e.storyboard(ctx, p, st.Feedback)
	case "voiceover":
		return e.voiceover(ctx, p, st.Feedback, sceneID)
	case "audio":
		return e.audio(ctx, p, sceneID)
	case "prompts":
		return e.prompts(ctx, p, st.Feedback, sceneID)
	case "images":
		return e.images(ctx, p, sceneID)
	case "videos":
		return e.videos(ctx, p, sceneID)
	case "render":
		return e.render(ctx, p)
	}
	return nil, fmt.Errorf("unknown stage %q", kind)
}

func (e *Engine) needClaude() error {
	if e.Claude == nil {
		return errors.New("Claude API dinonaktifkan (CLAUDE_DISABLED=true)")
	}
	return nil
}

// refImages loads uploaded reference images Claude can view (≤5 MB, supported formats, max 8).
func (e *Engine) refImages(ctx context.Context, projectID, sceneID string) ([]ai.Image, []store.Asset) {
	refs, err := e.Store.References(ctx, projectID, sceneID)
	if err != nil {
		return nil, nil
	}
	var imgs []ai.Image
	for _, a := range refs {
		if a.Kind != "ref_image" || a.SizeBytes > 5<<20 || len(imgs) >= 8 {
			continue
		}
		switch a.Mime {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			continue
		}
		data, err := e.Media.Read(a.StorageName)
		if err != nil {
			continue
		}
		imgs = append(imgs, ai.Image{Mime: a.Mime, Data: data, Note: a.Note})
	}
	return imgs, refs
}

func refVideoNotes(refs []store.Asset) string {
	var b strings.Builder
	for _, a := range refs {
		if a.Kind == "ref_video" {
			fmt.Fprintf(&b, "- Video referensi %q: %s\n", a.OriginalName, nzs(a.Note, "(tanpa catatan)"))
		}
	}
	return b.String()
}

func projectContext(p *store.Project) string {
	return fmt.Sprintf(`PROYEK
Judul: %s
Jenis video: %s — %s
Durasi target: %d detik
Rasio: %s
Bahasa konten: %s
Tone/gaya: %s
Gaya visual gambar/video: %s (%s)
Brief:
%s
`, p.Title, p.VideoType, videoTypeGuide[p.VideoType], p.TargetDurationSec, p.AspectRatio, p.Language, nzs(p.Tone, "(bebas)"), p.ImageStyle, ImageStyles[p.ImageStyle], p.Brief)
}

func feedbackBlock(fb string) string {
	if strings.TrimSpace(fb) == "" {
		return ""
	}
	return "\nREVISI DARI USER (wajib diikuti):\n" + fb + "\n"
}

// ---------- 1. storyboard ----------

type storyboardOut struct {
	Logline     string `json:"logline"`
	VisualStyle string `json:"visual_style"`
	Characters  []struct {
		Name       string `json:"name"`
		Appearance string `json:"appearance"`
	} `json:"characters"`
	Scenes []struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Shot        string  `json:"shot"`
		DurationSec float64 `json:"duration_sec"`
	} `json:"scenes"`
}

func obj(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

var str = map[string]any{"type": "string"}
var num = map[string]any{"type": "number"}

func arr(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }

func (e *Engine) storyboard(ctx context.Context, p *store.Project, fb string) (any, error) {
	if err := e.needClaude(); err != nil {
		return nil, err
	}
	imgs, refs := e.refImages(ctx, p.ID, "")
	prompt := projectContext(p) + refVideoNotes(refs) + feedbackBlock(fb) + `
TUGAS: Buat storyboard lengkap.
- logline: ringkasan 1-2 kalimat.
- visual_style: gaya visual keseluruhan (sinematografi, palet warna, lighting, mood) — gunakan gambar referensi bila ada.
- characters: tokoh/presenter/produk utama beserta deskripsi penampilan yang konsisten (kosongkan jika tidak relevan).
- scenes: urutan scene. Total duration_sec harus mendekati durasi target. Tiap scene 3–10 detik.
  description = apa yang terjadi & terlihat; shot = jenis shot & gerakan kamera (mis. "Medium close-up, slow dolly in").`
	schema := obj(map[string]any{
		"logline":      str,
		"visual_style": str,
		"characters":   arr(obj(map[string]any{"name": str, "appearance": str})),
		"scenes":       arr(obj(map[string]any{"title": str, "description": str, "shot": str, "duration_sec": num})),
	})
	var out storyboardOut
	if err := e.Claude.GenerateJSON(ctx, systemPrompt, prompt, imgs, schema, &out); err != nil {
		return nil, err
	}
	if len(out.Scenes) == 0 {
		return nil, errors.New("Claude tidak menghasilkan scene")
	}
	scenes := make([]store.Scene, len(out.Scenes))
	for i, s := range out.Scenes {
		d := s.DurationSec
		if d <= 0 {
			d = 5
		}
		scenes[i] = store.Scene{Title: s.Title, Description: s.Description, Shot: s.Shot, DurationSec: d}
	}
	removed, err := e.Store.ReplaceScenes(ctx, p.ID, scenes)
	if err != nil {
		return nil, err
	}
	e.Media.Remove(removed...)
	return map[string]any{"logline": out.Logline, "visual_style": out.VisualStyle, "characters": out.Characters, "scene_count": len(scenes)}, nil
}

// storyboardSummary renders the approved storyboard + current scenes for later stages.
func (e *Engine) storyboardSummary(ctx context.Context, p *store.Project, scenes []store.Scene, withVO bool) string {
	var b strings.Builder
	if st, err := e.Store.GetStage(ctx, p.ID, "storyboard"); err == nil {
		fmt.Fprintf(&b, "STORYBOARD (sudah disetujui):\n%s\n", string(st.Output))
	}
	b.WriteString("SCENES:\n")
	for _, s := range scenes {
		fmt.Fprintf(&b, "[scene_id=%s] #%d %s (%.1fs)\n  Deskripsi: %s\n  Shot: %s\n", s.ID, s.Idx, s.Title, s.DurationSec, s.Description, s.Shot)
		if withVO && s.Voiceover != "" {
			fmt.Fprintf(&b, "  Voice over: %s\n", s.Voiceover)
		}
	}
	return b.String()
}

func filterScenes(scenes []store.Scene, sceneID string) []store.Scene {
	if sceneID == "" {
		return scenes
	}
	for _, s := range scenes {
		if s.ID == sceneID {
			return []store.Scene{s}
		}
	}
	return nil
}

// ---------- 2. voiceover ----------

func (e *Engine) voiceover(ctx context.Context, p *store.Project, fb, sceneID string) (any, error) {
	if err := e.needClaude(); err != nil {
		return nil, err
	}
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	target := filterScenes(scenes, sceneID)
	if len(target) == 0 {
		return nil, errors.New("tidak ada scene")
	}
	scope := "semua scene"
	if sceneID != "" {
		scope = fmt.Sprintf("HANYA scene_id=%s", sceneID)
	}
	prompt := projectContext(p) + e.storyboardSummary(ctx, p, scenes, true) + feedbackBlock(fb) + fmt.Sprintf(`
TUGAS: Tulis naskah voice over untuk %s.
- Bahasa: %s. Natural untuk dibacakan; ±2.5 kata per detik sesuai duration_sec scene.
- Untuk drama boleh berupa narasi atau dialog dengan format "NAMA: kalimat".
- Kembalikan scene_id persis seperti di atas.`, scope, p.Language)
	schema := obj(map[string]any{"items": arr(obj(map[string]any{"scene_id": str, "voiceover": str}))})
	var out struct {
		Items []struct {
			SceneID   string `json:"scene_id"`
			Voiceover string `json:"voiceover"`
		} `json:"items"`
	}
	if err := e.Claude.GenerateJSON(ctx, systemPrompt, prompt, nil, schema, &out); err != nil {
		return nil, err
	}
	valid := map[string]bool{}
	for _, s := range target {
		valid[s.ID] = true
	}
	n := 0
	for _, it := range out.Items {
		if !valid[it.SceneID] {
			continue
		}
		if err := e.Store.UpdateSceneText(ctx, p.ID, it.SceneID, map[string]any{"voiceover": it.Voiceover}); err != nil {
			return nil, err
		}
		n++
	}
	return map[string]any{"updated_scenes": n}, nil
}

// ---------- 3. audio (TTS) ----------

func (e *Engine) audio(ctx context.Context, p *store.Project, sceneID string) (any, error) {
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	n, failed := 0, []string{}
	for _, s := range filterScenes(scenes, sceneID) {
		if strings.TrimSpace(s.Voiceover) == "" {
			continue
		}
		m, err := e.Providers.TTS.Synthesize(ctx, providers.TTSRequest{Text: s.Voiceover, Voice: e.Voice, Language: p.Language})
		if err != nil {
			failed = append(failed, fmt.Sprintf("scene %d: %v", s.Idx, err))
			continue
		}
		if err := e.saveGenerated(ctx, p.ID, s.ID, "audio", m); err != nil {
			return nil, err
		}
		n++
	}
	return result(n, failed)
}

// ---------- 4. image & video prompts ----------

func (e *Engine) prompts(ctx context.Context, p *store.Project, fb, sceneID string) (any, error) {
	if err := e.needClaude(); err != nil {
		return nil, err
	}
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	target := filterScenes(scenes, sceneID)
	if len(target) == 0 {
		return nil, errors.New("tidak ada scene")
	}
	imgs, refs := e.refImages(ctx, p.ID, sceneID)
	scope := "semua scene"
	if sceneID != "" {
		scope = fmt.Sprintf("HANYA scene_id=%s", sceneID)
	}
	prompt := projectContext(p) + e.storyboardSummary(ctx, p, scenes, true) + refVideoNotes(refs) + feedbackBlock(fb) + fmt.Sprintf(`
TUGAS: Buat prompt AI untuk %s (bahasa Inggris).
- image_prompt: prompt text-to-image untuk keyframe scene: subjek, aksi, komposisi, lensa/kamera, lighting, palet warna, gaya; ulangi deskripsi karakter agar konsisten antar scene. Rasio %s.
- video_prompt: prompt image-to-video yang menganimasikan keyframe: gerakan subjek, gerakan kamera, tempo, durasi scene, atmosfer. Tanpa teks/watermark di layar.
- Manfaatkan gambar/video referensi untuk gaya & konsistensi.
- Kembalikan scene_id persis seperti di atas.`, scope, p.AspectRatio)
	schema := obj(map[string]any{"items": arr(obj(map[string]any{"scene_id": str, "image_prompt": str, "video_prompt": str}))})
	var out struct {
		Items []struct {
			SceneID     string `json:"scene_id"`
			ImagePrompt string `json:"image_prompt"`
			VideoPrompt string `json:"video_prompt"`
		} `json:"items"`
	}
	if err := e.Claude.GenerateJSON(ctx, systemPrompt, prompt, imgs, schema, &out); err != nil {
		return nil, err
	}
	valid := map[string]bool{}
	for _, s := range target {
		valid[s.ID] = true
	}
	n := 0
	for _, it := range out.Items {
		if !valid[it.SceneID] {
			continue
		}
		if err := e.Store.UpdateSceneText(ctx, p.ID, it.SceneID, map[string]any{"image_prompt": it.ImagePrompt, "video_prompt": it.VideoPrompt}); err != nil {
			return nil, err
		}
		n++
	}
	return map[string]any{"updated_scenes": n}, nil
}

// ---------- 5. images ----------

func (e *Engine) refDataURLs(ctx context.Context, projectID, sceneID, kind string, max int, maxBytes int64) []string {
	refs, _ := e.Store.References(ctx, projectID, sceneID)
	var out []string
	for _, a := range refs {
		if a.Kind != kind || a.SizeBytes > maxBytes || len(out) >= max {
			continue
		}
		if data, err := e.Media.Read(a.StorageName); err == nil {
			out = append(out, providers.DataURL(providers.Media{Mime: a.Mime, Data: data}))
		}
	}
	return out
}

func (e *Engine) images(ctx context.Context, p *store.Project, sceneID string) (any, error) {
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	n, failed := 0, []string{}
	for _, s := range filterScenes(scenes, sceneID) {
		if strings.TrimSpace(s.ImagePrompt) == "" {
			failed = append(failed, fmt.Sprintf("scene %d: image prompt kosong", s.Idx))
			continue
		}
		prompt := s.ImagePrompt
		if style := ImageStyles[p.ImageStyle]; style != "" {
			prompt += "\nStyle: " + style
		}
		m, err := e.Providers.Image.GenerateImage(ctx, providers.ImageRequest{
			Prompt: prompt, AspectRatio: p.AspectRatio,
			ReferenceImages: e.refDataURLs(ctx, p.ID, s.ID, "ref_image", 4, 10<<20),
		})
		if err != nil {
			failed = append(failed, fmt.Sprintf("scene %d: %v", s.Idx, err))
			continue
		}
		if err := e.saveGenerated(ctx, p.ID, s.ID, "image", m); err != nil {
			return nil, err
		}
		n++
	}
	return result(n, failed)
}

// ---------- 6. videos ----------

func (e *Engine) videos(ctx context.Context, p *store.Project, sceneID string) (any, error) {
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	n, failed := 0, []string{}
	for _, s := range filterScenes(scenes, sceneID) {
		req := providers.VideoRequest{
			Prompt: s.VideoPrompt, AspectRatio: p.AspectRatio, DurationSec: s.DurationSec,
			ReferenceVideos: e.refDataURLs(ctx, p.ID, s.ID, "ref_video", 1, 50<<20),
		}
		if s.ImageAssetID != nil {
			if a, err := e.Store.GetAsset(ctx, *s.ImageAssetID); err == nil {
				if data, err := e.Media.Read(a.StorageName); err == nil {
					req.StartImage = providers.DataURL(providers.Media{Mime: a.Mime, Data: data})
				}
			}
		}
		if strings.TrimSpace(req.Prompt) == "" {
			failed = append(failed, fmt.Sprintf("scene %d: video prompt kosong", s.Idx))
			continue
		}
		m, err := e.Providers.Video.GenerateVideo(ctx, req)
		if err != nil {
			failed = append(failed, fmt.Sprintf("scene %d: %v", s.Idx, err))
			continue
		}
		if err := e.saveGenerated(ctx, p.ID, s.ID, "video", m); err != nil {
			return nil, err
		}
		n++
	}
	return result(n, failed)
}

// ---------- 7. final render ----------

func (e *Engine) render(ctx context.Context, p *store.Project) (any, error) {
	if e.Renderer == nil {
		return nil, errors.New("renderer tidak dikonfigurasi")
	}
	scenes, err := e.Store.ListScenes(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if len(scenes) == 0 {
		return nil, errors.New("tidak ada scene")
	}
	var segs []render.Segment
	var warnings []string
	for _, s := range scenes {
		seg := render.Segment{VisualKind: render.VisualNone, DurationSec: s.DurationSec, Subtitle: s.Voiceover}
		pick := func(id *string) (*store.Asset, string) {
			if id == nil {
				return nil, ""
			}
			a, err := e.Store.GetAsset(ctx, *id)
			if err != nil {
				return nil, ""
			}
			path, err := e.Media.Path(a.StorageName)
			if err != nil {
				return nil, ""
			}
			return a, path
		}
		if a, path := pick(s.VideoAssetID); a != nil && strings.HasPrefix(a.Mime, "video/") {
			seg.VisualKind, seg.VisualPath = render.VisualVideo, path
		} else if a, path := pick(s.ImageAssetID); a != nil && strings.HasPrefix(a.Mime, "image/") && a.Mime != "image/svg+xml" {
			seg.VisualKind, seg.VisualPath = render.VisualImage, path
			warnings = append(warnings, fmt.Sprintf("scene %d: belum ada klip video, memakai gambar diam", s.Idx))
		} else {
			warnings = append(warnings, fmt.Sprintf("scene %d: tidak ada video/gambar yang bisa dirender (mock?), memakai layar polos", s.Idx))
		}
		if a, path := pick(s.AudioAssetID); a != nil {
			seg.AudioPath = path
		} else if strings.TrimSpace(s.Voiceover) != "" {
			warnings = append(warnings, fmt.Sprintf("scene %d: tanpa audio VO", s.Idx))
		}
		segs = append(segs, seg)
	}

	res, err := e.Renderer.Render(ctx, p.AspectRatio, segs)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(res.WorkDir)

	f, err := os.Open(res.VideoPath)
	if err != nil {
		return nil, err
	}
	name, size, err := e.Media.Save(f, "video/mp4", 4<<30)
	f.Close()
	if err != nil {
		return nil, err
	}
	video := &store.Asset{ProjectID: p.ID, Kind: "final_video", StorageName: name, Mime: "video/mp4", SizeBytes: size, Source: "generated", OriginalName: "final.mp4"}
	if err := e.Store.CreateAsset(ctx, video); err != nil {
		e.Media.Remove(name)
		return nil, err
	}
	out := map[string]any{
		"final_asset_id": video.ID, "duration_sec": math.Round(res.DurationSec*10) / 10,
		"width": res.Width, "height": res.Height, "size_bytes": size, "warnings": warnings,
	}
	if len(res.SRT) > 0 {
		sname, ssize, err := e.Media.Save(bytes.NewReader(res.SRT), "application/x-subrip", 10<<20)
		if err == nil {
			sub := &store.Asset{ProjectID: p.ID, Kind: "subtitle", StorageName: sname, Mime: "application/x-subrip", SizeBytes: ssize, Source: "generated", OriginalName: "final.srt"}
			if err := e.Store.CreateAsset(ctx, sub); err == nil {
				out["subtitle_asset_id"] = sub.ID
			} else {
				e.Media.Remove(sname)
			}
		}
	}
	return out, nil
}

// ---------- captions for social publishing ----------

type Captions struct {
	YouTubeTitle       string `json:"youtube_title"`
	YouTubeDescription string `json:"youtube_description"`
	Instagram          string `json:"instagram"`
	Threads            string `json:"threads"`
	TikTok             string `json:"tiktok"`
}

// DraftCaptions asks Claude for per-platform captions (user reviews before posting).
func (e *Engine) DraftCaptions(ctx context.Context, projectID string) (*Captions, error) {
	if err := e.needClaude(); err != nil {
		return nil, err
	}
	p, err := e.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	scenes, err := e.Store.ListScenes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	prompt := projectContext(p) + e.storyboardSummary(ctx, p, scenes, true) + fmt.Sprintf(`
TUGAS: Tulis caption publikasi video final dalam bahasa %s.
- youtube_title: maks 90 karakter, menarik, tanpa clickbait menyesatkan.
- youtube_description: 2–4 kalimat + 3–5 hashtag relevan (termasuk #Shorts).
- instagram: caption Reels, hook di kalimat pertama, CTA, 5–10 hashtag, maks 2000 karakter.
- threads: maks 450 karakter, gaya percakapan, maks 2 hashtag.
- tiktok: maks 300 karakter, hook singkat + 3–5 hashtag.
Jangan mencantumkan data pribadi, klaim berlebihan, atau janji hasil finansial.`, p.Language)
	schema := obj(map[string]any{"youtube_title": str, "youtube_description": str, "instagram": str, "threads": str, "tiktok": str})
	var out Captions
	if err := e.Claude.GenerateJSON(ctx, systemPrompt, prompt, nil, schema, &out); err != nil {
		return nil, err
	}
	out.Threads = truncate(out.Threads, 500)
	out.YouTubeTitle = truncate(out.YouTubeTitle, 100)
	return &out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------- helpers ----------

func (e *Engine) saveGenerated(ctx context.Context, projectID, sceneID, field string, m *providers.Media) error {
	if _, ok := media.ExtFor(m.Mime); !ok {
		return fmt.Errorf("provider returned unsupported type %s", m.Mime)
	}
	name, size, err := e.Media.Save(bytes.NewReader(m.Data), m.Mime, e.MaxGenMB<<20)
	if err != nil {
		return err
	}
	sid := sceneID
	a := &store.Asset{ProjectID: projectID, SceneID: &sid, Kind: field, StorageName: name, Mime: m.Mime, SizeBytes: size, Source: "generated", OriginalName: field + "-generated"}
	if err := e.Store.CreateAsset(ctx, a); err != nil {
		e.Media.Remove(name)
		return err
	}
	return e.Store.SetSceneAsset(ctx, sceneID, field, a.ID)
}

func result(n int, failed []string) (any, error) {
	if n == 0 && len(failed) > 0 {
		return nil, errors.New(strings.Join(failed, "; "))
	}
	return map[string]any{"generated": n, "failed": failed}, nil
}

func nzs(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
