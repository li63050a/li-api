package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"api-gateway/model"
)

const (
	reviewKeyEnabled   = "review.enabled"
	reviewKeyChannelID = "review.channel_id"
	reviewKeyModel     = "review.model"
	reviewMaxBody      = 4 << 20
	reviewDefaultModel = "text-moderation-latest"
)

func init() {
	http.HandleFunc("/api/setting/review", ReviewSettingsHandler)
}

func ReviewSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		writeError(w, http.StatusForbidden, "forbidden", "invalid_request_error")
		return
	}
	switch r.Method {
	case "GET":
		enabled := false
		if v, ok := model.KVGet(reviewKeyEnabled); ok && v == "1" {
			enabled = true
		}
		channelID := 0
		if raw, ok := model.KVGet(reviewKeyChannelID); ok {
			fmt.Sscanf(raw, "%d", &channelID)
		}
		modelName := ""
		if v, ok := model.KVGet(reviewKeyModel); ok {
			modelName = v
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"enabled": enabled, "channel_id": channelID, "model": modelName})
	case "POST":
		var req struct {
			Enabled   bool   `json:"enabled"`
			ChannelID int    `json:"channel_id"`
			Model     string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
			return
		}
		if err := model.KVSet(reviewKeyEnabled, map[bool]string{true: "1", false: "0"}[req.Enabled]); err != nil {
			writeError(w, http.StatusInternalServerError, "save failed", "invalid_request_error")
			return
		}
		if err := model.KVSet(reviewKeyChannelID, fmt.Sprintf("%d", req.ChannelID)); err != nil {
			writeError(w, http.StatusInternalServerError, "save failed", "invalid_request_error")
			return
		}
		if err := model.KVSet(reviewKeyModel, req.Model); err != nil {
			writeError(w, http.StatusInternalServerError, "save failed", "invalid_request_error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func ModelReviewMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled := false
		if v, ok := model.KVGet(reviewKeyEnabled); ok && v == "1" {
			enabled = true
		}
		if !enabled {
			next(w, r)
			return
		}
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
		body, err := io.ReadAll(io.LimitReader(r.Body, reviewMaxBody))
		if err != nil {
			next(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

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
		text := strings.Join(texts, "\n")
		if text == "" {
			next(w, r)
			return
		}

		channelID := 0
		if raw, ok := model.KVGet(reviewKeyChannelID); ok {
			fmt.Sscanf(raw, "%d", &channelID)
		}
		ch, ok := model.GetChannel(channelID)
		if !ok {
			next(w, r)
			return
		}
		keys := ch.KeyList()
		if len(keys) == 0 {
			next(w, r)
			return
		}
		modelName := reviewDefaultModel
		if v, ok := model.KVGet(reviewKeyModel); ok && v != "" {
			modelName = v
		}
		reqBody, err := json.Marshal(map[string]interface{}{"model": modelName, "input": text})
		if err != nil {
			next(w, r)
			return
		}
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(ch.BaseURL, "/")+"/v1/moderations", bytes.NewReader(reqBody))
		if err != nil {
			next(w, r)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+keys[0])
		client := &http.Client{Timeout: 6 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			next(w, r)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			next(w, r)
			return
		}
		var modResp struct {
			Results []struct {
				Flagged bool `json:"flagged"`
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&modResp); err != nil {
			next(w, r)
			return
		}
		if len(modResp.Results) > 0 && modResp.Results[0].Flagged {
			writeError(w, http.StatusForbidden, "content filtered by model", "invalid_request_error")
			return
		}
		next(w, r)
	}
}
