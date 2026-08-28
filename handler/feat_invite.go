package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
)

func init() {
	http.HandleFunc("/admin/invites", GuardMiddleware(InviteHandler))
	http.HandleFunc("/admin/invites/", GuardMiddleware(InviteHandler))
	http.HandleFunc("/api/user/profile", ProfileHandler)
}

// InviteHandler GET/POST /admin/invites 邀请码管理（仅 root）
func InviteHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
	case "GET":
		raw := model.KVGetAll("invite.")
		out := make(map[string]model.Invite, len(raw))
		for code, v := range raw {
			var inv model.Invite
			if json.Unmarshal([]byte(v), &inv) == nil {
				out[code] = inv
			}
		}
		json.NewEncoder(w).Encode(out)
	case "POST":
		var req struct {
			Count int   `json:"count"`
			Quota int64 `json:"quota"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		codes, err := model.GenerateInvites(req.Count, req.Quota, s.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "codes": codes})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
