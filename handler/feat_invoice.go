package handler

import (
	"api-gateway/model"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/billing/invoice", InvoiceHandler)
	http.HandleFunc("/admin/users/batch_recharge", BulkRechargeHandler)
	http.HandleFunc("/api/billing/monthly_email", MonthlyEmailHandler)
}

func setInvoiceCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func validMonth(s string) bool {
	if len(s) != 7 || s[4] != '-' {
		return false
	}
	_, err := time.Parse("2006-01", s)
	return err == nil
}

func buildMonthlySummary(month string) (requests, cost int64) {
	path := filepath.Join(model.DataDir(), "access.log")
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	var costF float64
	var lines int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() && lines < 100000 {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines++
		var e struct {
			Time string  `json:"time"`
			Cost float64 `json:"cost"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if !strings.HasPrefix(e.Time, month) {
			continue
		}
		requests++
		costF += e.Cost
	}
	return requests, int64(costF)
}

func pdfEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

func invoicePDFContent(month string, requests, cost int64) string {
	lines := []string{
		"Billing Invoice",
		"Month: " + month,
		"Total requests: " + strconv.FormatInt(requests, 10),
		"Total cost: " + strconv.FormatInt(cost, 10),
		"Generated: " + time.Now().Format("2006-01-02"),
	}
	var b strings.Builder
	b.WriteString("BT\n/F1 20 Tf\n72 720 Td\n")
	for _, l := range lines {
		b.WriteString("(" + pdfEscape(l) + ") Tj\n0 -30 Td\n")
	}
	b.WriteString("ET\n")
	return b.String()
}

func buildInvoicePDF(month string, requests, cost int64) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	offsets := make([]int, 5)
	offsets[0] = buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	offsets[1] = buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	offsets[2] = buf.Len()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")

	content := invoicePDFContent(month, requests, cost)
	offsets[3] = buf.Len()
	fmt.Fprintf(&buf, "4 0 obj\n<< /Length %d >>\nstream\n", len(content))
	buf.WriteString(content)
	buf.WriteString("\nendstream\nendobj\n")

	offsets[4] = buf.Len()
	buf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefPos := buf.Len()
	buf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefPos)
	return buf.Bytes()
}

func InvoiceHandler(w http.ResponseWriter, r *http.Request) {
	if setInvoiceCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if !validMonth(month) {
		writeErr(w, http.StatusBadRequest, "invalid month")
		return
	}
	requests, cost := buildMonthlySummary(month)
	pdf := buildInvoicePDF(month, requests, cost)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="invoice-%s.pdf"`, month))
	_, _ = w.Write(pdf)
}

func BulkRechargeHandler(w http.ResponseWriter, r *http.Request) {
	if setInvoiceCORS(w, r) {
		return
	}
	if r.Method != "POST" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Usernames []string `json:"usernames"`
		Amount    int64    `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Usernames) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	count := 0
	for _, u := range req.Usernames {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		model.AddUserQuota(u, req.Amount)
		_ = model.AppendBilling(model.BillingEntry{
			User:   u,
			Type:   "recharge",
			Amount: req.Amount,
			Remark: "batch recharge",
		})
		count++
	}
	writeJSON(w, map[string]interface{}{"success": true, "count": count})
}

func sendMail(to, subject, body string) error {
	s := model.GetSetting()
	if s.SMTPHost == "" {
		return fmt.Errorf("smtp not configured")
	}
	port := s.SMTPPort
	if port == 0 {
		port = 25
	}
	from := s.SMTPFrom
	if from == "" {
		from = s.SMTPUser
	}
	if from == "" {
		from = to
	}
	var auth smtp.Auth
	if s.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.SMTPUser, s.SMTPPass, s.SMTPHost)
	}
	addr := fmt.Sprintf("%s:%d", s.SMTPHost, port)
	subj := mime.QEncoding.Encode("UTF-8", subject)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subj, body)
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}

func sendMonthlySummaryEmail(month string, requests, cost int64) error {
	s := model.GetSetting()
	if s.SMTPHost == "" || s.NotifyEmail == "" {
		return fmt.Errorf("smtp not configured")
	}
	body := fmt.Sprintf("Monthly usage summary for %s\r\n\r\nTotal requests: %d\r\nTotal cost: %d", month, requests, cost)
	return sendMail(s.NotifyEmail, "Monthly usage summary", body)
}

func MonthlyEmailHandler(w http.ResponseWriter, r *http.Request) {
	if setInvoiceCORS(w, r) {
		return
	}
	if r.Method != "POST" && r.Method != "GET" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	now := time.Now()
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastMonth := firstOfMonth.AddDate(0, -1, 0).Format("2006-01")
	requests, cost := buildMonthlySummary(lastMonth)
	if err := sendMonthlySummaryEmail(lastMonth, requests, cost); err != nil {
		writeJSON(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"success": true, "sent": true})
}
