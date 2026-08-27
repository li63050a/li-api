package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// SiteConfig 站点展示设置（扩展功能：站点信息）
type SiteConfig struct {
	Name        string `json:"name"`        // 站点名称
	Description string `json:"description"` // 站点描述
	Footer      string `json:"footer"`      // 页脚信息
	About       string `json:"about"`       // 关于内容
	Logo        string `json:"logo"`        // Logo URL
}

var (
	siteMu     sync.RWMutex
	siteConfig = SiteConfig{}
	siteLoaded bool
)

// GetSite 返回当前站点配置（懒加载，文件不存在用默认值）
func GetSite() SiteConfig {
	siteMu.RLock()
	if siteLoaded {
		c := siteConfig
		siteMu.RUnlock()
		return c
	}
	siteMu.RUnlock()

	siteMu.Lock()
	defer siteMu.Unlock()
	if siteLoaded {
		return siteConfig
	}
	path := filepath.Join(DataDir(), "site.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			siteConfig = SiteConfig{}
			siteLoaded = true
			_ = saveSite()
			return siteConfig
		}
		siteConfig = SiteConfig{}
		siteLoaded = true
		return siteConfig
	}
	if len(data) == 0 {
		siteConfig = SiteConfig{}
	} else if err := json.Unmarshal(data, &siteConfig); err != nil {
		siteConfig = SiteConfig{}
	}
	siteLoaded = true
	return siteConfig
}

// SaveSite 保存站点配置（tmp + rename 原子写入）
func SaveSite(s SiteConfig) error {
	siteMu.Lock()
	siteConfig = s
	siteLoaded = true
	err := saveSite()
	siteMu.Unlock()
	return err
}

func saveSite() error {
	path := filepath.Join(DataDir(), "site.json")
	data, err := json.MarshalIndent(siteConfig, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
