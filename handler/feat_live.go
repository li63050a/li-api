package handler

import (
	"api-gateway/model"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/logs/stream", LogStreamHandler)
}

// setLiveCORS 设置 SSE 跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func setLiveCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// LogStreamHandler GET /api/logs/stream 以 SSE 实时推送 access.log 的新增行（仅 root）
func LogStreamHandler(w http.ResponseWriter, r *http.Request) {
	if setLiveCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	path := filepath.Join(model.DataDir(), "access.log")
	var offset int64
	var leftover string
	lastHeartbeat := time.Now()

	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}

		if time.Since(lastHeartbeat) >= 15*time.Second {
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
			lastHeartbeat = time.Now()
		}

		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				offset = 0
				leftover = ""
			}
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		if st.Size() < offset {
			offset = 0
			leftover = ""
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			offset = 0
			leftover = ""
			continue
		}

		buf := make([]byte, 64*1024)
		wrote := false
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				offset += int64(n)
				data := leftover + string(buf[:n])
				lines := strings.Split(data, "\n")
				leftover = lines[len(lines)-1]
				for _, line := range lines[:len(lines)-1] {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", line)
					wrote = true
				}
			}
			if rerr != nil {
				break
			}
			if n < len(buf) {
				break
			}
		}
		f.Close()
		if wrote {
			fl.Flush()
		}
	}
}