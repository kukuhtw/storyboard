// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Storyboard-Commercial
// Copyright (C) 2026 the Storyboard Studio copyright holder. See NOTICE.

package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBytePlusImageAndVideo(t *testing.T) {
	var polls int32
	var lastImage, lastTask map[string]any
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") && r.Header.Get("Authorization") != "" {
			t.Errorf("API key must not be sent to result download URLs")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":"AuthenticationError","message":"bad key"}}`))
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v3/images/generations":
			json.NewDecoder(r.Body).Decode(&lastImage)
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"url": srvURL + "/files/img.jpg", "size": "2560x1440"}}})
		case r.Method == "POST" && r.URL.Path == "/api/v3/contents/generations/tasks":
			json.NewDecoder(r.Body).Decode(&lastTask)
			w.Write([]byte(`{"id":"cgt-1"}`))
		case r.Method == "GET" && r.URL.Path == "/api/v3/contents/generations/tasks/cgt-1":
			if atomic.AddInt32(&polls, 1) < 2 {
				w.Write([]byte(`{"id":"cgt-1","status":"running"}`))
				return
			}
			w.Write([]byte(`{"id":"cgt-1","status":"succeeded","content":{"video_url":"` + srvURL + `/files/v.mp4"}}`))
		case r.URL.Path == "/files/img.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\xff\xd8\xff\xe0jpeg"))
		case r.URL.Path == "/files/v.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("mp4data"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	bp := NewBytePlus(BytePlusConfig{BaseURL: srv.URL + "/api/v3", APIKey: "key", ImageModel: "seedream-x", VideoModel: "seedance-x", VideoResolution: "720p"})
	bp.PollEvery = 10 * time.Millisecond

	img, err := bp.GenerateImage(context.Background(), ImageRequest{Prompt: "p", AspectRatio: "16:9", ReferenceImages: []string{"data:image/png;base64,AA==", "data:image/png;base64,BB=="}})
	if err != nil || img.Mime != "image/jpeg" {
		t.Fatalf("image: %v %+v", err, img)
	}
	if lastImage["model"] != "seedream-x" || lastImage["size"] != "2560x1440" || lastImage["watermark"] != false {
		t.Fatalf("image request wrong: %v", lastImage)
	}
	if refs, ok := lastImage["image"].([]any); !ok || len(refs) != 2 {
		t.Fatalf("expected 2 reference images, got %v", lastImage["image"])
	}

	vid, err := bp.GenerateVideo(context.Background(), VideoRequest{Prompt: "move", AspectRatio: "9:16", DurationSec: 3.2, StartImage: "data:image/png;base64,AA=="})
	if err != nil || vid.Mime != "video/mp4" || string(vid.Data) != "mp4data" {
		t.Fatalf("video: %v %+v", err, vid)
	}
	if lastTask["ratio"] != "9:16" || lastTask["duration"] != float64(5) || lastTask["resolution"] != "720p" {
		t.Fatalf("task request wrong: %v", lastTask)
	}
	content := lastTask["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["role"] != "first_frame" {
		t.Fatalf("content wrong: %v", content)
	}

	bad := NewBytePlus(BytePlusConfig{BaseURL: srv.URL + "/api/v3", APIKey: "nope", ImageModel: "m"})
	if _, err := bad.GenerateImage(context.Background(), ImageRequest{Prompt: "p"}); err == nil || !strings.Contains(err.Error(), "AuthenticationError") {
		t.Fatalf("expected auth error, got %v", err)
	}
}
