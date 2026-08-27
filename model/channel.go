package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrChannelNotFound 表示渠道不存在
var ErrChannelNotFound = errors.New("channel not found")

// Channel 代表一个上游渠道（仿 new-api：渠道=某个上游服务实例）
type Channel struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`       // openai / azure / anthropic / 自定义
	BaseURL   string `json:"base_url"`   // 上游基地址，如 https://api.openai.com
	Keys      string `json:"keys"`       // 多个上游密钥，逗号分隔，轮询 + 故障转移
	AuthType  string `json:"auth_type"`  // bearer / header / query
	AuthKey   string `json:"auth_key"`   // header / query 的键名
	Models    string `json:"models"`     // 支持的模型，逗号分隔，"*" 表示全部
	ModelMapping string `json:"model_mapping"` // 模型名映射 JSON：{"公开名":"上游名"}
	Group     string `json:"group"`      // 分组（令牌与渠道通过分组关联）
	Priority  int    `json:"priority"`   // 优先级，越大越优先
	Weight    int    `json:"weight"`     // 同优先级内的权重（负载比例）
	RateLimit int    `json:"rate_limit"` // 每分钟请求数，0 不限
	Status    int    `json:"status"`     // 1 启用，0 禁用
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	chanMu   sync.RWMutex
	channels []Channel
	chanNext int
)

// InitChannels 加载渠道数据
func InitChannels() error {
	path := filepath.Join(dataDir, "channels.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			channels = []Channel{}
			chanNext = 1
			return nil
		}
		return err
	}
	if len(data) == 0 {
		channels = []Channel{}
		chanNext = 1
		return nil
	}
	if err := json.Unmarshal(data, &channels); err != nil {
		return err
	}
	chanNext = 1
	for _, c := range channels {
		if c.ID >= chanNext {
			chanNext = c.ID + 1
		}
	}
	return nil
}

func saveChannels() error {
	path := filepath.Join(dataDir, "channels.json")
	data, err := json.MarshalIndent(channels, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
