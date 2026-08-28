package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"api-gateway/model"
)

func init() {
	http.HandleFunc("/api/presets", PresetsHandler)
	http.HandleFunc("/api/user/checkin", CheckinHandler)
	http.HandleFunc("/api/setting/checkin", CheckinSettingsHandler)
}

func presetCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

type Preset struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	System string `json:"system"`
	Prompt string `json:"prompt"`
}

// PresetsHandler GET/POST/DELETE /api/presets 聊天预设管理
func PresetsHandler(w http.ResponseWriter, r *http.Request) {
	if presetCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		presetsGet(w)
	case http.MethodPost:
		presetsPost(w, r)
	case http.MethodDelete:
		presetsDelete(w, r, s)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func presetsGet(w http.ResponseWriter) {
	all := model.KVGetAll("preset.")
	out := []Preset{}
	for _, v := range all {
		var p Preset
		if err := json.Unmarshal([]byte(v), &p); err != nil {
			continue
		}
		if p.Name == "" {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, map[string]interface{}{"presets": out})
}

func presetsPost(w http.ResponseWriter, r *http.Request) {
	var req Preset
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	data, err := json.Marshal(req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode failed")
		return
	}
	if err := model.KVSet("preset."+req.Name, string(data)); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	writeJSON(w, map[string]interface{}{"success": true})
}

func presetsDelete(w http.ResponseWriter, r *http.Request, s *model.Session) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "missing name")
		return
	}
	if err := model.KVDel("preset." + name); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, map[string]interface{}{"success": true})
}

// CheckinHandler GET/POST /api/user/checkin 每日签到
func CheckinHandler(w http.ResponseWriter, r *http.Request) {
	if presetCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		return
	}
	today := time.Now().Format("2006-01-02")
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"today":   today,
			"checked": checkinDone(s.Username, today),
			"quota":   checkinQuota(),
		})
	case http.MethodPost:
		checkinPost(w, s, today)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func checkinQuota() int64 {
	q := int64(1000)
	if v, ok := model.KVGet("settings.checkin_quota"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			q = n
		}
	}
	return q
}

func checkinDone(username, today string) bool {
	v, ok := model.KVGet("checkin." + username)
	return ok && v == today
}

func checkinPost(w http.ResponseWriter, s *model.Session, today string) {
	if checkinDone(s.Username, today) {
		writeErr(w, http.StatusConflict, "already checked in today")
		return
	}
	quota := checkinQuota()
	model.AddUserQuota(s.Username, quota)
	_ = model.AppendBilling(model.BillingEntry{
		User:   s.Username,
		Type:   "checkin",
		Amount: quota,
		Remark: "签到",
	})
	_ = model.KVSet("checkin."+s.Username, today)
	writeJSON(w, map[string]interface{}{"success": true, "quota": quota})
}

// CheckinSettingsHandler GET/POST /api/setting/checkin 签到额度设置（root）
func CheckinSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if presetCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		return
	}
	if !model.IsRoot(s.Username) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"quota": checkinQuota()})
	case http.MethodPost:
		var req struct {
			Quota int64 `json:"quota"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Quota <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid quota")
			return
		}
		if err := model.KVSet("settings.checkin_quota", fmt.Sprintf("%d", req.Quota)); err != nil {
			writeErr(w, http.StatusInternalServerError, "store failed")
			return
		}
		writeJSON(w, map[string]interface{}{"success": true, "quota": req.Quota})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}