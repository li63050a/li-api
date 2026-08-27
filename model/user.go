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
	Role         string `json:"role"`          // root | user
	Status       int    `json:"status"`        // 1 启用 0 禁用
	Quota        int64  `json:"quota"`         // 额度（token 数），-1 表示不限
	Used         int64  `json:"used"`          // 已消耗
	RateLimit    int    `json:"rate_limit"`    // 每分钟请求数限制，0 不限
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

// InsertUser 新增用户（注册用），默认角色 user，额度 0
func InsertUser(username, password string) error {
	return CreateUser(username, password, "user", 0)
}

// CreateUser 管理员创建用户（可指定角色与额度）
func CreateUser(username, password, role string, quota int64) error {
	userMu.Lock()
	defer userMu.Unlock()
	for _, u := range users {
		if u.Username == username {
			return errors.New("username already exists")
		}
	}
	u := User{ID: userNext, Username: username, Role: role, Status: 1, Quota: quota, Used: 0}
	u.PasswordHash = hashPassword(password)
	users = append(users, u)
	userNext++
	return saveUsers()
}

// GetAllUsers 返回全部用户（脱敏：不含密码哈希）
func GetAllUsers() []User {
	userMu.RLock()
	defer userMu.RUnlock()
	cp := make([]User, len(users))
	for i := range users {
		u := users[i]
		u.PasswordHash = ""
		cp[i] = u
	}
	return cp
}

// GetUserByUsername 按用户名查询
func GetUserByUsername(name string) (*User, bool) {
	userMu.RLock()
	defer userMu.RUnlock()
	for i := range users {
		if users[i].Username == name {
			u := users[i]
			return &u, true
		}
	}
	return nil, false
}

// UpdateUser 更新用户资料（角色 / 状态 / 额度 / 密码）
func UpdateUser(id int, patch User) error {
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].ID == id {
			if patch.Username != "" {
				users[i].Username = patch.Username
			}
			if patch.Role != "" {
				users[i].Role = patch.Role
			}
			if patch.PasswordHash != "" {
				users[i].PasswordHash = patch.PasswordHash
			}
			users[i].Status = patch.Status
			users[i].Quota = patch.Quota
			users[i].RateLimit = patch.RateLimit
			return saveUsers()
		}
	}
	return errors.New("user not found")
}

// AddUserQuota 给用户增加额度（充值码 / 管理员调整用，可正可负）
func AddUserQuota(name string, n int64) {
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].Username == name {
			users[i].Quota += n
			_ = saveUsers()
			return
		}
	}
}

// UserQuotaAllowed 预检用户额度是否还可继续请求（转发前调用，不扣减）
// 用户不存在、被禁用或额度已耗尽时返回 false
func UserQuotaAllowed(name string) bool {
	if name == "" {
		return true
	}
	userMu.RLock()
	defer userMu.RUnlock()
	for i := range users {
		if users[i].Username == name {
			if users[i].Status != 1 {
				return false
			}
			if users[i].Quota >= 0 && users[i].Used >= users[i].Quota {
				return false
			}
			return true
		}
	}
	return true
}

// AddUserUsed 记账式扣减用户已用额度（响应完成后调用）
func AddUserUsed(name string, n int64) {
	if name == "" {
		return
	}
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].Username == name {
			users[i].Used += n
			_ = saveUsers()
			return
		}
	}
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

// HashPassword 导出密码哈希函数（供 handler 在更新密码时使用）
func HashPassword(pw string) string {
	return hashPassword(pw)
}

// DeleteUser 删除指定用户（root 不可被删除）
func DeleteUser(id int) error {
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].ID == id {
			if users[i].Role == "root" {
				return errors.New("不能删除 root 账户")
			}
			users = append(users[:i], users[i+1:]...)
			return saveUsers()
		}
	}
	return errors.New("user not found")
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
