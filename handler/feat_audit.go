package handler

import (
	"encoding/json"
	"net/http"

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
