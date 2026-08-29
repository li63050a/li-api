package handler

import (
	"encoding/json"
	"math/rand"
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
	http.HandleFunc("/api/setting/register", RegisterSettingsHandler)
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
		min, max := checkinQuotaRange()
		var quota interface{} = min
		if min != max {
			quota = strconv.FormatInt(min, 10) + "~" + strconv.FormatInt(max, 10)
		}
		writeJSON(w, map[string]interface{}{
			"enabled":   checkinEnabled(),
			"today":     today,
			"checked":   checkinDone(s.Username, today),
			"quota_min": min,
			"quota_max": max,
			"quota":     quota,
		})
	case http.MethodPost:
		checkinPost(w, s, today)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func checkinEnabled() bool {
	if v, ok := model.KVGet("checkin.enabled"); ok {
		return v == "1"
	}
	return true
}

func checkinQuotaRange() (int64, int64) {
	min, max := int64(1000), int64(1000)
	if v, ok := model.KVGet("checkin.quota_min"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			min = n
		}
	} else if v, ok := model.KVGet("settings.checkin_quota"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			min = n
		}
	}
	if v, ok := model.KVGet("checkin.quota_max"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			max = n
		}
	} else if v, ok := model.KVGet("settings.checkin_quota"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			max = n
		}
	}
	if max < min {
		max = min
	}
	return min, max
}

func checkinDone(username, today string) bool {
	v, ok := model.KVGet("checkin." + username)
	return ok && v == today
}

func checkinPost(w http.ResponseWriter, s *model.Session, today string) {
	if !checkinEnabled() {
		writeError(w, http.StatusForbidden, "签到已关闭", "invalid_request_error")
		return
	}
	if checkinDone(s.Username, today) {
		writeErr(w, http.StatusConflict, "already checked in today")
		return
	}
	min, max := checkinQuotaRange()
	quota := min
	if min < max {
		quota = min + rand.Int63n(max-min+1)
	}
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

// CheckinSettingsHandler GET/POST /api/setting/checkin 签到设置（root）
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
		min, max := checkinQuotaRange()
		writeJSON(w, map[string]interface{}{
			"enabled":   checkinEnabled(),
			"quota_min": min,
			"quota_max": max,
		})
	case http.MethodPost:
		var req struct {
			Enabled  bool  `json:"enabled"`
			QuotaMin int64 `json:"quota_min"`
			QuotaMax int64 `json:"quota_max"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		enabled := "0"
		if req.Enabled {
			enabled = "1"
		}
		if err := model.KVSet("checkin.enabled", enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "store failed")
			return
		}
		if req.QuotaMin > 0 {
			if err := model.KVSet("checkin.quota_min", strconv.FormatInt(req.QuotaMin, 10)); err != nil {
				writeErr(w, http.StatusInternalServerError, "store failed")
				return
			}
		}
		if req.QuotaMax > 0 {
			if err := model.KVSet("checkin.quota_max", strconv.FormatInt(req.QuotaMax, 10)); err != nil {
				writeErr(w, http.StatusInternalServerError, "store failed")
				return
			}
		}
		writeJSON(w, map[string]interface{}{"success": true, "enabled": req.Enabled, "quota_min": req.QuotaMin, "quota_max": req.QuotaMax})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// RegisterSettingsHandler GET/POST /api/setting/register 注册设置（root）
func RegisterSettingsHandler(w http.ResponseWriter, r *http.Request) {
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
		defaultQuota := int64(0)
		if v, ok := model.KVGet("register.default_quota"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
				defaultQuota = n
			}
		}
		minPasswordLen := 0
		if v, ok := model.KVGet("security.min_password_len"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				minPasswordLen = n
			}
		}
		writeJSON(w, map[string]interface{}{
			"default_quota":    defaultQuota,
			"min_password_len": minPasswordLen,
		})
	case http.MethodPost:
		var req struct {
			DefaultQuota   *int64 `json:"default_quota"`
			MinPasswordLen *int   `json:"min_password_len"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.DefaultQuota != nil {
			if err := model.KVSet("register.default_quota", strconv.FormatInt(*req.DefaultQuota, 10)); err != nil {
				writeErr(w, http.StatusInternalServerError, "store failed")
				return
			}
		}
		if req.MinPasswordLen != nil {
			if err := model.KVSet("security.min_password_len", strconv.Itoa(*req.MinPasswordLen)); err != nil {
				writeErr(w, http.StatusInternalServerError, "store failed")
				return
			}
		}
		writeJSON(w, map[string]interface{}{"success": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
