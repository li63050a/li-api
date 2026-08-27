package model

import (
	"api-gateway/db"
)

// KVSet 写入一条 KV 配置（upsert）
func KVSet(k, v string) error {
	_, err := db.DB.Exec("INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, v)
	return err
}

// KVGet 读取 KV 配置；不存在返回 ok=false
func KVGet(k string) (string, bool) {
	var v string
	err := db.DB.QueryRow("SELECT v FROM kv WHERE k=?", k).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// KVGetAll 读取指定前缀的全部 KV（返回 map[string]string，去掉前缀）
func KVGetAll(prefix string) map[string]string {
	rows, err := db.DB.Query("SELECT k,v FROM kv WHERE k LIKE ?", prefix+"%")
	if err != nil {
		return map[string]string{}
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		out[k[len(prefix):]] = v
	}
	return out
}

// KVDel 删除 KV 配置
func KVDel(k string) error {
	_, err := db.DB.Exec("DELETE FROM kv WHERE k=?", k)
	return err
}