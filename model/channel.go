package model

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"api-gateway/db"
)

// ErrChannelNotFound 表示渠道不存在
var ErrChannelNotFound = errors.New("channel not found")

// Channel 代表一个上游渠道（仿 new-api：渠道=某个上游服务实例）
type Channel struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`              // openai / azure / anthropic / 自定义
	BaseURL         string    `json:"base_url"`          // 上游基地址，如 https://api.openai.com
	Keys            string    `json:"keys"`              // 多个上游密钥，逗号分隔，轮询 + 故障转移
	AuthType        string    `json:"auth_type"`         // bearer / header / query
	AuthKey         string    `json:"auth_key"`          // header / query 的键名
	Models          string    `json:"models"`            // 支持的模型，逗号分隔，"*" 表示全部
	ModelMapping    string    `json:"model_mapping"`     // 模型名映射 JSON：{"公开名":"上游名"}
	AzureAPIVersion string    `json:"azure_api_version"` // Azure 渠道的 API 版本（如 2024-02-15-preview）
	Group           string    `json:"group"`             // 分组（令牌与渠道通过分组关联）
	Tags            string    `json:"tags"`              // 标签，逗号分隔，用于批量管理
	Priority        int       `json:"priority"`          // 优先级，越大越优先
	Weight          int       `json:"weight"`            // 同优先级内的权重（负载比例）
	RateLimit       int       `json:"rate_limit"`        // 每分钟请求数，0 不限
	Status          int       `json:"status"`            // 1 启用，0 禁用
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

var (
	chanMu   sync.RWMutex
	channels []Channel
	chanNext int
)

// InitChannels 从 SQLite 加载渠道（首次运行自动从旧 JSON 迁移）
func InitChannels() error {
	cs, err := loadChannelsFromDB()
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		if migrated, _ := migrateChannelsFromJSON(); migrated {
			cs, _ = loadChannelsFromDB()
		}
	}
	channels = cs
	chanNext = 1
	for _, c := range channels {
		if c.ID >= chanNext {
			chanNext = c.ID + 1
		}
	}
	return nil
}

func loadChannelsFromDB() ([]Channel, error) {
	rows, err := db.DB.Query(`SELECT id,name,type,base_url,keys,auth_type,auth_key,models,model_mapping,azure_api_version,grp,tags,priority,weight,rate_limit,status,created_at,updated_at FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var created, updated sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.Keys, &c.AuthType, &c.AuthKey, &c.Models, &c.ModelMapping, &c.AzureAPIVersion, &c.Group, &c.Tags, &c.Priority, &c.Weight, &c.RateLimit, &c.Status, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt = db.StrToTime(created.String)
		c.UpdatedAt = db.StrToTime(updated.String)
		out = append(out, c)
	}
	return out, nil
}

func saveChannels() error {
	return saveChannelsToDB(channels)
}

func saveChannelsToDB(cs []Channel) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM channels"); err != nil {
		tx.Rollback()
		return err
	}
	for _, c := range cs {
		if _, err := tx.Exec(`INSERT INTO channels(id,name,type,base_url,keys,auth_type,auth_key,models,model_mapping,azure_api_version,grp,tags,priority,weight,rate_limit,status,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.Type, c.BaseURL, c.Keys, c.AuthType, c.AuthKey, c.Models, c.ModelMapping, c.AzureAPIVersion, c.Group, c.Tags, c.Priority, c.Weight, c.RateLimit, c.Status, db.TimeToStr(c.CreatedAt), db.TimeToStr(c.UpdatedAt)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func migrateChannelsFromJSON() (bool, error) {
	path := filepath.Join(dataDir, "channels.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var cs []Channel
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cs); err != nil {
			return false, err
		}
	}
	if err := saveChannelsToDB(cs); err != nil {
		return false, err
	}
	db.RenameJSONToBak(dataDir, "channels.json")
	return true, nil
}

// GetAllChannels 返回所有启用的渠道
func GetAllChannels() ([]Channel, error) {
	chanMu.RLock()
	defer chanMu.RUnlock()
	out := make([]Channel, 0, len(channels))
	for _, c := range channels {
		if c.Status == 1 {
			out = append(out, c)
		}
	}
	return out, nil
}

// GetAllChannelsRaw 返回全部渠道（含禁用），用于管理后台
func GetAllChannelsRaw() ([]Channel, error) {
	chanMu.RLock()
	defer chanMu.RUnlock()
	out := make([]Channel, len(channels))
	copy(out, channels)
	return out, nil
}

// GetChannel 按 id 获取单个渠道
func GetChannel(id int) (Channel, bool) {
	chanMu.RLock()
	defer chanMu.RUnlock()
	for _, c := range channels {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
}

// GetChannelsByTag 按标签返回渠道（大小写不敏感的包含匹配）
func GetChannelsByTag(tag string) []Channel {
	chanMu.RLock()
	defer chanMu.RUnlock()
	needle := strings.ToLower(strings.TrimSpace(tag))
	out := make([]Channel, 0)
	if needle == "" {
		return out
	}
	for _, c := range channels {
		if strings.Contains(strings.ToLower(c.Tags), needle) {
			out = append(out, c)
		}
	}
	return out
}

// InsertChannel 新增渠道
func InsertChannel(c *Channel) (int64, error) {
	chanMu.Lock()
	defer chanMu.Unlock()
	c.ID = chanNext
	chanNext++
	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now
	if c.Weight <= 0 {
		c.Weight = 1
	}
	channels = append(channels, *c)
	if err := saveChannels(); err != nil {
		return 0, err
	}
	return int64(c.ID), nil
}

// UpdateChannel 更新渠道
func UpdateChannel(id int, c *Channel) error {
	chanMu.Lock()
	defer chanMu.Unlock()
	for i := range channels {
		if channels[i].ID == id {
			c.ID = id
			c.CreatedAt = channels[i].CreatedAt
			c.UpdatedAt = time.Now()
			if c.Weight <= 0 {
				c.Weight = 1
			}
			channels[i] = *c
			return saveChannels()
		}
	}
	return ErrChannelNotFound
}

// DeleteChannel 删除渠道
func DeleteChannel(id int) error {
	chanMu.Lock()
	defer chanMu.Unlock()
	for i := range channels {
		if channels[i].ID == id {
			channels = append(channels[:i], channels[i+1:]...)
			return saveChannels()
		}
	}
	return ErrChannelNotFound
}

// KeyList 返回该渠道的上游密钥列表
func (c *Channel) KeyList() []string {
	if c.Keys == "" {
		return nil
	}
	parts := strings.Split(c.Keys, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SupportsModel 判断该渠道是否支持给定模型
func (c *Channel) SupportsModel(model string) bool {
	if c.Models == "" || c.Models == "*" {
		return true
	}
	for _, m := range strings.Split(c.Models, ",") {
		if strings.TrimSpace(m) == model {
			return true
		}
	}
	return false
}
