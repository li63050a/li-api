package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/mcp", MCPHandler)
}

// mcpReq JSON-RPC 2.0 请求体
type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpResp JSON-RPC 2.0 响应体
type mcpResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpErr         `json:"error,omitempty"`
}

type mcpErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *mcpErr) Error() string { return e.Message }

// mcpTool 工具定义
type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

func prop(t string, desc string, required bool) map[string]interface{} {
	p := map[string]interface{}{"type": t, "description": desc}
	if required {
		p["required"] = true
	}
	return p
}

func schema(props map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": props}
}

var mcpTools = []mcpTool{
	{"list_models", "List all available model names and aliases",
		schema(map[string]interface{}{})},
	{"list_channels", "List all channels (id, name, type, base_url, group, status)",
		schema(map[string]interface{}{})},
	{"list_tokens", "List all tokens (name, owner, group, quota, used, status)",
		schema(map[string]interface{}{})},
	{"create_token", "Create a new gateway token",
		schema(map[string]interface{}{
			"name":   prop("string", "token name", true),
			"owner":  prop("string", "owner username", true),
			"group":  prop("string", "token group", false),
			"quota":  prop("integer", "token quota, -1 unlimited", false),
			"models": prop("string", "allowed models, comma separated", false),
		})},
	{"list_users", "List all users (id, username, role, status, quota, used)",
		schema(map[string]interface{}{})},
	{"get_stats", "Aggregate stats from access.log (total requests, cost, top5 models)",
		schema(map[string]interface{}{})},
}

func mcpReply(w http.ResponseWriter, id json.RawMessage, result interface{}, rpcErr *mcpErr) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mcpResp{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

// mcpAuthorized 校验 Bearer 会话（root）或网关令牌（owner 为 root），或 X-API-Key
func mcpAuthorized(r *http.Request) bool {
	key := ""
	ah := r.Header.Get("Authorization")
	if strings.HasPrefix(ah, "Bearer ") {
		key = strings.TrimSpace(strings.TrimPrefix(ah, "Bearer "))
	} else if xk := r.Header.Get("X-API-Key"); xk != "" {
		key = xk
	}
	if key == "" {
		return false
	}
	if s, ok := model.GetSession(key); ok {
		return model.IsRoot(s.Username)
	}
	if t, err := model.GetToken(key); err == nil {
		return model.IsRoot(t.Owner)
	}
	return false
}

// mcpSSE 以 text/event-stream 提供 MCP 端点发现与心跳
func mcpSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, "event: endpoint\ndata: /mcp\n\n")
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

// mcpHandleJSONRPC 处理单个 JSON-RPC 请求
func mcpHandleJSONRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		mcpReply(w, nil, nil, &mcpErr{-32700, "parse error"})
		return
	}
	var req mcpReq
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		mcpReply(w, nil, nil, &mcpErr{-32700, "parse error"})
		return
	}
	if !mcpAuthorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(mcpResp{JSONRPC: "2.0", ID: req.ID, Error: &mcpErr{-32001, "unauthorized"}})
		return
	}
	switch req.Method {
	case "initialize":
		mcpReply(w, req.ID, map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "api-gateway", "version": "0.0.0"},
		}, nil)
	case "tools/list":
		mcpReply(w, req.ID, map[string]interface{}{"tools": mcpTools}, nil)
	case "tools/call":
		result, callErr := mcpCallTool(req.Params)
		if callErr != nil {
			mcpReply(w, req.ID, map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": callErr.Error()}},
				"isError": true,
			}, nil)
			return
		}
		b, _ := json.Marshal(result)
		mcpReply(w, req.ID, map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": string(b)}},
			"isError": false,
		}, nil)
	default:
		mcpReply(w, req.ID, nil, &mcpErr{-32601, "Method not found"})
	}
}

// mcpCallTool 按名称分发工具调用，各工具体返回可 JSON 序列化的值
func mcpCallTool(params json.RawMessage) (interface{}, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, fmt.Errorf("invalid call params")
		}
	}
	switch call.Name {
	case "list_models":
		return mcpListModels(), nil
	case "list_channels":
		return mcpListChannels(), nil
	case "list_tokens":
		return mcpListTokens(), nil
	case "create_token":
		return mcpCreateToken(call.Arguments)
	case "list_users":
		return mcpListUsers(), nil
	case "get_stats":
		return mcpGetStats(), nil
	}
	return nil, fmt.Errorf("unknown tool: %s", call.Name)
}

func mcpListModels() interface{} {
	set := map[string]bool{}
	chs, _ := model.GetAllChannels()
	for _, c := range chs {
		for _, m := range strings.Split(c.Models, ",") {
			if m = strings.TrimSpace(m); m != "" && m != "*" {
				set[m] = true
			}
		}
	}
	aliases := model.KVGetAll("alias.")
	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	sort.Strings(models)
	return map[string]interface{}{"models": models, "aliases": aliases}
}

func mcpListChannels() interface{} {
	type ch struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		BaseURL string `json:"base_url"`
		Group   string `json:"group"`
		Status  int    `json:"status"`
	}
	chs, _ := model.GetAllChannelsRaw()
	out := make([]ch, 0, len(chs))
	for _, c := range chs {
		out = append(out, ch{c.ID, c.Name, c.Type, c.BaseURL, c.Group, c.Status})
	}
	return out
}

func mcpListTokens() interface{} {
	type tk struct {
		Name   string `json:"name"`
		Owner  string `json:"owner"`
		Group  string `json:"group"`
		Quota  int64  `json:"quota"`
		Used   int64  `json:"used"`
		Status int    `json:"status"`
	}
	toks, _ := model.GetAllTokens()
	out := make([]tk, 0, len(toks))
	for _, t := range toks {
		out = append(out, tk{t.Name, t.Owner, t.Group, t.Quota, t.Used, t.Status})
	}
	return out
}

func mcpCreateToken(args json.RawMessage) (interface{}, error) {
	var p struct {
		Name   string `json:"name"`
		Owner  string `json:"owner"`
		Group  string `json:"group"`
		Quota  int64  `json:"quota"`
		Models string `json:"models"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid arguments")
		}
	}
	if p.Name == "" || p.Owner == "" {
		return nil, fmt.Errorf("name and owner are required")
	}
	if p.Group == "" {
		p.Group = "default"
	}
	if p.Quota == 0 {
		p.Quota = -1
	}
	t := &model.Token{Name: p.Name, Owner: p.Owner, Group: p.Group, Quota: p.Quota, Models: p.Models, Status: 1}
	if _, err := model.InsertToken(t); err != nil {
		return nil, err
	}
	return map[string]interface{}{"key": t.Key}, nil
}

func mcpListUsers() interface{} {
	type us struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
		Status   int    `json:"status"`
		Quota    int64  `json:"quota"`
		Used     int64  `json:"used"`
	}
	users := model.GetAllUsers()
	out := make([]us, 0, len(users))
	for _, u := range users {
		out = append(out, us{u.ID, u.Username, u.Role, u.Status, u.Quota, u.Used})
	}
	return out
}

// mcpGetStats 聚合 access.log：总请求数、总花费、Top5 模型
func mcpGetStats() interface{} {
	f, err := os.Open(filepath.Join(model.DataDir(), "access.log"))
	if err != nil {
		return map[string]interface{}{"total_requests": 0, "total_cost": int64(0), "top5_models": []string{}}
	}
	defer f.Close()
	var totalReq int64
	var totalCost float64
	byModel := map[string]int64{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e map[string]interface{}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		totalReq++
		switch c := e["cost"].(type) {
		case float64:
			totalCost += c
		case int:
			totalCost += float64(c)
		}
		if m, _ := e["model"].(string); m != "" {
			byModel[m]++
		}
	}
	type mc struct {
		Model    string `json:"model"`
		Requests int64  `json:"requests"`
	}
	top := make([]mc, 0, len(byModel))
	for m, n := range byModel {
		top = append(top, mc{m, n})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Requests == top[j].Requests {
			return top[i].Model < top[j].Model
		}
		return top[i].Requests > top[j].Requests
	})
	if len(top) > 5 {
		top = top[:5]
	}
	return map[string]interface{}{
		"total_requests": totalReq,
		"total_cost":     totalCost,
		"top5_models":    top,
	}
}

// MCPHandler 处理 /mcp 的 JSON-RPC 与 SSE 请求
func MCPHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == "GET" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		mcpSSE(w, r)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mcpHandleJSONRPC(w, r)
}