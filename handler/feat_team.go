package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
)

func init() {
	http.HandleFunc("/admin/team", TeamAdminHandler)
	http.HandleFunc("/admin/team/member", TeamAdminHandler)
	http.HandleFunc("/api/team", TeamSelfHandler)
}

// teamCORS 设置跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func teamCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// TeamAdminHandler 管理团队（仅 root）
// POST /admin/team/member 创建团队成员 {username,password,parent}
// GET /admin/team 返回全部设置了 parent 的用户及其额度使用情况
func TeamAdminHandler(w http.ResponseWriter, r *http.Request) {
	if teamCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "GET":
		var members []map[string]interface{}
		for _, u := range model.GetAllUsers() {
			if u.Parent == "" {
				continue
			}
			members = append(members, map[string]interface{}{
				"username": u.Username,
				"parent":   u.Parent,
				"quota":    u.Quota,
				"used":     u.Used,
				"status":   u.Status,
			})
		}
		writeJSON(w, map[string]interface{}{"members": members})

	case "POST":
		if r.URL.Path != "/admin/team/member" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Parent   string `json:"parent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
			writeErr(w, http.StatusBadRequest, "username and password required")
			return
		}
		if req.Parent == "" {
			writeErr(w, http.StatusBadRequest, "parent required")
			return
		}
		if err := model.CreateUser(req.Username, req.Password, "user", 0); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := model.SetUserParent(req.Username, req.Parent); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"success": true, "username": req.Username, "parent": req.Parent})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// TeamSelfHandler GET /api/team 返回当前用户的团队成员（任意登录用户）
func TeamSelfHandler(w http.ResponseWriter, r *http.Request) {
	if teamCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, map[string]interface{}{"children": model.GetUsersByParent(s.Username)})
}