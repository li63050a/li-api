package handler

import (
	"api-gateway/model"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/ops/exec", ExecHandler)
	http.HandleFunc("/api/ops/fs", FsHandler)
	http.HandleFunc("/api/ops/fs/read", FsReadHandler)
	http.HandleFunc("/api/ops/fs/write", FsWriteHandler)
	http.HandleFunc("/api/ops/fs/delete", FsDeleteHandler)
	http.HandleFunc("/api/ops/fs/upload", UploadHandler)
	http.HandleFunc("/api/setting/exec", ExecSettingsHandler)
}

func serverCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func serverRequireRoot(w http.ResponseWriter, r *http.Request) *model.Session {
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil
	}
	return s
}

// fsBase 返回文件管理的安全根目录（DATA_DIR 或 ./data）
func fsBase() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return filepath.Clean(d)
	}
	return "./data"
}

// inBase 判断绝对路径是否位于安全根目录内
func inBase(p string) bool {
	rel, err := filepath.Rel(fsBase(), p)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// resolveFSPath 把用户传入路径（相对 base 或绝对）规整为 base 内的绝对路径
func resolveFSPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	var full string
	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Clean(filepath.Join(fsBase(), p))
	}
	if !inBase(full) {
		return "", false
	}
	return full, true
}

// captureWriter 收集子进程输出
type captureWriter struct {
	b []byte
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	return len(p), nil
}

// ExecHandler POST /api/ops/exec（root）在网关主机执行命令
// body {"cmd","dir?"}；30s 超时；结果 {"ok","stdout","stderr","code","dir"}。
func ExecHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s := serverRequireRoot(w, r)
	if s == nil {
		return
	}
	v, _ := model.KVGet("ops.exec_enabled")
	if v == "" {
		v = "1"
	}
	if v != "1" {
		writeError(w, http.StatusForbidden, "exec disabled", "invalid_request_error")
		return
	}
	var body struct {
		Cmd string `json:"cmd"`
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
		return
	}
	if strings.TrimSpace(body.Cmd) == "" {
		writeError(w, http.StatusBadRequest, "cmd required", "invalid_request_error")
		return
	}
	_ = model.AppendAudit(s.Username, "server.exec", body.Cmd)
	dir := body.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	shell := "/bin/sh"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/bash"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", body.Cmd)
	cmd.Dir = dir
	var out, errOut captureWriter
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	werr := cmd.Run()
	if ctx.Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "exec timeout", "timeout_error")
		return
	}
	code := 0
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":     true,
		"stdout": string(out.b),
		"stderr": string(errOut.b),
		"code":   code,
		"dir":    dir,
	})
}

// fsEntry 目录条目
type fsEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	IsDir bool   `json:"is_dir"`
}

// FsHandler GET /api/ops/fs?path=（root）列出目录，目录优先再按名字排序。
func FsHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	full, ok := resolveFSPath(r.URL.Query().Get("path"))
	if !ok {
		writeError(w, http.StatusForbidden, "path outside base", "invalid_request_error")
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "invalid_request_error")
		return
	}
	list := make([]fsEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		list = append(list, fsEntry{Name: e.Name(), Size: info.Size(), Mtime: info.ModTime().Unix(), IsDir: info.IsDir()})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsDir != list[j].IsDir {
			return list[i].IsDir
		}
		return list[i].Name < list[j].Name
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"path": full, "entries": list})
}

// FsReadHandler GET /api/ops/fs/read?path=（root）读取文件内容（上限 1MB）。
func FsReadHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	full, ok := resolveFSPath(r.URL.Query().Get("path"))
	if !ok {
		writeError(w, http.StatusForbidden, "path outside base", "invalid_request_error")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "invalid_request_error")
		return
	}
	defer f.Close()
	const maxRead = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if len(data) > maxRead {
		data = data[:maxRead]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"path": full, "content": string(data)})
}

// FsWriteHandler POST /api/ops/fs/write（root）body {"path","content"} 写入文件（0644）。
func FsWriteHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
		return
	}
	full, ok := resolveFSPath(body.Path)
	if !ok {
		writeError(w, http.StatusForbidden, "path outside base", "invalid_request_error")
		return
	}
	if err := os.WriteFile(full, []byte(body.Content), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// FsDeleteHandler POST /api/ops/fs/delete（root）body {"path"} 删除文件或目录。
func FsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
		return
	}
	full, ok := resolveFSPath(body.Path)
	if !ok {
		writeError(w, http.StatusForbidden, "path outside base", "invalid_request_error")
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "invalid_request_error")
		return
	}
	if info.IsDir() {
		err = os.RemoveAll(full)
	} else {
		err = os.Remove(full)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// ExecSettingsHandler GET/POST /api/setting/exec（root）读写命令执行开关。
func ExecSettingsHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	switch r.Method {
	case "GET":
		v, _ := model.KVGet("ops.exec_enabled")
		if v == "" {
			v = "1"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"enabled": v == "1"})
	case "POST":
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
			return
		}
		v := "0"
		if body.Enabled {
			v = "1"
		}
		if err := model.KVSet("ops.exec_enabled", v); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "enabled": body.Enabled})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// UploadHandler POST /api/ops/fs/upload（root）multipart 上传文件到 base+path。
func UploadHandler(w http.ResponseWriter, r *http.Request) {
	serverCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverRequireRoot(w, r) == nil {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	full, ok := resolveFSPath(r.FormValue("path"))
	if !ok {
		writeError(w, http.StatusForbidden, "path outside base", "invalid_request_error")
		return
	}
	src, h, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file required", "invalid_request_error")
		return
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	dst, err := os.Create(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	defer dst.Close()
	n, err := io.Copy(dst, src)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "size": n, "name": h.Filename})
}
