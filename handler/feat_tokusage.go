package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
)

func init() {
	http.HandleFunc("/api/token/usage", TokenUsageHandler)
}

// TokenUsageHandler GET /api/token/usage（会话）
// 返回每个令牌的用量（key 脱敏、名称、请求次数、累计消耗）。
// 普通用户仅能看到自己的令牌；root 可见全部，可用 ?user=<username> 过滤。
func TokenUsageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	isRoot := model.IsRoot(s.Username)
	filterUser := ""
	if isRoot {
		filterUser = r.URL.Query().Get("user")
	}

	tokens, err := model.GetAllTokens()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	tokenUsage.mu.Lock()
	usage := make(map[string]*struct{ Requests, Cost int64 }, len(tokenUsage.m))
	for k, v := range tokenUsage.m {
		usage[k] = &struct{ Requests, Cost int64 }{Requests: v.Requests, Cost: v.Cost}
	}
	tokenUsage.mu.Unlock()

	out := make([]map[string]interface{}, 0, len(tokens))
	for _, t := range tokens {
		if !isRoot && t.Owner != s.Username {
			continue
		}
		if filterUser != "" && t.Owner != filterUser {
			continue
		}
		e := usage[t.Key]
		if e == nil {
			e = &struct{ Requests, Cost int64 }{}
		}
		out = append(out, map[string]interface{}{
			"key_masked": maskToken(t.Key),
			"name":       t.Name,
			"requests":   e.Requests,
			"cost":       e.Cost,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["cost"].(int64) > out[j]["cost"].(int64)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}