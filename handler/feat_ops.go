package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/setting/ops", OpsSettingsHandler)
	http.HandleFunc("/api/announcement", AnnouncementHandler)
}

func opsCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func opsWriteErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}

// OpsSettingsHandler GET/POST /api/setting/ops 读写运维配置
// GET: 返回维护状态、公告与回调地址；POST: 仅 root 可写，逐项写入 KV
func OpsSettingsHandler(w http.ResponseWriter, r *http.Request) {
	opsCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		opsWriteErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case "GET":
		maint, _ := model.KVGet("ops.maintenance")
		ann, _ := model.KVGet("ops.announcement")
		cb, _ := model.KVGet("ops.callback_url")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"maintenance":  maint == "1",
			"announcement": ann,
			"callback_url": cb,
		})
	case "POST":
		if !model.IsRoot(s.Username) {
			opsWriteErr(w, http.StatusForbidden, "forbidden")
			return
		}
		var body struct {
			Maintenance  bool   `json:"maintenance"`
			Announcement string `json:"announcement"`
			CallbackURL  string `json:"callback_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			opsWriteErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		mv := "0"
		if body.Maintenance {
			mv = "1"
		}
		for k, v := range map[string]string{
			"ops.maintenance":  mv,
			"ops.announcement": body.Announcement,
			"ops.callback_url": body.CallbackURL,
		} {
			if err := model.KVSet(k, v); err != nil {
				opsWriteErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// AnnouncementHandler GET /api/announcement 公开返回公告（无需认证）
func AnnouncementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ann, _ := model.KVGet("ops.announcement")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"announcement": ann})
}

// MaintenanceMiddleware 维护模式中间件：开启时仅 root 会话可继续，其余返回 503
func MaintenanceMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v, ok := model.KVGet("ops.maintenance"); ok && v == "1" {
			if s, ok2 := requireSession(r); ok2 && model.IsRoot(s.Username) {
				next(w, r)
				return
			}
			opsWriteErr(w, http.StatusServiceUnavailable, "maintenance")
			return
		}
		next(w, r)
	}
}

// NotifyRequestCompletion 在请求完成后把访问日志条目异步推送到 notify.callback_url
// （超时 5s，goroutine 执行，失败仅记录日志）
func NotifyRequestCompletion(entry map[string]interface{}) {
	cb, ok := model.KVGet("notify.callback_url")
	if !ok || strings.TrimSpace(cb) == "" {
		return
	}
	go func() {
		b, err := json.Marshal(entry)
		if err != nil {
			log.Printf("notify: marshal entry failed: %v", err)
			return
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(cb, "application/json", bytes.NewReader(b))
		if err != nil {
			log.Printf("notify: callback %s failed: %v", cb, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("notify: callback %s returned status %d", cb, resp.StatusCode)
		}
	}()
}
