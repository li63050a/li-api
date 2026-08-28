package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/setting/turnstile", TurnstileHandler)
}

// TurnstileHandler GET/POST /api/setting/turnstile
// GET: 仅 root 可读（secret 掩码为 ***+后4位）；POST: 仅 root 可写
func TurnstileHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "GET":
		siteKey, _ := model.KVGet("turnstile.site_key")
		secret, _ := model.KVGet("turnstile.secret")
		enabled, _ := model.KVGet("turnstile.enabled")
		masked := ""
		if secret != "" {
			masked = "***" + secret[len(secret)-4:]
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"site_key": siteKey,
			"secret":   masked,
			"enabled":  enabled == "1",
		})
	case "POST":
		var body struct {
			SiteKey string `json:"site_key"`
			Secret  string `json:"secret"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		enabled := "0"
		if body.Enabled {
			enabled = "1"
		}
		for _, kv := range [][2]string{
			{"turnstile.site_key", body.SiteKey},
			{"turnstile.secret", body.Secret},
			{"turnstile.enabled", enabled},
		} {
			if err := model.KVSet(kv[0], kv[1]); err != nil {
				http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// VerifyTurnstile 校验 Cloudflare Turnstile 响应；未启用时直接放行
func VerifyTurnstile(r *http.Request) bool {
	enabled, ok := model.KVGet("turnstile.enabled")
	if !ok || enabled != "1" {
		return true
	}
	siteKey, _ := model.KVGet("turnstile.site_key")
	secret, _ := model.KVGet("turnstile.secret")

	response := ""
	if err := r.ParseForm(); err == nil {
		response = r.FormValue("cf-turnstile-response")
	}
	if response == "" && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			var j struct {
				Turnstile string `json:"turnstile"`
			}
			if json.Unmarshal(body, &j) == nil {
				response = j.Turnstile
			}
		}
	}
	if response == "" {
		return false
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", response)
	form.Set("sitekey", siteKey)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", form)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var vr struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return false
	}
	return vr.Success
}