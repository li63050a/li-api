package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// User 管理员账户（轻量实现：单文件存储，默认 root/123456）
type User struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"` // 格式 sha256(salt+pw):salt
	Role         string `json:"role"`          // root
	Status       int    `json:"status"`        // 1 启用
}

// Session 登录会话
type Session struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	userMu   sync.RWMutex
	users    []User
	userNext int
	sessions sync.Map // token -> *Session
)

// InitUsers 加载用户，不存在则创建默认 root/123456
func InitUsers() error {
	path := filepath.Join(dataDir, "user.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			u := User{ID: 1, Username: "root", Role: "root", Status: 1}
			u.PasswordHash = hashPassword("123456")
			users = []User{u}
			userNext = 2
			return saveUsers()
		}
		return err
	}
	if len(data) == 0 {
		users = []User{}
		userNext = 1
		return nil
	}
	if err := json.Unmarshal(data, &users); err != nil {
		return err
	}
	userNext = 1
	for _, u := range users {
		if u.ID >= userNext {
			userNext = u.ID + 1
		}
	}
	return nil
}

func saveUsers() error {
	path := filepath.Join(dataDir, "user.json")
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashPassword(pw string) string {
	salt := randToken(16)
	sum := sha256.Sum256([]byte(salt + pw))
	return hex.EncodeToString(sum[:]) + ":" + salt
}

func checkPassword(hash, pw string) bool {
	parts := strings.SplitN(hash, ":", 2)
	if len(parts) != 2 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[1] + pw))
	return hex.EncodeToString(sum[:]) == parts[0]
}

// VerifyUser 校验用户名密码
func VerifyUser(username, password string) (*User, bool) {
	userMu.RLock()
	defer userMu.RUnlock()
	for i := range users {
		if users[i].Username == username && users[i].Status == 1 {
			if checkPassword(users[i].PasswordHash, password) {
				u := users[i]
				return &u, true
			}
			return nil, false
		}
	}
	return nil, false
}

// CreateSession 创建会话并返回 token
func CreateSession(username string) string {
	tok := randToken(32)
	sessions.Store(tok, &Session{Token: tok, Username: username, CreatedAt: time.Now()})
	return tok
}

// InsertUser 新增用户（注册用），默认角色 user
func InsertUser(username, password string) error {
	userMu.Lock()
	defer userMu.Unlock()
	for _, u := range users {
		if u.Username == username {
			return errors.New("username already exists")
		}
	}
	u := User{ID: userNext, Username: username, Role: "user", Status: 1}
	u.PasswordHash = hashPassword(password)
	users = append(users, u)
	userNext++
	return saveUsers()
}

// IsRoot 判断用户名是否为 root（拥有全部权限）
func IsRoot(username string) bool {
	userMu.RLock()
	defer userMu.RUnlock()
	for _, u := range users {
		if u.Username == username {
			return u.Role == "root"
		}
	}
	return false
}

// GetSession 查询会话
func GetSession(token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	v, ok := sessions.Load(token)
	if !ok {
		return nil, false
	}
	return v.(*Session), true
}

// DeleteSession 注销会话
func DeleteSession(token string) {
	sessions.Delete(token)
}
