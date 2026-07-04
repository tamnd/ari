package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// fakeServer is an in-memory MCP server over the ReadWriteCloser the client
// speaks to. The client writes a request line, the server computes the
// response synchronously into a buffer, and the client's next read drains
// it, which matches the client's one-round-trip-at-a-time contract.
type fakeServer struct {
	mu       sync.Mutex
	pending  bytes.Buffer
	handler  func(method string, params json.RawMessage) (any, *rpcError)
	notified []string
	closed   bool
}

func (f *fakeServer) Write(p []byte) (int, error) {
	var in struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(p), &in); err != nil {
		return len(p), nil
	}
	if in.ID == nil {
		f.mu.Lock()
		f.notified = append(f.notified, in.Method)
		f.mu.Unlock()
		return len(p), nil
	}
	result, rerr := f.handler(in.Method, in.Params)
	resp := rpcResponse{JSONRPC: "2.0", ID: in.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		raw, _ := json.Marshal(result)
		resp.Result = raw
	}
	line, _ := json.Marshal(resp)
	f.mu.Lock()
	f.pending.Write(line)
	f.pending.WriteByte('\n')
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeServer) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending.Read(p)
}

func (f *fakeServer) Close() error {
	f.closed = true
	return nil
}

func newTestClient(handler func(method string, params json.RawMessage) (any, *rpcError)) (*Client, *fakeServer) {
	fs := &fakeServer{handler: handler}
	return newClient(fs), fs
}

func TestClientHandshakeListAndCall(t *testing.T) {
	ctx := context.Background()
	c, fs := newTestClient(func(method string, params json.RawMessage) (any, *rpcError) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": protocolVersion, "serverInfo": map[string]any{"name": "fake"}}, nil
		case "tools/list":
			return map[string]any{"tools": []map[string]any{
				{"name": "query", "description": "run a read query", "inputSchema": map[string]any{"type": "object"}},
			}}, nil
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(params, &p)
			return map[string]any{"content": []map[string]any{{"type": "text", "text": "ran " + p.Name}}}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(fs.notified) != 1 || fs.notified[0] != "notifications/initialized" {
		t.Fatalf("initialized notification not sent: %v", fs.notified)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "query" {
		t.Fatalf("tools = %+v", tools)
	}

	res, err := c.CallTool(ctx, "query", json.RawMessage(`{"sql":"select 1"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Text != "ran query" || res.IsError {
		t.Fatalf("call result = %+v", res)
	}
}

func TestClientToolErrorIsNotTransportError(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "tools/call" {
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": "table not found"}},
				"isError": true,
			}, nil
		}
		return map[string]any{}, nil
	})
	res, err := c.CallTool(ctx, "query", nil)
	if err != nil {
		t.Fatalf("a tool-level error must not be a transport error: %v", err)
	}
	if !res.IsError || res.Text != "table not found" {
		t.Fatalf("result = %+v, want the error text carried", res)
	}
}

func TestClientRPCErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(func(string, json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: -32000, Message: "boom"}
	})
	if _, err := c.ListTools(ctx); err == nil {
		t.Fatal("a JSON-RPC error must surface")
	}
}
