package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erishen/llm-router/internal/store"
)

// fakeMCPServer 生成一个模拟 MCP server 的 python 脚本（stdio + newline JSON-RPC）。
func fakeMCPServer(t *testing.T) (scriptPath string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_mcp.py")
	script := `import sys, json
def send(obj):
    print(json.dumps(obj), flush=True)
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        send({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake-mcp","version":"1.0"}}})
    elif msg.get("method") == "tools/list":
        send({"jsonrpc":"2.0","id":msg["id"],"result":{"tools":[
            {"name":"add","description":"two numbers added","inputSchema":{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}},
            {"name":"hello","description":"say hi","inputSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}
        ]}})
    elif msg.get("method") == "tools/call":
        args = msg["params"]["arguments"]
        if msg["params"]["name"] == "add":
            send({"jsonrpc":"2.0","id":msg["id"],"result":{"content":[{"type":"text","text":str(args["a"]+args["b"])}]}})
        elif msg["params"]["name"] == "hello":
            send({"jsonrpc":"2.0","id":msg["id"],"result":{"content":[{"type":"text","text":"hi " + args["name"]}]}})
        else:
            send({"jsonrpc":"2.0","id":msg["id"],"result":{"isError":True,"content":[{"type":"text","text":"unknown tool"}]}})
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMCPConnectAndList(t *testing.T) {
	script := fakeMCPServer(t)
	mgr := newMCPManager()
	s, err := mgr.ensure("fake", store.MCPServer{Command: "python3", Args: []string{script}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.close()

	names := s.toolNames()
	if len(names) != 2 || names[0] != "add" || names[1] != "hello" {
		t.Fatalf("tools = %v", names)
	}
}

func TestMCPCall(t *testing.T) {
	script := fakeMCPServer(t)
	mgr := newMCPManager()
	s, err := mgr.ensure("fake", store.MCPServer{Command: "python3", Args: []string{script}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.call(ctx, "tools/call", map[string]any{
		"name":      "add",
		"arguments": map[string]any{"a": 40, "b": 2},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Text != "42" {
		t.Fatalf("result = %v", out.Content)
	}
}

func TestMCPToolNames(t *testing.T) {
	if got := mcpToolName("fs", "read_file"); got != "mcp_fs_read_file" {
		t.Fatalf("mcpToolName = %q", got)
	}
	server, tool := splitMCPToolName("mcp_fs_read_file")
	if server != "fs" || tool != "read_file" {
		t.Fatalf("split = %q %q", server, tool)
	}
}

func TestCleanSchema(t *testing.T) {
	in := map[string]any{
		"$schema":            "http://json-schema.org/draft-07/schema#",
		"type":               "object",
		"title":              "Add",
		"properties":         map[string]any{"a": map[string]any{"type": "number"}},
		"required":           []string{"a"},
		"additionalProperties": false,
	}
	out := cleanSchema(in)
	if _, ok := out["$schema"]; ok {
		t.Fatal("$schema should be stripped")
	}
	if _, ok := out["title"]; ok {
		t.Fatal("title should be stripped")
	}
	if _, ok := out["additionalProperties"]; ok {
		t.Fatal("additionalProperties should be stripped")
	}
	if out["type"] != "object" {
		t.Fatalf("type = %v", out["type"])
	}
	// nil schema
	empty := cleanSchema(nil)
	if empty["type"] != "object" {
		t.Fatalf("nil schema type = %v", empty["type"])
	}
}

// fakeHTTPMCP 起一个模拟 Streamable HTTP 型 MCP server（httptest）。
// 收到 initialize 时回发 Mcp-Session-Id。
func fakeHTTPMCP(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "sess-abc")
		}
		w.Header().Set("Content-Type", "application/json")
		res := map[string]any{}
		switch msg.Method {
		case "initialize":
			res["result"] = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake-http-mcp", "version": "1.0"},
			}
		case "tools/list":
			res["result"] = map[string]any{"tools": []any{
				map[string]any{"name": "add", "description": "two numbers added",
					"inputSchema": map[string]any{"type": "object",
						"properties": map[string]any{"a": map[string]any{"type": "number"}, "b": map[string]any{"type": "number"}},
						"required":   []string{"a", "b"}}},
			}}
		case "tools/call":
			args, _ := msg.Params["arguments"].(map[string]any)
			a, _ := args["a"].(float64)
			b, _ := args["b"].(float64)
			res["result"] = map[string]any{"content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("%.0f", a+b)},
			}}
		default:
			res["result"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestMCPHTTPTransport 验证 Streamable HTTP 传输：连接、schema 注册、工具调用。
func TestMCPHTTPTransport(t *testing.T) {
	url := fakeHTTPMCP(t)
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Mcps = map[string]store.MCPServer{
			"remote": {Transport: "http", URL: url},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{store: st, mcps: newMCPManager()}

	schemas := p.toolSchemas()
	found := false
	for _, s := range schemas {
		fn, _ := s["function"].(map[string]any)
		if fn["name"] == "mcp_remote_add" {
			found = true
		}
	}
	if !found {
		t.Fatal("mcp_remote_add not in tool schemas (http transport)")
	}
	got := p.execTool("k1", "mcp_remote_add", toolArgs{"a": 40, "b": 2})
	if got != "42" {
		t.Fatalf("http mcp exec = %q", got)
	}
	if sts := p.MCPStatuses(); sts["remote"].Connected != true {
		t.Fatalf("http mcp status not connected: %+v", sts["remote"])
	}
}

func TestMCPExecViaProxy(t *testing.T) {
	script := fakeMCPServer(t)
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Mcps = map[string]store.MCPServer{
			"fake": {Command: "python3", Args: []string{script}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{store: st, mcps: newMCPManager()}

	// schema 里应有 mcp_fake_add
	schemas := p.toolSchemas()
	found := false
	for _, s := range schemas {
		fn, _ := s["function"].(map[string]any)
		if fn["name"] == "mcp_fake_add" {
			found = true
		}
	}
	if !found {
		t.Fatal("mcp_fake_add not in tool schemas")
	}
	// 执行 MCP 工具
	got := p.execTool("k1", "mcp_fake_add", toolArgs{"a": 40, "b": 2})
	if got != "42" {
		t.Fatalf("mcp exec = %q", got)
	}
	// hello 工具
	got = p.execTool("k1", "mcp_fake_hello", toolArgs{"name": "world"})
	if got != "hi world" {
		t.Fatalf("mcp hello = %q", got)
	}
	// 未知 MCP 工具
	got = p.execTool("k1", "mcp_fake_nope", toolArgs{})
	if got == "42" {
		t.Fatal("unknown mcp tool should error")
	}
	// 未配置的 server
	got = p.execTool("k1", "mcp_missing_x", toolArgs{})
	if got == "" {
		t.Fatal("missing server should error")
	}
}
