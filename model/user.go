package model

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"api-gateway/db"
	"api-gateway/redisc"
)

// User 账户（仿 new-api：首个注册用户自动成为 root 超级管理员，不预置默认账号）
type User struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"` // 格式 sha256(salt+pw):salt
	Role         string    `json:"role"`          // root | user
	Status       int       `json:"status"`        // 1 启用 0 禁用
	Quota        int64     `json:"quota"`         // 额度（token 数），-1 表示不限
	Used         int64     `json:"used"`          // 已消耗
	RateLimit    int       `json:"rate_limit"`    // 每分钟请求数限制，0 不限
	Email        string    `json:"email"`
	TwoFASecret  string    `json:"twofa_secret,omitempty"`
	TwoFAEnabled int       `json:"twofa_enabled"` // 1 启用 TOTP 双因素
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session 登录会话
type Session struct {
	Token     string    `json:"token"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	userMu         sync.RWMutex
	users          []User
	userNext       int
	sessions       sync.Map // token -> *Session
	sessionLogOnce sync.Once
)

// logSessionBackend 启动时记录一次会话后端选择（redis 或 memory）。
func logSessionBackend() {
	sessionLogOnce.Do(func() {
		if redisc.Enabled() {
			log.Printf("sessions: redis")
		} else {
			log.Printf("sessions: memory")
		}
	})
}

// InitUsers 从 SQLite 加载用户（首次运行自动从旧 JSON 迁移）。
// 不预置默认管理员；若系统尚无任何用户，可用环境变量 INIT_ROOT_USER / INIT_ROOT_PASSWORD 按需引导初始 root。
func InitUsers() error {
	logSessionBackend()
	us, err := loadUsersFromDB()
	if err != nil {
		return err
	}
	if len(us) == 0 {
		if migrated, _ := migrateUsersFromJSON(); migrated {
			us, _ = loadUsersFromDB()
		}
	}
	if len(us) == 0 {
		ru, rp := os.Getenv("INIT_ROOT_USER"), os.Getenv("INIT_ROOT_PASSWORD")
		if ru != "" && rp != "" {
			u := User{ID: 1, Username: ru, Role: "root", Status: 1, Quota: -1, Used: 0}
			u.PasswordHash = hashPassword(rp)
			users = []User{u}
			userNext = 2
			return saveUsers()
		}
		users = []User{}
		userNext = 1
		return nil
	}
	users = us
	userNext = 1
	for _, u := range users {
		if u.ID >= userNext {
			userNext = u.ID + 1
		}
	}
	return nil
}

func loadUsersFromDB() ([]User, error) {
	rows, err := db.DB.Query(`SELECT id,username,password_hash,role,status,quota,used,rate_limit,email,twofa_secret,twofa_enabled,created_at,updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created, updated sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Status, &u.Quota, &u.Used, &u.RateLimit, &u.Email, &u.TwoFASecret, &u.TwoFAEnabled, &created, &updated); err != nil {
			return nil, err
		}
		u.CreatedAt = db.StrToTime(created.String)
		u.UpdatedAt = db.StrToTime(updated.String)
		out = append(out, u)
	}
	return out, nil
}

func saveUsers() error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM users"); err != nil {
		tx.Rollback()
		return err
	}
	for _, u := range users {
		if _, err := tx.Exec(`INSERT INTO users(id,username,password_hash,role,status,quota,used,rate_limit,email,twofa_secret,twofa_enabled,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.PasswordHash, u.Role, u.Status, u.Quota, u.Used, u.RateLimit, u.Email, u.TwoFASecret, u.TwoFAEnabled, db.TimeToStr(u.CreatedAt), db.TimeToStr(u.UpdatedAt)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func migrateUsersFromJSON() (bool, error) {
	path := filepath.Join(dataDir, "user.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var us []User
	if len(data) > 0 {
		if err := json.Unmarshal(data, &us); err != nil {
			return false, err
		}
	}
	if err := saveUsersToDB(us); err != nil {
		return false, err
	}
	db.RenameJSONToBak(dataDir, "user.json")
	return true, nil
}

// saveUsersToDB 把给定用户切片整表写回 SQLite（供迁移复用）
func saveUsersToDB(us []User) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM users"); err != nil {
		tx.Rollback()
		return err
	}
	for _, u := range us {
		if _, err := tx.Exec(`INSERT INTO users(id,username,password_hash,role,status,quota,used,rate_limit,email,twofa_secret,twofa_enabled,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.PasswordHash, u.Role, u.Status, u.Quota, u.Used, u.RateLimit, u.Email, u.TwoFASecret, u.TwoFAEnabled, db.TimeToStr(u.CreatedAt), db.TimeToStr(u.UpdatedAt)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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
	if redisc.Enabled() {
		_ = redisc.Set("sess:"+tok, username, 7*24*time.Hour)
		return tok
	}
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
		u.TwoFASecret = ""
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

// CountUsers 返回当前用户总数（用于首个注册用户自动成为 root）
func CountUsers() int {
	userMu.RLock()
	defer userMu.RUnlock()
	return len(users)
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

// SetUserEmail 更新用户绑定邮箱
func SetUserEmail(username, email string) error {
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].Username == username {
			users[i].Email = email
			return saveUsers()
		}
	}
	return errors.New("user not found")
}

// GetUser2FA 返回用户 TOTP 密钥与是否启用
func GetUser2FA(username string) (secret string, enabled bool) {
	userMu.RLock()
	defer userMu.RUnlock()
	for i := range users {
		if users[i].Username == username {
			return users[i].TwoFASecret, users[i].TwoFAEnabled == 1
		}
	}
	return "", false
}

// SetUser2FA 设置用户 TOTP 密钥与启用状态
func SetUser2FA(username, secret string, enabled bool) error {
	userMu.Lock()
	defer userMu.Unlock()
	for i := range users {
		if users[i].Username == username {
			users[i].TwoFASecret = secret
			if enabled {
				users[i].TwoFAEnabled = 1
			} else {
				users[i].TwoFAEnabled = 0
			}
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
	if redisc.Enabled() {
		username, ok := redisc.Get("sess:" + token)
		if !ok {
			return nil, false
		}
		return &Session{Token: token, Username: username, CreatedAt: time.Now()}, true
	}
	v, ok := sessions.Load(token)
	if !ok {
		return nil, false
	}
	return v.(*Session), true
}

// DeleteSession 注销会话
func DeleteSession(token string) {
	if redisc.Enabled() {
		_ = redisc.Del("sess:" + token)
		return
	}
	sessions.Delete(token)
}
