package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"time"
)

// init 注册套餐 / 订阅 / 额度转移 / 退款 路由
func init() {
	http.HandleFunc("/api/plans", PlanHandler)
	http.HandleFunc("/api/plan/subscribe", SubscribeHandler)
	http.HandleFunc("/api/user/transfer", TransferHandler)
	http.HandleFunc("/admin/refund", RefundHandler)
}

// setPlanCORS 设置跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func setPlanCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// isRootSession 校验管理会话且为 root，失败返回 false
func isRootSession(r *http.Request) bool {
	s, ok := requireSession(r)
	if !ok {
		return false
	}
	return model.IsRoot(s.Username)
}

// PlanHandler GET /api/plans 套餐列表（公开）；POST /api/plans 新增套餐（root）；DELETE /api/plans?name= 删除套餐（root）
func PlanHandler(w http.ResponseWriter, r *http.Request) {
	if setPlanCORS(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"success": true, "plans": model.GetPlans()})
	case http.MethodPost:
		if !isRootSession(r) {
			writeErr(w, http.StatusForbidden, "Forbidden")
			return
		}
		var p model.Plan
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "Bad request")
			return
		}
		if err := model.SavePlan(p); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"success": true, "plan": p})
	case http.MethodDelete:
		if !isRootSession(r) {
			writeErr(w, http.StatusForbidden, "Forbidden")
			return
		}
		name := r.URL.Query().Get("name")
		if err := model.DelPlan(name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type subscribeRequest struct {
	Name string `json:"name"`
}

// SubscribeHandler POST /api/plan/subscribe 订阅套餐（需登录，不必是 root）
// 订阅时立即发放一次套餐额度，并保存订阅记录供每日定时续发。
func SubscribeHandler(w http.ResponseWriter, r *http.Request) {
	if setPlanCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "Forbidden")
		return
	}
	var req subscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "plan name required")
		return
	}
	var plan model.Plan
	found := false
	for _, p := range model.GetPlans() {
		if p.Name == req.Name {
			plan = p
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "plan not found")
		return
	}

	now := time.Now()
	model.AddUserQuota(s.Username, plan.Quota)
	_ = model.AppendBilling(model.BillingEntry{
		User:    s.Username,
		Type:    "subscribe",
		Amount:  plan.Quota,
		Balance: 0,
		Remark:  plan.Name,
	})
	_ = model.SaveSub(s.Username, model.Sub{
		Plan:      plan.Name,
		Expire:    now.AddDate(0, 0, plan.DurationDays).Format(time.RFC3339),
		GrantedAt: now.Format(time.RFC3339),
	})

	writeJSON(w, map[string]interface{}{"success": true, "plan": plan.Name, "quota": plan.Quota})
}

type transferRequest struct {
	To     string `json:"to"`
	Amount int64  `json:"amount"`
}

// TransferHandler POST /api/user/transfer 用户额度转移 {to, amount}
// 发送方扣减已用额度（used += amount），接收方增加额度（quota += amount）。
func TransferHandler(w http.ResponseWriter, r *http.Request) {
	if setPlanCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "Forbidden")
		return
	}
	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	if req.To == "" {
		writeErr(w, http.StatusBadRequest, "to required")
		return
	}
	if req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if model.IsRoot(s.Username) {
		writeErr(w, http.StatusForbidden, "root cannot transfer quota")
		return
	}
	if req.To == s.Username {
		writeErr(w, http.StatusBadRequest, "cannot transfer to self")
		return
	}
	sender, ok := model.GetUserByUsername(s.Username)
	if !ok {
		writeErr(w, http.StatusNotFound, "sender not found")
		return
	}
	recipient, ok := model.GetUserByUsername(req.To)
	if !ok {
		writeErr(w, http.StatusNotFound, "recipient not found")
		return
	}
	remaining := sender.Quota - sender.Used
	if remaining < req.Amount {
		writeErr(w, http.StatusForbidden, "insufficient quota")
		return
	}

	model.AddUserUsed(s.Username, req.Amount)
	model.AddUserQuota(req.To, req.Amount)

	_ = model.AppendBilling(model.BillingEntry{
		User:    s.Username,
		Type:    "transfer",
		Amount:  -req.Amount,
		Balance: remaining - req.Amount,
		Remark:  "to " + req.To,
	})
	_ = model.AppendBilling(model.BillingEntry{
		User:    req.To,
		Type:    "transfer",
		Amount:  req.Amount,
		Balance: recipient.Quota + req.Amount,
		Remark:  "from " + s.Username,
	})

	writeJSON(w, map[string]interface{}{"success": true})
}

type refundRequest struct {
	Username string `json:"username"`
	Amount   int64  `json:"amount"`
}

// RefundHandler POST /admin/refund 管理员退款 {username, amount}（root）
func RefundHandler(w http.ResponseWriter, r *http.Request) {
	if setPlanCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isRootSession(r) {
		writeErr(w, http.StatusForbidden, "Forbidden")
		return
	}
	var req refundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeErr(w, http.StatusBadRequest, "username required")
		return
	}
	if req.Amount <= 0 {
		writeErr(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if _, ok := model.GetUserByUsername(req.Username); !ok {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}

	model.AddUserQuota(req.Username, req.Amount)
	_ = model.AppendBilling(model.BillingEntry{
		User:    req.Username,
		Type:    "refund",
		Amount:  req.Amount,
		Balance: 0,
		Remark:  "admin refund",
	})

	writeJSON(w, map[string]interface{}{"success": true})
}
