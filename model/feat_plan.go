package model

import (
	"encoding/json"
	"errors"
)

// Plan 订阅套餐（管理员定义，按天周期发放额度）
type Plan struct {
	Name         string `json:"name"`
	Quota        int64  `json:"quota"`
	DurationDays int    `json:"duration_days"`
}

// Sub 用户订阅记录
type Sub struct {
	Plan      string `json:"plan"`
	Expire    string `json:"expire"`     // RFC3339 到期时间
	GrantedAt string `json:"granted_at"` // RFC3339 订阅开始时间
}

// GetPlans 返回全部订阅套餐
func GetPlans() []Plan {
	all := KVGetAll("plan.")
	plans := make([]Plan, 0, len(all))
	for _, v := range all {
		var p Plan
		if json.Unmarshal([]byte(v), &p) != nil || p.Name == "" {
			continue
		}
		plans = append(plans, p)
	}
	return plans
}

// SavePlan 保存订阅套餐（新增或更新）
func SavePlan(p Plan) error {
	if p.Name == "" {
		return errors.New("plan name required")
	}
	if p.Quota <= 0 {
		return errors.New("plan quota must be positive")
	}
	if p.DurationDays <= 0 {
		return errors.New("plan duration_days must be positive")
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return KVSet("plan."+p.Name, string(b))
}

// DelPlan 删除订阅套餐
func DelPlan(name string) error {
	if name == "" {
		return errors.New("plan name required")
	}
	return KVDel("plan." + name)
}

// GetSub 读取用户订阅
func GetSub(username string) (Sub, bool) {
	v, ok := KVGet("sub." + username)
	if !ok {
		return Sub{}, false
	}
	var s Sub
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return Sub{}, false
	}
	return s, true
}

// SaveSub 保存用户订阅
func SaveSub(username string, s Sub) error {
	if username == "" {
		return errors.New("username required")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return KVSet("sub."+username, string(b))
}

// AllSubs 返回全部用户订阅（key 为用户名，value 为 Sub JSON）
func AllSubs() map[string]string {
	return KVGetAll("sub.")
}

// DelSub 删除用户订阅
func DelSub(username string) error {
	if username == "" {
		return errors.New("username required")
	}
	return KVDel("sub." + username)
}
