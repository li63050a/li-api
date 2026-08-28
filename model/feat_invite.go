package model

import (
	"encoding/json"
	"errors"
)

// Invite 邀请码（KV 存储：invite.<code>）
type Invite struct {
	Quota   int64  `json:"quota"`
	Inviter string `json:"inviter"`
	Count   int    `json:"count"`
	Used    int    `json:"used"`
}

// SaveInvite 持久化一个邀请码
func SaveInvite(code string, inv Invite) error {
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return KVSet("invite."+code, string(b))
}

// LoadInvite 读取邀请码；不存在返回 ok=false
func LoadInvite(code string) (*Invite, bool) {
	raw, ok := KVGet("invite." + code)
	if !ok {
		return nil, false
	}
	var inv Invite
	if err := json.Unmarshal([]byte(raw), &inv); err != nil {
		return nil, false
	}
	return &inv, true
}

// GenerateInvites 批量生成邀请码，每个码配额 quota、可被 count 人使用
func GenerateInvites(count int, quota int64, inviter string) ([]string, error) {
	if count <= 0 || count > 100 {
		return nil, errors.New("count 超出范围 (1-100)")
	}
	if quota <= 0 {
		return nil, errors.New("quota 必须为正数")
	}
	codes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		code := randToken(8)
		if err := SaveInvite(code, Invite{Quota: quota, Inviter: inviter, Count: 1, Used: 0}); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// RedeemInvite 使用邀请码：校验存在且未用尽，成功后 used+1 并返回邀请详情
func RedeemInvite(code, username string) (*Invite, error) {
	inv, ok := LoadInvite(code)
	if !ok {
		return nil, errors.New("invite not found")
	}
	if inv.Used >= inv.Count {
		return nil, errors.New("invite already used")
	}
	inv.Used++
	if err := SaveInvite(code, *inv); err != nil {
		return nil, err
	}
	return inv, nil
}
