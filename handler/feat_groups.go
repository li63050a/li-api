package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// groupsCORS 设置跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func groupsCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func init() {
	http.HandleFunc("/admin/groups", GroupsHandler)
	http.HandleFunc("/api/stats/channels", ChannelCostHandler)
}

// GroupsHandler 处理 /admin/groups 的用户分组管理（仅 root）
// GET 列表；POST 新增/更新 {name,models:[...],ratio}；DELETE /admin/groups?name=...
func GroupsHandler(w http.ResponseWriter, r *http.Request) {
	if groupsCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(map[string]interface{}{"groups": model.GetGroups()})
	case "POST":
		var g model.Group
		if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(g.Name) == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := model.SaveGroup(g); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(g)
	case "DELETE":
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := model.DelGroup(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// chanCostItem 按上游渠道聚合的统计项
type chanCostItem struct {
	Channel  string `json:"channel"`
	Requests int64  `json:"requests"`
	Cost     int64  `json:"cost"`
}

// ChannelCostHandler GET /api/stats/channels 返回各上游渠道的请求数与花费（仅 root）
// 读取 access.log，按 upstream 字段聚合，按花费降序返回前 10
func ChannelCostHandler(w http.ResponseWriter, r *http.Request) {
	if groupsCORS(w, r) {
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

	entries, err := loadAccessEntries(maxStatsLines)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	idx := map[string]int{}
	var names []string
	var costs []int64
	var counts []int64
	for _, e := range entries {
		c, ok := e["upstream"].(string)
		if !ok || c == "" {
			continue
		}
		i, ok := idx[c]
		if !ok {
			i = len(names)
			idx[c] = i
			names = append(names, c)
			costs = append(costs, 0)
			counts = append(counts, 0)
		}
		costs[i] += statsToInt(e["cost"])
		counts[i]++
	}

	order := make([]int, len(names))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if costs[i] != costs[j] {
			return costs[i] > costs[j]
		}
		if counts[i] != counts[j] {
			return counts[i] > counts[j]
		}
		return names[i] < names[j]
	})
	if len(order) > 10 {
		order = order[:10]
	}

	items := make([]chanCostItem, 0, len(order))
	for _, i := range order {
		items = append(items, chanCostItem{Channel: names[i], Requests: counts[i], Cost: costs[i]})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"channels": items})
}