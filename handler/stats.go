package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

var (
	statMu   sync.Mutex
	statDate string
	statReq  int64
	statTok  int64
)

// recordStats 记录一次请求的统计（按自然日重置）
func recordStats(tokens int64) {
	today := time.Now().Format("2006-01-02")
	statMu.Lock()
	if statDate != today {
		statDate = today
		statReq = 0
		statTok = 0
	}
	statReq++
	statTok += tokens
	statMu.Unlock()
}

// DashboardHandler GET /api/dashboard 返回运行概览（登录即可访问，root 可见全部）
func DashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	channels, _ := model.GetAllChannels()
	total := len(channels)
	enabled := 0
	for _, c := range channels {
		if c.Status == 1 {
			enabled++
		}
	}
	autoDisabledCount := 0
	autoDisabled.Range(func(_, _ interface{}) bool {
		autoDisabledCount++
		return true
	})

	statMu.Lock()
	defer statMu.Unlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"date":                 statDate,
		"today_requests":       statReq,
		"today_tokens":         statTok,
		"channels_total":       total,
		"channels_enabled":     enabled,
		"channels_auto_disabled": autoDisabledCount,
		"users_total":          len(model.GetAllUsers()),
		"is_root":              model.IsRoot(s.Username),
	})
}
