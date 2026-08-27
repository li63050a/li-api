package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
)

// BillingHandler GET /api/feat/billing
// 返回账单/额度变动记录。root 看全部，普通用户只看自己；并并入已使用充值码记录。
func init() {
	http.HandleFunc("/api/feat/billing", BillingHandler)
	http.HandleFunc("/api/feat/billing/adjust", BillingAdjustHandler)
}

func setBillingCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// BillingHandler 获取账单记录
func BillingHandler(w http.ResponseWriter, r *http.Request) {
	if setBillingCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	entries := model.LoadBillings()

	// 并入已使用(Status==0)的充值码记录
	redemptions := model.GetAllRedemptions()
	for _, rd := range redemptions {
		if rd.Status != 0 {
			continue
		}
		entries = append(entries, model.BillingEntry{
			Time:    rd.RedeemedAt.Format("2006-01-02 15:04:05"),
			User:    rd.RedeemedBy,
			Type:    "redeem",
			Amount:  rd.Quota,
			Balance: 0,
			Remark:  "充值码" + rd.Code,
		})
	}

	// 普通用户只看自己的记录
	if !model.IsRoot(session.Username) {
		filtered := entries[:0]
		for _, e := range entries {
			if e.User == session.Username {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// 整体按时间倒序
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Time > entries[j].Time
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"billings": entries,
	})
}

type billingAdjustRequest struct {
	User   string `json:"user"`
	Amount int64  `json:"amount"`
	Remark string `json:"remark"`
}

// BillingAdjustHandler POST /api/feat/billing/adjust 仅 root 调整额度
func BillingAdjustHandler(w http.ResponseWriter, r *http.Request) {
	if setBillingCORS(w, r) {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !model.IsRoot(session.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req billingAdjustRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if req.User == "" {
		http.Error(w, "user required", http.StatusBadRequest)
		return
	}

	model.AddUserQuota(req.User, req.Amount)

	var balance int64
	if u, found := model.GetUserByUsername(req.User); found {
		balance = u.Quota
	}

	model.AppendBilling(model.BillingEntry{
		User:    req.User,
		Type:    "adjust",
		Amount:  req.Amount,
		Balance: balance,
		Remark:  req.Remark,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"balance": balance,
	})
}
