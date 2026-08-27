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

// ErrNotFound 表示路由不存在
var ErrNotFound = errors.New("route not found")

// DataDir 返回当前数据存储目录
func DataDir() string {
	return dataDir
}

// Keys 返回该路由的上游密钥列表（优先 AuthValues，其次 AuthValue）
func (r *Route) Keys() []string {
	if r.AuthValues != "" {
		parts := strings.Split(r.AuthValues, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if r.AuthValue != "" {
		return []string{r.AuthValue}
	}
	return nil
}

// Route 描述一条转发路由
type Route struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Prefix       string    `json:"prefix"`
	UpstreamURL  string    `json:"upstream_url"`
	AuthType     string    `json:"auth_type"`    // none, bearer, header, query
	AuthKey      string    `json:"auth_key"`
	AuthValue    string    `json:"auth_value"`
	AuthValues   string    `json:"auth_values"`  // 多个上游密钥，逗号分隔，轮流使用（故障转移）
	Timeout      int       `json:"timeout"`      // 秒
	NeedAPIKey   bool      `json:"need_api_key"`
	AllowedPaths string    `json:"allowed_paths"` // 逗号分隔，为空表示全部放行
	RateLimit    int       `json:"rate_limit"`    // 每分钟请求数，0 表示不限
	Enable       bool      `json:"enable"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

var (
	mu     sync.RWMutex
	routes []Route
	nextID int
	dataDir = "data"
)

// Init 加载数据目录，打开 SQLite，并从库中加载路由（首次运行自动从旧 JSON 迁移）
func Init() error {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if err := db.Open(dataDir); err != nil {
		return err
	}
	rs, err := loadRoutesFromDB()
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		if migrated, merr := migrateRoutesFromJSON(); merr == nil && migrated {
			rs, _ = loadRoutesFromDB()
		}
	}
	routes = rs
	nextID = 1
	for _, r := range routes {
		if r.ID >= nextID {
			nextID = r.ID + 1
		}
	}
	return nil
}

func loadRoutesFromDB() ([]Route, error) {
	rows, err := db.DB.Query(`SELECT id,name,prefix,upstream_url,auth_type,auth_key,auth_value,auth_values,timeout,need_api_key,allowed_paths,rate_limit,enable,created_at,updated_at FROM routes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		var r Route
		var created, updated sql.NullString
		var needAPIKey, enable int
		if err := rows.Scan(&r.ID, &r.Name, &r.Prefix, &r.UpstreamURL, &r.AuthType, &r.AuthKey, &r.AuthValue, &r.AuthValues, &r.Timeout, &needAPIKey, &r.AllowedPaths, &r.RateLimit, &enable, &created, &updated); err != nil {
			return nil, err
		}
		r.NeedAPIKey = needAPIKey != 0
		r.Enable = enable != 0
		r.CreatedAt = db.StrToTime(created.String)
		r.UpdatedAt = db.StrToTime(updated.String)
		out = append(out, r)
	}
	return out, nil
}

func save() error {
	return saveRoutesToDB(routes)
}

func saveRoutesToDB(rs []Route) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM routes"); err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rs {
		if _, err := tx.Exec(`INSERT INTO routes(id,name,prefix,upstream_url,auth_type,auth_key,auth_value,auth_values,timeout,need_api_key,allowed_paths,rate_limit,enable,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.Name, r.Prefix, r.UpstreamURL, r.AuthType, r.AuthKey, r.AuthValue, r.AuthValues, r.Timeout, boolToInt(r.NeedAPIKey), r.AllowedPaths, r.RateLimit, boolToInt(r.Enable), db.TimeToStr(r.CreatedAt), db.TimeToStr(r.UpdatedAt)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func migrateRoutesFromJSON() (bool, error) {
	path := filepath.Join(dataDir, "routes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var rs []Route
	if len(data) > 0 {
		if err := json.Unmarshal(data, &rs); err != nil {
			return false, err
		}
	}
	if err := saveRoutesToDB(rs); err != nil {
		return false, err
	}
	db.RenameJSONToBak(dataDir, "routes.json")
	return true, nil
}

// boolToInt 布尔转数据库存储用的 0/1
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetAll 返回所有路由（含禁用），用于管理后台展示
func GetAll() ([]Route, error) {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Route, len(routes))
	copy(out, routes)
	return out, nil
}

// GetAllEnabled 返回所有启用的路由，用于转发匹配
func GetAllEnabled() ([]Route, error) {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		if r.Enable {
			out = append(out, r)
		}
	}
	return out, nil
}

// Insert 新增路由
func Insert(r *Route) (int64, error) {
	mu.Lock()
	defer mu.Unlock()
	r.ID = nextID
	nextID++
	now := time.Now()
	r.CreatedAt = now
	r.UpdatedAt = now
	routes = append(routes, *r)
	if err := save(); err != nil {
		return 0, err
	}
	return int64(r.ID), nil
}

// Update 更新路由
func Update(id int, r *Route) error {
	mu.Lock()
	defer mu.Unlock()
	for i := range routes {
		if routes[i].ID == id {
			r.ID = id
			r.CreatedAt = routes[i].CreatedAt
			r.UpdatedAt = time.Now()
			routes[i] = *r
			return save()
		}
	}
	return ErrNotFound
}

// Delete 删除路由
func Delete(id int) error {
	mu.Lock()
	defer mu.Unlock()
	for i := range routes {
		if routes[i].ID == id {
			routes = append(routes[:i], routes[i+1:]...)
			return save()
		}
	}
	return ErrNotFound
}
