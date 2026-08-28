package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
)

func init() {
	http.HandleFunc("/admin/channels/test/batch", ChannelTestBatchHandler)
}

// runChannelTest 探测单个渠道：向渠道 base_url + /v1/models 发一个探测请求，返回状态码与响应片段。
func runChannelTest(id int) map[string]interface{} {
	ch, ok := model.GetChannel(id)
	if !ok {
		return map[string]interface{}{
			"success": false,
			"status":  http.StatusNotFound,
			"message": "channel not found",
		}
	}

	keys := ch.KeyList()
	key := ""
	if len(keys) > 0 {
		key = keys[0]
	}
	target := strings.TrimRight(ch.BaseURL, "/") + "/v1/models"
	req, _ := http.NewRequest("GET", target, nil)
	proxy := buildForwardProxy(req, target, "", ch.AuthType, ch.AuthKey, key)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	msg := rec.Body.String()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return map[string]interface{}{
		"success": rec.Code == http.StatusOK,
		"status":  rec.Code,
		"message": msg,
	}
}

// ChannelTestHandler POST /admin/channels/test/{id}
// 探测上游可用性：向渠道 base_url + /v1/models 发一个探测请求，返回状态码与响应片段（仅 root）
func ChannelTestHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

	parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if len(parts) == 0 {
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if _, ok := model.GetChannel(id); !ok {
		http.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(runChannelTest(id))
}

// ChannelTestBatchHandler POST /admin/channels/test/batch
// 批量探测渠道可用性：body {"ids":[...]}，返回每个渠道的探测结果（仅 root）
func ChannelTestBatchHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

	var body struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}
	if len(body.IDs) == 0 {
		http.Error(w, "Empty ids", http.StatusBadRequest)
		return
	}

	results := make([]map[string]interface{}, 0, len(body.IDs))
	for _, id := range body.IDs {
		res := runChannelTest(id)
		res["id"] = id
		results = append(results, res)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}
