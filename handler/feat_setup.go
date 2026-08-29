package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"api-gateway/model"
)

func init() {
	http.HandleFunc("/api/public", PublicHandler)
	http.HandleFunc("/api/setup/status", SetupStatusHandler)
	http.HandleFunc("/api/setup", SetupHandler)
}

func setupCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func PublicHandler(w http.ResponseWriter, r *http.Request) {
	setupCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	siteName := model.GetSite().Name
	if siteName == "" {
		siteName = "API 网关"
	}
	announcement, _ := model.KVGet("ops.announcement")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"needs_setup":   model.CountUsers() == 0,
		"open_register": model.GetSetting().OpenRegister,
		"site_name":     siteName,
		"announcement":  announcement,
		"version":       ServerVersion,
	})
}

func SetupStatusHandler(w http.ResponseWriter, r *http.Request) {
	setupCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"needs_setup": model.CountUsers() == 0,
	})
}

func SetupHandler(w http.ResponseWriter, r *http.Request) {
	setupCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if model.CountUsers() > 0 {
		writeError(w, http.StatusForbidden, "already initialized", "invalid_request_error")
		return
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
		return
	}
	minLen := 8
	if v, ok := model.KVGet("security.min_password_len"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minLen = n
		}
	}
	if len(cred.Username) < 3 || len(cred.Password) < minLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("用户名至少3位，密码至少%d位", minLen), "invalid_request_error")
		return
	}
	if err := model.CreateUser(cred.Username, cred.Password, "root", -1); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	tok := model.CreateSession(cred.Username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token":    tok,
		"username": cred.Username,
		"role":     "root",
	})
}