package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"api-gateway/db"
)

// Setting 全局设置（仿 new-api：自用 / 营业）
type Setting struct {
	Mode            string             `json:"mode"` // self 自用 | biz 营业
	QuotaUnit       string             `json:"quota_unit"`
	OpenRegister    bool               `json:"open_register"`    // 是否允许公开注册
	ModelRatio      map[string]float64 `json:"model_ratio"`      // 提示词倍率（营业计费用）
	CompletionRatio map[string]float64 `json:"completion_ratio"` // 补全词倍率（营业计费用，缺省时取 ModelRatio）
	SMTPHost        string             `json:"smtp_host"`        // SMTP 服务器
	SMTPPort        int                `json:"smtp_port"`        // SMTP 端口
	SMTPUser        string             `json:"smtp_user"`        // SMTP 用户名
	SMTPPass        string             `json:"smtp_pass"`        // SMTP 密码
	SMTPFrom        string             `json:"smtp_from"`        // 发件人
	NotifyEmail     string             `json:"notify_email"`     // 通知接收邮箱
}

var (
	settingMu sync.RWMutex
	setting   = Setting{Mode: "self", OpenRegister: true, ModelRatio: map[string]float64{}, CompletionRatio: map[string]float64{}}
)

// InitSettings 加载设置，不存在则写入默认值
func InitSettings() error {
	settingMu.Lock()
	defer settingMu.Unlock()

	row := db.DB.QueryRow(
		"SELECT id, mode, open_register, model_ratio, completion_ratio, smtp_host, smtp_port, smtp_user, smtp_pass, smtp_from, notify_email FROM settings WHERE id = 1",
	)
	var (
		id              int
		mode            string
		openRegister    int
		modelRatio      string
		completionRatio string
		smtpHost        string
		smtpPort        int
		smtpUser        string
		smtpPass        string
		smtpFrom        string
		notifyEmail     string
	)
	err := row.Scan(&id, &mode, &openRegister, &modelRatio, &completionRatio, &smtpHost, &smtpPort, &smtpUser, &smtpPass, &smtpFrom, &notifyEmail)
	if err != nil {
		path := filepath.Join(dataDir, "setting.json")
		if _, ferr := os.Stat(path); ferr == nil {
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if len(data) > 0 {
				if uerr := json.Unmarshal(data, &setting); uerr != nil {
					return uerr
				}
			}
			mr, merr := json.Marshal(setting.ModelRatio)
			if merr != nil {
				return merr
			}
			cr, cerr := json.Marshal(setting.CompletionRatio)
			if cerr != nil {
				return cerr
			}
			if _, ierr := db.DB.Exec(
				"INSERT INTO settings (id, mode, open_register, model_ratio, completion_ratio, smtp_host, smtp_port, smtp_user, smtp_pass, smtp_from, notify_email) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				setting.Mode, boolToInt(setting.OpenRegister), string(mr), string(cr), setting.SMTPHost, setting.SMTPPort, setting.SMTPUser, setting.SMTPPass, setting.SMTPFrom, setting.NotifyEmail,
			); ierr != nil {
				return ierr
			}
			db.RenameJSONToBak(dataDir, "setting.json")
			if setting.ModelRatio == nil {
				setting.ModelRatio = map[string]float64{}
			}
			if setting.CompletionRatio == nil {
				setting.CompletionRatio = map[string]float64{}
			}
			return nil
		}
		// no row and no file: defaults
		setting = Setting{Mode: "self", OpenRegister: true, ModelRatio: map[string]float64{}, CompletionRatio: map[string]float64{}}
		if _, derr := db.DB.Exec(
			"INSERT INTO settings (id, mode, open_register, model_ratio, completion_ratio, smtp_host, smtp_port, smtp_user, smtp_pass, smtp_from, notify_email) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			setting.Mode, boolToInt(setting.OpenRegister), "{}", "{}", setting.SMTPHost, setting.SMTPPort, setting.SMTPUser, setting.SMTPPass, setting.SMTPFrom, setting.NotifyEmail,
		); derr != nil {
			return derr
		}
		return nil
	}

	setting = Setting{
		Mode:            mode,
		OpenRegister:    openRegister != 0,
		ModelRatio:      map[string]float64{},
		CompletionRatio: map[string]float64{},
		SMTPHost:        smtpHost,
		SMTPPort:        smtpPort,
		SMTPUser:        smtpUser,
		SMTPPass:        smtpPass,
		SMTPFrom:        smtpFrom,
		NotifyEmail:     notifyEmail,
	}
	if modelRatio != "" {
		_ = json.Unmarshal([]byte(modelRatio), &setting.ModelRatio)
	}
	if completionRatio != "" {
		_ = json.Unmarshal([]byte(completionRatio), &setting.CompletionRatio)
	}
	if setting.ModelRatio == nil {
		setting.ModelRatio = map[string]float64{}
	}
	if setting.CompletionRatio == nil {
		setting.CompletionRatio = map[string]float64{}
	}
	return nil
}

func saveSettings() error {
	settingMu.RLock()
	defer settingMu.RUnlock()
	mr, err := json.Marshal(setting.ModelRatio)
	if err != nil {
		return err
	}
	cr, err := json.Marshal(setting.CompletionRatio)
	if err != nil {
		return err
	}
	if _, err := db.DB.Exec("DELETE FROM settings WHERE id = 1"); err != nil {
		return err
	}
	_, err = db.DB.Exec(
		"INSERT INTO settings (id, mode, open_register, model_ratio, completion_ratio, smtp_host, smtp_port, smtp_user, smtp_pass, smtp_from, notify_email) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		setting.Mode, boolToInt(setting.OpenRegister), string(mr), string(cr), setting.SMTPHost, setting.SMTPPort, setting.SMTPUser, setting.SMTPPass, setting.SMTPFrom, setting.NotifyEmail,
	)
	return err
}

// GetSetting 返回当前设置副本
func GetSetting() Setting {
	settingMu.RLock()
	defer settingMu.RUnlock()
	s := setting
	if s.ModelRatio == nil {
		s.ModelRatio = map[string]float64{}
	}
	if s.CompletionRatio == nil {
		s.CompletionRatio = map[string]float64{}
	}
	if s.QuotaUnit == "" {
		s.QuotaUnit = "tokens"
	}
	return s
}

// UpdateSetting 局部更新设置并持久化
func UpdateSetting(patch Setting) Setting {
	settingMu.Lock()
	if patch.Mode != "" {
		setting.Mode = patch.Mode
	}
	setting.OpenRegister = patch.OpenRegister
	if patch.ModelRatio != nil {
		setting.ModelRatio = patch.ModelRatio
	}
	if patch.CompletionRatio != nil {
		setting.CompletionRatio = patch.CompletionRatio
	}
	if patch.SMTPHost != "" {
		setting.SMTPHost = patch.SMTPHost
	}
	if patch.SMTPPort != 0 {
		setting.SMTPPort = patch.SMTPPort
	}
	if patch.SMTPUser != "" {
		setting.SMTPUser = patch.SMTPUser
	}
	if patch.SMTPPass != "" {
		setting.SMTPPass = patch.SMTPPass
	}
	if patch.SMTPFrom != "" {
		setting.SMTPFrom = patch.SMTPFrom
	}
	if patch.NotifyEmail != "" {
		setting.NotifyEmail = patch.NotifyEmail
	}
	if patch.QuotaUnit != "" {
		setting.QuotaUnit = patch.QuotaUnit
	}
	settingMu.Unlock()
	_ = saveSettings()
	settingMu.RLock()
	s := setting
	settingMu.RUnlock()
	return s
}

// ModelCost 计算某模型消耗（营业模式）：提示词 × ModelRatio + 补全词 × CompletionRatio
// 非营业模式直接返回原始 token 数之和
func ModelCost(modelName string, prompt, completion int64) int64 {
	s := GetSetting()
	if s.Mode != "biz" {
		return prompt + completion
	}
	if v, ok := KVGet("fixedprice." + modelName); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	r := s.ModelRatio[modelName]
	if r <= 0 {
		r = 1
	}
	cr := s.CompletionRatio[modelName]
	if cr <= 0 {
		cr = r
	}
	cost := int64(float64(prompt)*r + float64(completion)*cr)
	if s.QuotaUnit == "units" {
		cost *= 500000
	}
	return cost
}
