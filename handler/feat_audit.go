package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"api-gateway/model"
)

func init() {
	http.HandleFunc("/api/feat/audit", AuditHandler)
	http.HandleFunc("/api/feat/audit/clear", AuditClearHandler)
}

func setAuditCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// AuditHandler GET /api/feat/audit 查看审计日志（仅 root）
func AuditHandler(w http.ResponseWriter, r *http.Request) {
	setAuditCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	actor := s.Username
	_ = model.AppendAudit(actor, "view", "查看审计日志")
	audits := model.LoadAudits()
	if audits == nil {
		audits = []model.AuditEntry{}
	}
	limit := 200
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if limit > 500 {
		limit = 500
	}
	if q := r.URL.Query().Get("q"); q != "" {
		ql := strings.ToLower(q)
		filtered := make([]model.AuditEntry, 0, len(audits))
		for _, e := range audits {
			if strings.Contains(strings.ToLower(e.Actor), ql) ||
				strings.Contains(strings.ToLower(e.Action), ql) ||
				strings.Contains(strings.ToLower(e.Detail), ql) {
				filtered = append(filtered, e)
			}
		}
		audits = filtered
	}
	if len(audits) > limit {
		audits = audits[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"audits": audits})
}

// AuditClearHandler POST /api/feat/audit/clear 清空审计日志（仅 root）
func AuditClearHandler(w http.ResponseWriter, r *http.Request) {
	setAuditCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	actor := s.Username
	_ = model.AppendAudit(actor, "clear", "清空审计日志")
	_ = model.ClearAudits()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}
