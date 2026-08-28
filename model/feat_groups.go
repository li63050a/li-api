package model

import (
	"encoding/json"
	"sort"
)

// Group 用户分组：一组可用模型 + 配额倍率（仿 new-api，KV 存储）
type Group struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Ratio  float64  `json:"ratio"`
}

// groupKVPrefix 分组在 KV 表中的键前缀
const groupKVPrefix = "group."

// GetGroups 返回全部分组，按名称排序
func GetGroups() []Group {
	raw := KVGetAll(groupKVPrefix)
	out := make([]Group, 0, len(raw))
	for _, v := range raw {
		var g Group
		if err := json.Unmarshal([]byte(v), &g); err != nil {
			continue
		}
		if g.Name == "" {
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetGroup 按名称读取分组
func GetGroup(name string) (Group, bool) {
	v, ok := KVGet(groupKVPrefix + name)
	if !ok {
		return Group{}, false
	}
	var g Group
	if err := json.Unmarshal([]byte(v), &g); err != nil {
		return Group{}, false
	}
	return g, true
}

// SaveGroup 保存分组（upsert）
func SaveGroup(g Group) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return KVSet(groupKVPrefix+g.Name, string(b))
}

// DelGroup 删除分组
func DelGroup(name string) error {
	return KVDel(groupKVPrefix + name)
}

// GroupModels 返回分组允许的模型列表；分组不存在或模型为空时返回 nil（relay 用于限制可用模型）
func GroupModels(name string) []string {
	g, ok := GetGroup(name)
	if !ok || len(g.Models) == 0 {
		return nil
	}
	out := make([]string, len(g.Models))
	copy(out, g.Models)
	return out
}