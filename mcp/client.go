package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// protocolVersion is the MCP revision this client speaks. The server
// echoes its own; a mismatch is tolerated because tools/list and
// tools/call are stable across the revisions M1 targets.
const protocolVersion = "2024-11-05"

// ToolDesc is one tool a server advertises: the name, a one-line
// description, and the JSON Schema for its arguments. The bridge namespaces
// the name and defers the schema behind the search step.
type ToolDesc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// CallResult is what tools/call returns: the text content the server
// produced and whether it reported an error. The content is untrusted (D20)
// and the caller marks it so before it enters model context.
type CallResult struct {
	Text    string
	IsError bool
}

// Client is a JSON-RPC 2.0 client over one newline-delimited stream to an
// MCP server. It is synchronous: one round trip at a time under a mutex,
// which is enough because a session issues list and call requests in
// sequence, never concurrently.
type Client struct {
	rwc io.ReadWriteCloser
	br  *bufio.Reader

	mu   sync.Mutex
	next int64
}

// newClient wraps an established duplex stream. Tests inject a fake stream;
// Connect builds a real stdio one.
func newClient(rwc io.ReadWriteCloser) *Client {
	return &Client{rwc: rwc, br: bufio.NewReader(rwc), next: 1}
}

// rpcRequest is one JSON-RPC request. A nil ID marks a notification, which
// expects no response.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is one JSON-RPC response. Result and Error are mutually
// exclusive per the spec.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// Initialize performs the handshake: it sends initialize, reads the
// server's capabilities, and sends the initialized notification. A server
// that fails the handshake is treated as unavailable by the caller.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ari", "version": "m1"},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify("notifications/initialized")
}

// ListTools fetches the server's tools fresh. The caller re-lists on
// reconnect so a restarted server does not leave stale tools behind.
func (c *Client) ListTools(ctx context.Context) ([]ToolDesc, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDesc `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding tools/list: %w", err)
	}
	return out.Tools, nil
}

// CallTool invokes one tool by its short (un-namespaced) name and returns
// the flattened text content. A server-reported tool error is not a
// transport error: it comes back with IsError set so the model can read and
// correct it.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = json.RawMessage(args)
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("decoding tools/call: %w", err)
	}
	var text string
	for i, block := range out.Content {
		if block.Type != "text" && block.Text == "" {
			continue
		}
		if i > 0 && text != "" {
			text += "\n"
		}
		text += block.Text
	}
	return CallResult{Text: text, IsError: out.IsError}, nil
}

// Close shuts the stream, which for a stdio transport ends the child
// process, so a server never outlives the session.
func (c *Client) Close() error { return c.rwc.Close() }

// call sends a request and reads responses until the matching id arrives,
// skipping any notifications the server interleaves. It respects context
// cancellation between reads.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.next
	c.next++
	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := c.br.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("reading response to %s: %w", method, err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // a non-JSON line (a server log leak) is not our message
		}
		if resp.ID == nil || *resp.ID != id {
			continue // a notification or a stale id
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify sends a fire-and-forget notification with no id.
func (c *Client) notify(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method})
}

// write frames one message as a single newline-terminated JSON line, the
// stdio transport framing.
func (c *Client) write(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.rwc.Write(data)
	return err
}
