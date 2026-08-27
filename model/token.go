package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// randToken 生成指定长度的随机十六进制字符串
func randToken(n int) string {
	b := make([]byte, n/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// ErrTokenInvalid 表示令牌无效或已被禁用
var ErrTokenInvalid = errors.New("token invalid")
// ErrQuotaExceeded 表示令牌额度已用完
var ErrQuotaExceeded = errors.New("quota exceeded")

// Token 代表一个面向用户的访问令牌（仿 new-api：令牌按分组关联渠道，按额度计费）
type Token struct {
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	Owner      string    `json:"owner"`       // 创建者用户名（root 可见全部）
	Group      string    `json:"group"`       // 分组，与渠道的分组对应
	Quota      int64     `json:"quota"`       // 额度（token 单位），-1 表示不限制
	Used       int64     `json:"used"`
	Unlimited  int       `json:"unlimited"`   // 1 表示不限额度
	Status     int       `json:"status"`      // 1 启用，0 禁用
	ExpiredAt  time.Time `json:"expired_at"`  // 过期时间，零值表示永不过期
	Models     string    `json:"models"`      // 允许使用的模型（逗号分隔），空表示全部
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var (
	tokenMu   sync.RWMutex
	tokens    []Token
	tokenNext int
)

// InitTokens 加载令牌数据
func InitTokens() error {
	path := filepath.Join(dataDir, "tokens.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			tokens = []Token{}
			tokenNext = 1
			return nil
		}
		return err
	}
	if len(data) == 0 {
		tokens = []Token{}
		tokenNext = 1
		return nil
	}
	if err := json.Unmarshal(data, &tokens); err != nil {
		return err
	}
	tokenNext = 1
	for _, t := range tokens {
		if len(t.Key) >= tokenNext {
			tokenNext = len(t.Key) + 1
		}
	}
	return nil
}

func saveTokens() error {
	path := filepath.Join(dataDir, "tokens.json")
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetAllTokens 返回所有令牌（管理后台展示）
func GetAllTokens() ([]Token, error) {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	out := make([]Token, len(tokens))
	copy(out, tokens)
	return out, nil
}

// InsertToken 新增令牌，未提供 key 时自动生成
func InsertToken(t *Token) (int64, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Status == 0 {
		t.Status = 1
	}
	if t.Group == "" {
		t.Group = "default"
	}
	if t.Key == "" {
		t.Key = randToken(32)
	}
	tokens = append(tokens, *t)
	if err := saveTokens(); err != nil {
		return 0, err
	}
	return int64(len(tokens)), nil
}

// UpdateToken 更新令牌
func UpdateToken(key string, t *Token) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for i := range tokens {
		if tokens[i].Key == key {
			t.CreatedAt = tokens[i].CreatedAt
			t.UpdatedAt = time.Now()
			if t.Key != "" {
				tokens[i].Key = t.Key
			}
			tokens[i].Name = t.Name
			tokens[i].Group = t.Group
			tokens[i].Quota = t.Quota
			tokens[i].Unlimited = t.Unlimited
			tokens[i].Status = t.Status
			tokens[i].ExpiredAt = t.ExpiredAt
			tokens[i].Models = t.Models
			return saveTokens()
		}
	}
	return ErrNotFound
}

// GetToken 按 key 获取令牌（不修改状态）
func GetToken(key string) (*Token, error) {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	for i := range tokens {
		if tokens[i].Key == key {
			t := tokens[i]
			return &t, nil
		}
	}
	return nil, ErrTokenInvalid
}

// IsExpired 令牌是否已过期（零值 ExpiredAt 表示永不过期）
func (t *Token) IsExpired() bool {
	return !t.ExpiredAt.IsZero() && t.ExpiredAt.Before(time.Now())
}

// TokenModelAllowed 判断令牌是否允许使用指定模型（空列表表示全部允许）
func TokenModelAllowed(models, modelName string) bool {
	if models == "" {
		return true
	}
	for _, m := range strings.Split(models, ",") {
		if strings.TrimSpace(m) == modelName {
			return true
		}
	}
	return false
}

// UseToken 校验并扣减额度（cost 为本次消耗的 token 数）
// 返回错误：令牌无效 / 禁用 / 额度不足
func UseToken(key string, cost int64) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for i := range tokens {
		if tokens[i].Key == key {
			if tokens[i].Status != 1 {
				return ErrTokenInvalid
			}
			if tokens[i].Unlimited == 0 && tokens[i].Quota >= 0 {
				if tokens[i].Used >= tokens[i].Quota {
					return ErrQuotaExceeded
				}
			}
			tokens[i].Used += cost
			tokens[i].UpdatedAt = time.Now()
			_ = saveTokens()
			return nil
		}
	}
	return ErrTokenInvalid
}

// DeleteToken 删除令牌
func DeleteToken(key string) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for i := range tokens {
		if tokens[i].Key == key {
			tokens = append(tokens[:i], tokens[i+1:]...)
			return saveTokens()
		}
	}
	return ErrNotFound
}

// CheckAndUse 校验令牌是否可用，可用则扣减一次额度（写入持久化）
func CheckAndUse(key string) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	for i := range tokens {
		if tokens[i].Key == key {
			if tokens[i].Status != 1 {
				return ErrTokenInvalid
			}
			if tokens[i].IsExpired() {
				return ErrTokenInvalid
			}
			if tokens[i].Quota >= 0 && tokens[i].Used >= tokens[i].Quota {
				return ErrQuotaExceeded
			}
			tokens[i].Used++
			tokens[i].UpdatedAt = time.Now()
			_ = saveTokens()
			return nil
		}
	}
	return ErrTokenInvalid
}
