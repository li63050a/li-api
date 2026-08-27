package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Setting 全局设置（仿 new-api：自用 / 营业）
type Setting struct {
	Mode          string             `json:"mode"`            // self 自用 | biz 营业
	OpenRegister  bool               `json:"open_register"`   // 是否允许公开注册
	ModelRatio    map[string]float64 `json:"model_ratio"`     // 模型倍率（营业模式下计费用）
}

var (
	settingMu sync.RWMutex
	setting   = Setting{Mode: "self", OpenRegister: true, ModelRatio: map[string]float64{}}
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
	settingMu.Unlock()
	_ = saveSettings()
	settingMu.RLock()
	s := setting
	settingMu.RUnlock()
	return s
}

// ModelCost 计算某模型消耗（按营业倍率），tokens 为原始 token 数
func ModelCost(modelName string, tokens int64) int64 {
	s := GetSetting()
	if s.Mode != "biz" {
		return tokens
	}
	ratio := s.ModelRatio[modelName]
	if ratio <= 0 {
		ratio = 1
	}
	return int64(float64(tokens) * ratio)
}
