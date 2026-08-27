package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LogsHandler GET /api/logs 查询访问日志（JSONL）
// 支持过滤：model / status / group；分页：page / limit
func LogsHandler(w http.ResponseWriter, r *http.Request) {
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
	_ = s

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 50
	}
	fModel := r.URL.Query().Get("model")
	fStatus := r.URL.Query().Get("status")
	fGroup := r.URL.Query().Get("group")

	path := filepath.Join(model.DataDir(), "access.log")
	f, err := os.Open(path)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"total": 0, "page": page, "limit": limit, "logs": []interface{}{}})
		return
	}
	defer f.Close()

	var all []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if fModel != "" && fmtStr(entry["model"]) != fModel {
			continue
		}
		if fStatus != "" && fmtStr(entry["status"]) != fStatus {
			continue
		}
		if fGroup != "" && fmtStr(entry["group"]) != fGroup {
			continue
		}
		all = append(all, entry)
	}

	// 最新在前
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	total := len(all)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	pageData := all[start:end]
	if pageData == nil {
		pageData = []map[string]interface{}{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total": total,
		"page":  page,
		"limit": limit,
		"logs":  pageData,
	})
}

func fmtStr(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}
