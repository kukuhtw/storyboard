// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package social

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tempVideo(t *testing.T, size int) (string, int64) {
	p := filepath.Join(t.TempDir(), "v.mp4")
	os.WriteFile(p, make([]byte, size), 0o600)
	return p, int64(size)
}

func TestYouTubePublish(t *testing.T) {
	var meta map[string]any
	var uploaded int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/upload/videos"):
			if r.URL.Query().Get("uploadType") != "resumable" || r.Header.Get("X-Upload-Content-Length") != "1000" {
				w.WriteHeader(400)
				return
			}
			json.NewDecoder(r.Body).Decode(&meta)
			w.Header().Set("Location", srv.URL+"/session/1")
		case r.Method == "PUT" && r.URL.Path == "/session/1":
			n, _ := io.Copy(io.Discard, r.Body)
			uploaded = n
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"vid123"}`))
		}
	}))
	defer srv.Close()
	y := NewYouTube("id", "sec", "https://app/oauth/youtube/callback")
	y.UploadBase = srv.URL + "/upload"
	path, size := tempVideo(t, 1000)
	res, err := y.Publish(context.Background(), &Token{Access: "at"}, PublishRequest{VideoPath: path, VideoSize: size, Title: "Judul <x>", Caption: "Deskripsi", Privacy: "public", AIGenerated: true})
	if err != nil || res.ExternalID != "vid123" || res.URL != "https://www.youtube.com/shorts/vid123" || uploaded != 1000 {
		t.Fatalf("publish: %v %+v uploaded=%d", err, res, uploaded)
	}
	st := meta["status"].(map[string]any)
	sn := meta["snippet"].(map[string]any)
	if st["containsSyntheticMedia"] != true || st["privacyStatus"] != "public" || sn["categoryId"] != "22" || sn["title"] != "Judul x" || !strings.Contains(sn["description"].(string), "#Shorts") {
		t.Fatalf("metadata wrong: %v", meta)
	}
	if u := y.AuthURL("st"); !strings.Contains(u, "access_type=offline") || !strings.Contains(u, "youtube.upload") {
		t.Fatal(u)
	}
}

func TestTikTokPublishChunked(t *testing.T) {
	var initBody map[string]any
	var ranges []string
	var polls int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/post/publish/creator_info/query/":
			w.Write([]byte(`{"data":{"creator_username":"brand","privacy_level_options":["PUBLIC_TO_EVERYONE","SELF_ONLY"],"max_video_post_duration_sec":180},"error":{"code":"ok"}}`))
		case "/v2/post/publish/video/init/":
			json.NewDecoder(r.Body).Decode(&initBody)
			w.Write([]byte(`{"data":{"publish_id":"pub1","upload_url":"` + srv.URL + `/upload?x=1"},"error":{"code":"ok"}}`))
		case "/upload":
			ranges = append(ranges, r.Header.Get("Content-Range"))
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(206)
		case "/v2/post/publish/status/fetch/":
			if atomic.AddInt32(&polls, 1) < 2 {
				w.Write([]byte(`{"data":{"status":"PROCESSING_UPLOAD"},"error":{"code":"ok"}}`))
				return
			}
			w.Write([]byte(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":[7300000000000000001]},"error":{"code":"ok"}}`))
		}
	}))
	defer srv.Close()
	tk := NewTikTok("k", "s", "https://app/oauth/tiktok/callback")
	tk.APIBase, tk.PollEvery = srv.URL+"/v2", 5*time.Millisecond
	path, size := tempVideo(t, 65_000_123) // > 64 MB → chunked
	res, err := tk.Publish(context.Background(), &Token{Access: "at"}, PublishRequest{VideoPath: path, VideoSize: size, DurationSec: 30, Caption: "cap", Privacy: "public", AIGenerated: true})
	if err != nil || res.URL != "https://www.tiktok.com/@brand/video/7300000000000000001" {
		t.Fatalf("publish: %v %+v", err, res)
	}
	si := initBody["source_info"].(map[string]any)
	pi := initBody["post_info"].(map[string]any)
	if si["chunk_size"] != float64(10_000_000) || si["total_chunk_count"] != float64(6) || pi["is_aigc"] != true || pi["privacy_level"] != "PUBLIC_TO_EVERYONE" {
		t.Fatalf("init body: %v", initBody)
	}
	if len(ranges) != 6 || ranges[0] != "bytes 0-9999999/65000123" || ranges[5] != "bytes 50000000-65000122/65000123" {
		t.Fatalf("ranges: %v", ranges)
	}
	// duration over account limit
	if _, err := tk.Publish(context.Background(), &Token{Access: "at"}, PublishRequest{VideoPath: path, VideoSize: size, DurationSec: 500, Privacy: "public"}); err == nil {
		t.Fatal("expected duration error")
	}
}

func TestMetaPublishInstagramAndThreads(t *testing.T) {
	for _, name := range []string{"instagram", "threads"} {
		var created map[string][]string
		var polls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer lt" {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":{"message":"Invalid OAuth access token","code":190}}`))
				return
			}
			r.ParseForm()
			switch {
			case r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/u1/media") || strings.HasSuffix(r.URL.Path, "/u1/threads")):
				created = r.PostForm
				w.Write([]byte(`{"id":"c1"}`))
			case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/c1"):
				if atomic.AddInt32(&polls, 1) < 2 {
					w.Write([]byte(`{"status_code":"IN_PROGRESS","status":"IN_PROGRESS"}`))
					return
				}
				w.Write([]byte(`{"status_code":"FINISHED","status":"FINISHED"}`))
			case r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/media_publish") || strings.HasSuffix(r.URL.Path, "/threads_publish")):
				if r.PostForm.Get("creation_id") != "c1" {
					w.WriteHeader(400)
					return
				}
				w.Write([]byte(`{"id":"m1"}`))
			case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/m1"):
				w.Write([]byte(`{"permalink":"https://example.com/p/m1"}`))
			}
		}))
		var m *metaPlatform
		if name == "instagram" {
			m = NewInstagram("a", "s", "https://app/cb", "")
		} else {
			m = NewThreads("a", "s", "https://app/cb", "")
		}
		m.graphHost, m.PollEvery = srv.URL, 5*time.Millisecond
		res, err := m.Publish(context.Background(), &Token{Access: "lt"}, PublishRequest{ExternalUserID: "u1", PublicURL: "https://app/public/media/x?sig=1", Caption: "halo", DurationSec: 30, AIGenerated: true})
		srv.Close()
		if err != nil || res.ExternalID != "m1" || res.URL != "https://example.com/p/m1" {
			t.Fatalf("%s publish: %v %+v", name, err, res)
		}
		if name == "instagram" && (created["media_type"][0] != "REELS" || created["is_ai_generated"][0] != "true" || created["caption"][0] != "halo") {
			t.Fatalf("ig container: %v", created)
		}
		if name == "threads" && (created["media_type"][0] != "VIDEO" || created["is_made_with_ai"][0] != "true" || created["text"][0] != "halo") {
			t.Fatalf("threads container: %v", created)
		}
	}
}

func TestErrorsDoNotLeakTokens(t *testing.T) {
	err := getJSON(context.Background(), "http://127.0.0.1:1/refresh_access_token?access_token=SUPERSECRET", nil, nil)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

func TestTikTokChunks(t *testing.T) {
	if c, n := tiktokChunks(4_000_000); c != 4_000_000 || n != 1 {
		t.Fatal(c, n)
	}
	if c, n := tiktokChunks(64_000_000); c != 64_000_000 || n != 1 {
		t.Fatal(c, n)
	}
	if c, n := tiktokChunks(129_999_999); c != 10_000_000 || n != 12 {
		t.Fatal(c, n)
	}
}
