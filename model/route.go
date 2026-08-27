package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrNotFound 表示路由不存在
var ErrNotFound = errors.New("route not found")

// Route 描述一条转发路由
type Route struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	Prefix       string    `json:"prefix"`
	UpstreamURL  string    `json:"upstream_url"`
	AuthType     string    `json:"auth_type"`    // none, bearer, header, query
	AuthKey      string    `json:"auth_key"`
	AuthValue    string    `json:"auth_value"`
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

// Init 加载数据目录与路由文件，不存在则初始化为空
func Init() error {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "routes.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			routes = []Route{}
			nextID = 1
			return nil
		}
		return err
	}
	if len(data) == 0 {
		routes = []Route{}
		nextID = 1
		return nil
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return err
	}
	nextID = 1
	for _, r := range routes {
		if r.ID >= nextID {
			nextID = r.ID + 1
		}
	}
	return nil
}

func save() error {
	path := filepath.Join(dataDir, "routes.json")
	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
