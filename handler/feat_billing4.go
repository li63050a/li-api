package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

// init 注册套餐覆盖倍率 / 订阅管理 路由
func init() {
	http.HandleFunc("/api/overrides", OverridesHandler)
	http.HandleFunc("/api/subscriptions", SubscriptionsAdminHandler)
	http.HandleFunc("/api/subscriptions/reset", SubscriptionsAdminHandler)
}

// setBilling4CORS 设置跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func setBilling4CORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// OverridesHandler 套餐覆盖倍率矩阵（root）
// GET /api/overrides 返回全部 override.*（key 为 usergroup|tokengroup）
// POST /api/overrides body {usergroup, tokengroup, ratio} 写入覆盖倍率
// DELETE /api/overrides?usergroup=..&tokengroup=.. 删除覆盖倍率
func OverridesHandler(w http.ResponseWriter, r *http.Request) {
	if setBilling4CORS(w, r) {
		return
	}
	if !isRootSession(r) {
		writeErr(w, http.StatusForbidden, "Forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"success": true, "overrides": model.KVGetAll("override.")})
	case http.MethodPost:
		var req struct {
			Usergroup  string  `json:"usergroup"`
			Tokengroup string  `json:"tokengroup"`
			Ratio      float64 `json:"ratio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "Bad request")
			return
		}
		if req.Usergroup == "" || req.Tokengroup == "" || req.Ratio <= 0 {
			writeErr(w, http.StatusBadRequest, "usergroup, tokengroup and positive ratio required")
			return
		}
		if err := model.KVSet("override."+req.Usergroup+"|"+req.Tokengroup, strconv.FormatFloat(req.Ratio, 'f', -1, 64)); err != nil {
			writeErr(w, http.StatusInternalServerError, "Internal error")
			return
		}
		writeJSON(w, map[string]interface{}{"success": true})
	case http.MethodDelete:
		ug := r.URL.Query().Get("usergroup")
		tg := r.URL.Query().Get("tokengroup")
		if ug == "" || tg == "" {
			writeErr(w, http.StatusBadRequest, "usergroup and tokengroup required")
			return
		}
		if err := model.KVDel("override." + ug + "|" + tg); err != nil {
			writeErr(w, http.StatusInternalServerError, "Internal error")
			return
		}
		writeJSON(w, map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type subEntry struct {
	Username  string `json:"username"`
	Plan      string `json:"plan"`
	Expire    string `json:"expire"`
	GrantedAt string `json:"granted_at"`
}

// SubscriptionsAdminHandler 订阅管理（root）
// GET /api/subscriptions 列出全部订阅
// POST /api/subscriptions/reset body {username, plan} 删除该用户订阅
func SubscriptionsAdminHandler(w http.ResponseWriter, r *http.Request) {
	if setBilling4CORS(w, r) {
		return
	}
	if !isRootSession(r) {
		writeErr(w, http.StatusForbidden, "Forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		subs := []subEntry{}
		for username, v := range model.AllSubs() {
			var s model.Sub
			if json.Unmarshal([]byte(v), &s) != nil {
				continue
			}
			subs = append(subs, subEntry{Username: username, Plan: s.Plan, Expire: s.Expire, GrantedAt: s.GrantedAt})
		}
		sort.Slice(subs, func(i, j int) bool { return subs[i].Username < subs[j].Username })
		writeJSON(w, map[string]interface{}{"success": true, "subscriptions": subs})
	case http.MethodPost:
		if r.URL.Path != "/api/subscriptions/reset" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Username string `json:"username"`
			Plan     string `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
			writeErr(w, http.StatusBadRequest, "username required")
			return
		}
		if err := model.DelSub(req.Username); err != nil {
			writeErr(w, http.StatusInternalServerError, "Internal error")
			return
		}
		writeJSON(w, map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}