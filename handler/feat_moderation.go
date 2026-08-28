package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"api-gateway/model"
)

const (
	moderationKeyEnabled = "moderation.enabled"
	moderationKeyWords   = "moderation.words"
	moderationMaxBody    = 4 << 20
)

var moderationDefaultWords = []string{"赌博", "毒品", "枪支", "违法"}

var (
	moderationMu    sync.Mutex
	moderationDirty = true
	moderationWords []string
)

func init() {
	http.HandleFunc("/api/setting/moderation", ModerationSettingsHandler)
}

func refreshModerationWords() {
	moderationMu.Lock()
	defer moderationMu.Unlock()
	if !moderationDirty {
		return
	}
	words := append([]string{}, moderationDefaultWords...)
	if raw, ok := model.KVGet(moderationKeyWords); ok {
		for _, w := range strings.Split(raw, ",") {
			if w = strings.TrimSpace(w); w != "" {
				words = append(words, w)
			}
		}
	}
	moderationWords = words
	moderationDirty = false
}

func moderationWriteErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}

func ModerationSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case "GET":
		enabled := false
		if v, ok := model.KVGet(moderationKeyEnabled); ok && v == "1" {
			enabled = true
		}
		var words []string
		if raw, ok := model.KVGet(moderationKeyWords); ok {
			for _, w := range strings.Split(raw, ",") {
				if w = strings.TrimSpace(w); w != "" {
					words = append(words, w)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"enabled": enabled, "words": words})
	case "POST":
		s, ok := requireSession(r)
		if !ok || !model.IsRoot(s.Username) {
			moderationWriteErr(w, http.StatusForbidden, "forbidden")
			return
		}
		var req struct {
			Enabled bool        `json:"enabled"`
			Words   interface{} `json:"words"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			moderationWriteErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		var joined string
		switch v := req.Words.(type) {
		case string:
			joined = v
		case []interface{}:
			var parts []string
			for _, w := range v {
				if s, ok := w.(string); ok {
					parts = append(parts, s)
				}
			}
			joined = strings.Join(parts, ",")
		}
		if err := model.KVSet(moderationKeyEnabled, map[bool]string{true: "1", false: "0"}[req.Enabled]); err != nil {
			moderationWriteErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		if err := model.KVSet(moderationKeyWords, joined); err != nil {
			moderationWriteErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		moderationMu.Lock()
		moderationDirty = true
		moderationMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func collectText(v interface{}, out *[]string) {
	switch t := v.(type) {
	case string:
		if t != "" {
			*out = append(*out, t)
		}
	case []interface{}:
		for _, e := range t {
			collectText(e, out)
		}
	case map[string]interface{}:
		for _, k := range []string{"text", "content", "input"} {
			if s, ok := t[k].(string); ok && s != "" {
				*out = append(*out, s)
			}
		}
	}
}

func SensitiveMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			next(w, r)
			return
		}
		ct := r.Header.Get("Content-Type")
		if ct != "" && !strings.HasPrefix(ct, "application/json") {
			next(w, r)
			return
		}
		if r.Body == nil {
			next(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, moderationMaxBody))
		if err != nil {
			next(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		enabled := false
		if v, ok := model.KVGet(moderationKeyEnabled); ok && v == "1" {
			enabled = true
		}
		if !enabled {
			next(w, r)
			return
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			next(w, r)
			return
		}
		var texts []string
		if msgs, ok := payload["messages"].([]interface{}); ok {
			for _, m := range msgs {
				if msg, ok := m.(map[string]interface{}); ok {
					collectText(msg["content"], &texts)
				}
			}
		}
		collectText(payload["prompt"], &texts)
		collectText(payload["input"], &texts)
		text := strings.ToLower(strings.Join(texts, "\n"))

		refreshModerationWords()
		moderationMu.Lock()
		words := moderationWords
		moderationMu.Unlock()
		for _, word := range words {
			if strings.Contains(text, strings.ToLower(word)) {
				moderationWriteErr(w, http.StatusForbidden, "content filtered")
				return
			}
		}
		next(w, r)
	}
}