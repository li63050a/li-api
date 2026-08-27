package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Setting 全局设置（仿 new-api：自用 / 营业）
type Setting struct {
	Mode             string             `json:"mode"`               // self 自用 | biz 营业
	OpenRegister     bool               `json:"open_register"`      // 是否允许公开注册
	ModelRatio       map[string]float64 `json:"model_ratio"`        // 提示词倍率（营业计费用）
	CompletionRatio  map[string]float64 `json:"completion_ratio"`   // 补全词倍率（营业计费用，缺省时取 ModelRatio）
}

var (
	settingMu sync.RWMutex
	setting   = Setting{Mode: "self", OpenRegister: true, ModelRatio: map[string]float64{}, CompletionRatio: map[string]float64{}}
)

// InitSettings 加载设置，不存在则写入默认值
func InitSettings() error {
	path := filepath.Join(dataDir, "setting.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return saveSettings()
		}
		return err
	}
	if len(data) == 0 {
		return saveSettings()
	}
	settingMu.Lock()
	defer settingMu.Unlock()
	if err := json.Unmarshal(data, &setting); err != nil {
		return err
	}
	if setting.ModelRatio == nil {
		setting.ModelRatio = map[string]float64{}
	}
	return nil
}

func saveSettings() error {
	settingMu.RLock()
	defer settingMu.RUnlock()
	path := filepath.Join(dataDir, "setting.json")
	data, err := json.MarshalIndent(setting, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	r := s.ModelRatio[modelName]
	if r <= 0 {
		r = 1
	}
	cr := s.CompletionRatio[modelName]
	if cr <= 0 {
		cr = r
	}
	return int64(float64(prompt)*r + float64(completion)*cr)
}
