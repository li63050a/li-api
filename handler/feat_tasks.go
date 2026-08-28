package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
)

func init() {
	http.HandleFunc("/api/tasks", TasksHandler)
}

// TasksHandler GET/DELETE /api/tasks（root）
// GET: 按时间倒序返回全部调度任务记录；DELETE: 清空全部 task.* KV 并重置序号
func TasksHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries := make([]map[string]interface{}, 0)
		for suffix, v := range model.KVGetAll("task.") {
			if suffix == "seq" {
				continue
			}
			var rec map[string]interface{}
			if json.Unmarshal([]byte(v), &rec) != nil {
				continue
			}
			entries = append(entries, rec)
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i]["time"].(string) > entries[j]["time"].(string)
		})
		json.NewEncoder(w).Encode(map[string]interface{}{"tasks": entries})
	case http.MethodDelete:
		for k := range model.KVGetAll("task.") {
			_ = model.KVDel("task." + k)
		}
		_ = model.KVDel("task.seq")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}