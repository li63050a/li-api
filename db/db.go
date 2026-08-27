// Package db 提供统一的 SQLite 关系型存储后端（纯 Go，无需 CGO）。
// 各 model 包在 Init 时把数据加载进内存切片，save* 时整表写回 SQLite，
// 从而在不改变任何函数签名与业务逻辑的前提下，把底层存储从 JSON 文件换成关系型数据库。
package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB 全局数据库连接（单写者，连接池大小为 1，避免 SQLite 写锁冲突）
var DB *sql.DB

// TimeToStr 将 time.Time 序列化为可存储字符串（零值存空串）
func TimeToStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// StrToTime 解析存储的时间字符串；空串或解析失败返回零值
func StrToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Open 打开（或创建）位于 dataDir 下的 gateway.db，并设置常用 PRAGMA。
// 重复调用安全（仅首次生效）。
func Open(dataDir string) error {
	if DB != nil {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "gateway.db")
	var err error
	DB, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	DB.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=OFF",
	} {
		if _, err := DB.Exec(p); err != nil {
			return err
		}
	}
	if err := Migrate(); err != nil {
		return err
	}
	return nil
}

// Migrate 创建所有表（幂等）
func Migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			status INTEGER NOT NULL DEFAULT 1,
			quota BIGINT NOT NULL DEFAULT 0,
			used BIGINT NOT NULL DEFAULT 0,
			rate_limit INTEGER NOT NULL DEFAULT 0,
			created_at TEXT,
			updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS tokens (
			key TEXT PRIMARY KEY,
			name TEXT,
			owner TEXT,
			grp TEXT,
			quota BIGINT,
			used BIGINT,
			unlimited INTEGER,
			status INTEGER,
			expired_at TEXT,
			models TEXT,
			created_at TEXT,
			updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT, type TEXT, base_url TEXT, keys TEXT, auth_type TEXT, auth_key TEXT,
			models TEXT, model_mapping TEXT, grp TEXT, priority INTEGER, weight INTEGER,
			rate_limit INTEGER, status INTEGER, created_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK (id=1),
			mode TEXT, open_register INTEGER, model_ratio TEXT, completion_ratio TEXT,
			smtp_host TEXT, smtp_port INTEGER, smtp_user TEXT, smtp_pass TEXT, smtp_from TEXT, notify_email TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS routes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT, prefix TEXT UNIQUE, upstream_url TEXT, auth_type TEXT, auth_key TEXT,
			auth_value TEXT, auth_values TEXT, timeout INTEGER, need_api_key INTEGER,
			allowed_paths TEXT, rate_limit INTEGER, enable INTEGER, created_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS redemptions (
			code TEXT PRIMARY KEY, quota BIGINT, status INTEGER, created_by TEXT,
			created_at TEXT, redeemed_by TEXT, redeemed_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS billing (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time TEXT, usr TEXT, type TEXT, amount BIGINT, balance BIGINT, remark TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS audits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time TEXT, actor TEXT, action TEXT, detail TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS model_prices (
			id INTEGER PRIMARY KEY CHECK (id=1),
			data TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS site (
			id INTEGER PRIMARY KEY CHECK (id=1),
			data TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS access_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time TEXT, method TEXT, path TEXT, grp TEXT, token TEXT,
			model TEXT, status INTEGER, stream INTEGER, cost BIGINT,
			duration INTEGER, detail TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			k TEXT PRIMARY KEY,
			v TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	// 为 users 表补充新增列（幂等迁移）
	existing := map[string]bool{}
	cols, err := DB.Query("PRAGMA table_info(users)")
	if err != nil {
		return err
	}
	for cols.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt interface{}
		if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			cols.Close()
			return err
		}
		existing[name] = true
	}
	cols.Close()
	addCol := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		_, err := DB.Exec("ALTER TABLE users ADD COLUMN " + name + " " + ddl)
		return err
	}
	if err := addCol("email", "TEXT"); err != nil {
		return err
	}
	if err := addCol("twofa_secret", "TEXT"); err != nil {
		return err
	}
	if err := addCol("twofa_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// IsEmpty 判断指定表是否没有任何行
func IsEmpty(table string) (bool, error) {
	var n int
	if err := DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		return true, err
	}
	return n == 0, nil
}

// RenameJSONToBak 把迁移完成的旧 JSON 文件重命名为 .bak，避免重复迁移
func RenameJSONToBak(dataDir, name string) {
	src := filepath.Join(dataDir, name)
	if _, err := os.Stat(src); err == nil {
		_ = os.Rename(src, src+".bak")
	}
}
