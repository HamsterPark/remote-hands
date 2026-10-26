package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// This file implements the small part of the Model Context Protocol that a
// tools-only server needs, over the "streamable HTTP" transport: every
// JSON-RPC request is POSTed and answered with a plain JSON response. There
// are no sessions and no server-sent events.

const maxRequestBody = 32 << 20

// Protocol versions we can speak; the newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type toolResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

func textResult(s string) toolResult {
	return toolResult{Content: []content{{Type: "text", Text: s}}}
}

func errorResult(format string, args ...any) toolResult {
	r := textResult(fmt.Sprintf(format, args...))
	r.IsError = true
	return r
}

// app holds everything the HTTP handlers need.
type app struct {
	name  string // computer name shown to the AI
	host  string // address in the connection URL
	ui    *ui
	jobs  *jobManager
	peers *peerNames
	tools []*toolDef

	mu         sync.RWMutex
	token      string
	lastBadKey time.Time
}

func newApp(name, host, token string, u *ui) *app {
	a := &app{name: name, host: host, ui: u, token: token, jobs: newJobManager(), peers: newPeerNames()}
	a.tools = a.buildTools()
	return a
}

func (a *app) setToken(t string) {
	a.mu.Lock()
	a.token = t
	a.mu.Unlock()
}

func (a *app) checkKey(key string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(key) == len(a.token) && subtle.ConstantTimeCompare([]byte(key), []byte(a.token)) == 1
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "remote-hands ok %s\n", appVersion())
	})
	// The key can be part of the URL (what the connection lines use, so that
	// no header has to be configured) or a bearer token.
	mux.HandleFunc("/mcp/{key}", func(w http.ResponseWriter, r *http.Request) {
		a.serveMCP(w, r, r.PathValue("key"))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.serveMCP(w, r, strings.TrimSpace(key))
	})
	return mux
}

func (a *app) serveMCP(w http.ResponseWriter, r *http.Request, key string) {
	// Browsers always send Origin. No MCP client we know does, and a web
	// page has no business talking to this server.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "remote-hands: requests from web pages are not allowed", http.StatusForbidden)
		return
	}
	who := a.caller(r)
	if !a.checkKey(key) {
		a.noteBadKey(who)
		// 403 rather than 401: a 401 would make MCP clients start an OAuth
		// login, which cannot work here.
		http.Error(w, "remote-hands: wrong key. The connection lines were changed; copy the new 2 lines from the Remote Hands window on that computer.", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "remote-hands: this is an MCP endpoint (streamable HTTP). Add it to Claude Code or Codex as an HTTP MCP server; it only answers POST requests.", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, "remote-hands: request too large", http.StatusRequestEntityTooLarge)
		return
	}
	out, status := a.handleRPC(r.Context(), body, who)
	if out == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// caller names the computer a request comes from, for the activity log.
func (a *app) caller(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return a.peers.lookup(host)
}

func (a *app) noteBadKey(who string) {
	a.mu.Lock()
	quiet := time.Since(a.lastBadKey) < time.Minute
	a.lastBadKey = time.Now()
	a.mu.Unlock()
	if !quiet {
		a.ui.activity(who, a.ui.t("act.badkey"))
	}
}

// handleRPC processes one JSON-RPC message or a batch. It returns the
// response body (nil for "202 Accepted") and the HTTP status.
func (a *app) handleRPC(ctx context.Context, body []byte, who string) ([]byte, int) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var msgs []json.RawMessage
		if err := json.Unmarshal(body, &msgs); err != nil || len(msgs) == 0 {
			return parseErrorBody(), http.StatusBadRequest
		}
		var replies []*rpcResponse
		for _, m := range msgs {
			if r := a.handleMessage(ctx, m, who); r != nil {
				replies = append(replies, r)
			}
		}
		if len(replies) == 0 {
			return nil, http.StatusAccepted
		}
		b, _ := json.Marshal(replies)
		return b, http.StatusOK
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return parseErrorBody(), http.StatusBadRequest
	}
	reply := a.handleMessage(ctx, body, who)
	if reply == nil {
		return nil, http.StatusAccepted
	}
	b, _ := json.Marshal(reply)
	return b, http.StatusOK
}

func parseErrorBody() []byte {
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
	return b
}

// handleMessage answers one request. Notifications and responses (messages
// without an id or without a method) get no reply.
func (a *app) handleMessage(ctx context.Context, raw json.RawMessage, who string) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32600, Message: "invalid request"}}
	}
	if req.Method == "" || len(req.ID) == 0 {
		return nil
	}
	result, rerr := a.dispatch(ctx, &req, who)
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp
}

func (a *app) dispatch(ctx context.Context, req *rpcRequest, who string) (result any, rerr *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		client := strings.TrimSpace(p.ClientInfo.Name + " " + p.ClientInfo.Version)
		if client == "" {
			client = "MCP client"
		}
		a.ui.activity(who, a.ui.t("act.connected", client))
		return map[string]any{
			"protocolVersion": negotiateVersion(p.ProtocolVersion),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{
				"name":    "remote-hands",
				"title":   "Remote Hands (" + a.name + ")",
				"version": appVersion(),
			},
			"instructions": a.instructions(),
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": a.tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		var tool *toolDef
		for _, t := range a.tools {
			if t.Name == p.Name {
				tool = t
			}
		}
		if tool == nil {
			return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
		}
		if len(bytes.TrimSpace(p.Arguments)) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		defer func() {
			if v := recover(); v != nil {
				result, rerr = errorResult("Internal error in %s: %v\n%s", p.Name, v, debug.Stack()), nil
			}
		}()
		return tool.run(ctx, p.Arguments, who), nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
}

// negotiateVersion answers with the client's protocol version when we know
// it, and with our newest one otherwise (the client then decides).
func negotiateVersion(requested string) string {
	for _, v := range protocolVersions {
		if v == requested {
			return v
		}
	}
	return protocolVersions[0]
}
