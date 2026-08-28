package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

type invoiceMeta struct {
	Month     string `json:"month"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
	Requests  int64  `json:"requests"`
	Cost      int64  `json:"cost"`
}

func init() {
	http.HandleFunc("/api/billing/invoices", InvoicesHandler)
	http.HandleFunc("/api/billing/invoices/download", InvoicesHandler)
}

func listInvoices() []invoiceMeta {
	raw := model.KVGetAll("invoice_meta.")
	list := make([]invoiceMeta, 0, len(raw))
	for month, v := range raw {
		var m invoiceMeta
		if json.Unmarshal([]byte(v), &m) != nil {
			continue
		}
		if m.Month == "" {
			m.Month = month
		}
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Month > list[j].Month })
	return list
}

func InvoicesHandler(w http.ResponseWriter, r *http.Request) {
	if setInvoiceCORS(w, r) {
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	if r.URL.Path == "/api/billing/invoices/download" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		month := r.URL.Query().Get("month")
		if !validMonth(month) {
			writeErr(w, http.StatusBadRequest, "invalid month")
			return
		}
		http.Redirect(w, r, "/api/billing/invoice?month="+month, http.StatusFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"invoices": listInvoices()})
	case http.MethodPost:
		var req struct {
			Month string `json:"month"`
			Note  string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validMonth(req.Month) {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		requests, cost := buildMonthlySummary(req.Month)
		m := invoiceMeta{
			Month:     req.Month,
			Note:      req.Note,
			CreatedAt: time.Now().Format(time.RFC3339),
			Requests:  requests,
			Cost:      cost,
		}
		data, _ := json.Marshal(m)
		if err := model.KVSet("invoice_meta."+req.Month, string(data)); err != nil {
			writeErr(w, http.StatusInternalServerError, "store failed")
			return
		}
		writeJSON(w, m)
	case http.MethodDelete:
		month := r.URL.Query().Get("month")
		if !validMonth(month) {
			writeErr(w, http.StatusBadRequest, "invalid month")
			return
		}
		if err := model.KVDel("invoice_meta." + month); err != nil {
			writeErr(w, http.StatusInternalServerError, "delete failed")
			return
		}
		writeJSON(w, map[string]interface{}{"success": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}