package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"time"
)

func init() {
	http.HandleFunc("/api/feat/tokens/batch", BatchTokenHandler)
}

// BatchTokenHandler 处理 POST /api/feat/tokens/batch：批量生成令牌
func BatchTokenHandler(w http.ResponseWriter, r *http.Request) {
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
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		Count      int    `json:"count"`
		NamePrefix string `json:"name_prefix"`
		Group      string `json:"group"`
		Quota      int64  `json:"quota"`
		Models     string `json:"models"`
		ExpiredAt  string `json:"expired_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Count < 1 || req.Count > 100 {
		http.Error(w, "count must be between 1 and 100", http.StatusBadRequest)
		return
	}

	group := req.Group
	if group == "" {
		group = "default"
	}

	var expiredAt time.Time
	if req.ExpiredAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiredAt)
		if err != nil {
			http.Error(w, "invalid expired_at, expected RFC3339", http.StatusBadRequest)
			return
		}
		expiredAt = t
	}

	type tokOut struct {
		Key    string `json:"key"`
		Name   string `json:"name"`
		Group  string `json:"group"`
		Quota  int64  `json:"quota"`
		Models string `json:"models"`
	}

	tokens := make([]tokOut, 0, req.Count)
	for i := 1; i <= req.Count; i++ {
		t := model.Token{
			Name:      req.NamePrefix + itoa(i),
			Owner:     s.Username,
			Group:     group,
			Quota:     req.Quota,
			Unlimited: boolToInt(req.Quota < 0),
			Status:    1,
			Models:    req.Models,
			ExpiredAt: expiredAt,
		}
		if _, err := model.InsertToken(&t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tokens = append(tokens, tokOut{
			Key:    t.Key,
			Name:   t.Name,
			Group:  t.Group,
			Quota:  t.Quota,
			Models: t.Models,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"tokens":  tokens,
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
