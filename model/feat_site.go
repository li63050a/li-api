package model

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"api-gateway/db"
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

// GetSite 返回当前站点配置（懒加载，无数据用默认值）
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
	if !loadSiteFromDB() {
		if migrated, _ := migrateSiteFromJSON(); migrated {
			loadSiteFromDB()
		} else {
			siteConfig = SiteConfig{}
			_ = saveSite()
		}
	}
	siteLoaded = true
	return siteConfig
}

// loadSiteFromDB 从 SQLite 读取站点配置到内存；成功返回 true
func loadSiteFromDB() bool {
	var data sql.NullString
	if err := db.DB.QueryRow(`SELECT data FROM site WHERE id=1`).Scan(&data); err != nil || !data.Valid || data.String == "" {
		return false
	}
	var s SiteConfig
	if json.Unmarshal([]byte(data.String), &s) != nil {
		return false
	}
	siteConfig = s
	return true
}

// SaveSite 保存站点配置
func SaveSite(s SiteConfig) error {
	siteMu.Lock()
	siteConfig = s
	siteLoaded = true
	err := saveSite()
	siteMu.Unlock()
	return err
}

func saveSite() error {
	data, err := json.Marshal(siteConfig)
	if err != nil {
		return err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM site"); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("INSERT INTO site(id,data) VALUES(1,?)", string(data)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// migrateSiteFromJSON 首次启动时把旧 site.json 迁移进 SQLite
func migrateSiteFromJSON() (bool, error) {
	path := filepath.Join(DataDir(), "site.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var s SiteConfig
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s); err != nil {
			return false, err
		}
	}
	siteConfig = s
	if err := saveSite(); err != nil {
		return false, err
	}
	db.RenameJSONToBak(DataDir(), "site.json")
	return true, nil
}