package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// Token 代表一个面向用户的访问令牌，可设置额度（请求次数）
type Token struct {
	Key    string    `json:"key"`
	Name   string    `json:"name"`
	Quota  int       `json:"quota"`  // 允许的最大请求数，-1 表示不限制
	Used   int       `json:"used"`
	Status int       `json:"status"` // 1 启用，0 禁用
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
			tokens[i].Quota = t.Quota
			tokens[i].Status = t.Status
			return saveTokens()
		}
	}
	return ErrNotFound
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
