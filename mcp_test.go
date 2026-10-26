package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestApp(t *testing.T) *app {
	t.Helper()
	a := newApp("test-pc", "127.0.0.1", testToken, newUI(io.Discard, newTranslator("en"), false))
	a.peers.fetch = nil
	t.Cleanup(a.jobs.killAll)
	return a
}

func post(t *testing.T, url, body string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad JSON %q: %v", b, err)
	}
	return m
}

func TestInitializeAndListTools(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	url := srv.URL + "/mcp/" + testToken

	resp, b := post(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1"}}}`, nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("initialize: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	res := decode(t, b)["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	if res["serverInfo"].(map[string]any)["name"] != "remote-hands" {
		t.Errorf("serverInfo = %v", res["serverInfo"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("no tools capability: %v", res["capabilities"])
	}
	if !strings.Contains(res["instructions"].(string), `"test-pc"`) {
		t.Errorf("instructions do not name the computer: %v", res["instructions"])
	}

	resp, b = post(t, url, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if resp.StatusCode != http.StatusAccepted || len(b) != 0 {
		t.Errorf("notification: %d %q", resp.StatusCode, b)
	}

	_, b = post(t, url, `{"jsonrpc":"2.0","id":"two","method":"tools/list"}`, nil)
	m := decode(t, b)
	if m["id"] != "two" {
		t.Errorf("id not echoed: %v", m["id"])
	}
	var names []string
	for _, tl := range m["result"].(map[string]any)["tools"].([]any) {
		tool := tl.(map[string]any)
		names = append(names, tool["name"].(string))
		if tool["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("%s: inputSchema is not an object", tool["name"])
		}
		if tool["description"] == "" {
			t.Errorf("%s has no description", tool["name"])
		}
	}
	want := "system_info run_command check_command read_file write_file edit_file list_directory take_screenshot"
	if strings.Join(names, " ") != want {
		t.Errorf("tools = %v", names)
	}
}

func TestNegotiateVersion(t *testing.T) {
	if v := negotiateVersion("2024-11-05"); v != "2024-11-05" {
		t.Errorf("known version not echoed: %s", v)
	}
	if v := negotiateVersion("2099-01-01"); v != protocolVersions[0] {
		t.Errorf("unknown version: got %s", v)
	}
}

func TestToolCallOverHTTP(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	_, b := post(t, srv.URL+"/mcp/"+testToken, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"system_info","arguments":{}}}`, nil)
	res := decode(t, b)["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("system_info failed: %v", res)
	}
	txt := res["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"Computer name: test-pc", "Home folder:", "Shell used by run_command:"} {
		if !strings.Contains(txt, want) {
			t.Errorf("system_info lacks %q:\n%s", want, txt)
		}
	}
}

func TestBearerHeaderAndBadKeys(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	if resp, b := post(t, srv.URL+"/mcp", ping, map[string]string{"Authorization": "Bearer " + testToken}); resp.StatusCode != 200 {
		t.Errorf("bearer token refused: %d %s", resp.StatusCode, b)
	}
	// Bad keys get 404: MCP clients answer 401 and 403 with an OAuth login.
	cases := map[string]struct {
		url    string
		header map[string]string
		want   int
	}{
		"wrong key in URL":   {srv.URL + "/mcp/ffffffffffffffffffffffffffffffff", nil, http.StatusNotFound},
		"short key in URL":   {srv.URL + "/mcp/0123", nil, http.StatusNotFound},
		"no key":             {srv.URL + "/mcp", nil, http.StatusNotFound},
		"wrong bearer":       {srv.URL + "/mcp", map[string]string{"Authorization": "Bearer nope"}, http.StatusNotFound},
		"request from a web": {srv.URL + "/mcp/" + testToken, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
	}
	for name, c := range cases {
		if resp, _ := post(t, c.url, ping, c.header); resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, c.want)
		}
	}
}

func TestBearerHeaderForms(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for header, want := range map[string]int{
		"bearer " + testToken: http.StatusOK,       // the scheme is case-insensitive
		testToken:             http.StatusNotFound, // the scheme is required
		"Basic " + testToken:  http.StatusNotFound,
	} {
		if resp, _ := post(t, srv.URL+"/mcp", ping, map[string]string{"Authorization": header}); resp.StatusCode != want {
			t.Errorf("Authorization %q: %d, want %d", header[:6], resp.StatusCode, want)
		}
	}
}

func TestFromTailnet(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.64.0.1:5000":       true,
		"100.127.255.9:1":       true,
		"127.0.0.1:1":           true,
		"[::1]:1":               true,
		"192.168.1.5:5000":      false,
		"100.128.0.1:5000":      false,
		"[fe80::1]:1":           false,
		"not an address":        false,
		"[::ffff:100.64.0.1]:1": true,
	} {
		if got := fromTailnet(addr); got != want {
			t.Errorf("fromTailnet(%q) = %v", addr, got)
		}
	}
}

func TestActivityLogIsPrintable(t *testing.T) {
	if got := printable("a\x1b[2Jb\nc\u202ed\x7f"); got != "a [2Jb c d " {
		t.Errorf("printable = %q", got)
	}
	if got := printable("Příliš 你好"); got != "Příliš 你好" {
		t.Errorf("printable changed normal text: %q", got)
	}
}

func TestKeyRotation(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewServer(a.routes())
	defer srv.Close()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	a.setToken("ffffffffffffffffffffffffffffffff")
	if resp, _ := post(t, srv.URL+"/mcp/"+testToken, ping, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("old key still accepted: %d", resp.StatusCode)
	}
	if resp, _ := post(t, srv.URL+"/mcp/ffffffffffffffffffffffffffffffff", ping, nil); resp.StatusCode != 200 {
		t.Errorf("new key refused: %d", resp.StatusCode)
	}
}

func TestProtocolErrors(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	url := srv.URL + "/mcp/" + testToken

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", resp.StatusCode)
	}

	if resp, _ := post(t, url, `{not json`, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON: %d, want 400", resp.StatusCode)
	}

	_, b := post(t, url, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, nil)
	if e := decode(t, b)["error"].(map[string]any); e["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", e)
	}

	// Newer Claude Code versions first probe with server/discover and fall
	// back to initialize when they get a quick JSON-RPC error.
	resp, b = post(t, url, `{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{}}`,
		map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover"})
	m := decode(t, b)
	if resp.StatusCode != 200 || m["id"] != "server-discover-probe-1" || m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("discovery probe: %d %s", resp.StatusCode, b)
	}

	_, b = post(t, url, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"format_disk"}}`, nil)
	if e := decode(t, b)["error"].(map[string]any); e["code"].(float64) != -32602 {
		t.Errorf("unknown tool: %v", e)
	}

	_, b = post(t, url, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`, nil)
	var batch []map[string]any
	if err := json.Unmarshal(b, &batch); err != nil || len(batch) != 2 {
		t.Errorf("batch: %s", b)
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(newTestApp(t).routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(b), "remote-hands ok") {
		t.Errorf("health = %q", b)
	}
}

func TestToolPanicBecomesError(t *testing.T) {
	a := newTestApp(t)
	a.tools = append(a.tools, &toolDef{Name: "boom", run: func(context.Context, json.RawMessage, string) toolResult { panic("kaboom") }})
	res, rerr := a.dispatch(context.Background(), &rpcRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"boom"}`)}, "test")
	if rerr != nil {
		t.Fatalf("rpc error: %v", rerr)
	}
	if r := res.(toolResult); !r.IsError || !strings.Contains(r.Content[0].Text, "kaboom") {
		t.Errorf("panic not reported: %+v", r)
	}
}
