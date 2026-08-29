package model

import (
	"encoding/json"
	"errors"
	"sort"
)

// VModel 虚拟模型：展示名（用户可见、可随意命名、可自定义价格）→ 真实上游模型。
// 大量展示名可以全部指向同一个上游模型，价格独立设置。KV 键：vmodel.<display>
type VModel struct {
	Display         string  `json:"display"`          // 展示名（用户调用 /v1/models 时看到的）
	Upstream        string  `json:"upstream"`         // 真实上游模型名
	Ratio           float64 `json:"ratio"`            // 提示词价格倍率（biz 模式）
	CompletionRatio float64 `json:"completion_ratio"` // 补全词价格倍率（biz 模式）
}

// GetVModels 返回全部虚拟模型（按展示名排序）
func GetVModels() []VModel {
	raw := KVGetAll("vmodel.")
	out := make([]VModel, 0, len(raw))
	for _, v := range raw {
		var vm VModel
		if json.Unmarshal([]byte(v), &vm) == nil && vm.Display != "" && vm.Upstream != "" {
			out = append(out, vm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Display < out[j].Display })
	return out
}

// GetVModel 按展示名查询虚拟模型
func GetVModel(display string) (VModel, bool) {
	v, ok := KVGet("vmodel." + display)
	if !ok || v == "" {
		return VModel{}, false
	}
	var vm VModel
	if json.Unmarshal([]byte(v), &vm) != nil || vm.Upstream == "" {
		return VModel{}, false
	}
	return vm, true
}

// SaveVModel 保存虚拟模型
func SaveVModel(vm VModel) error {
	if vm.Display == "" || vm.Upstream == "" {
		return errors.New("display/upstream 必填")
	}
	b, err := json.Marshal(vm)
	if err != nil {
		return err
	}
	return KVSet("vmodel."+vm.Display, string(b))
}

// DelVModel 删除虚拟模型
func DelVModel(display string) error {
	return KVDel("vmodel." + display)
}

// SetVModelPrompt 保存虚拟模型的系统提示词（KV 键 vprompt.<display>）
func SetVModelPrompt(display, prompt string) error {
	return KVSet("vprompt."+display, prompt)
}

// GetVModelPrompt 读取虚拟模型的系统提示词；未配置返回 ok=false
func GetVModelPrompt(display string) (string, bool) {
	return KVGet("vprompt." + display)
}
