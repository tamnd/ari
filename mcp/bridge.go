package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/tamnd/ari/tool"
)

// startupTimeout bounds one server's handshake and first tools/list, so a
// broken or slow server degrades to a warning instead of hanging session
// start.
const startupTimeout = 15 * time.Second

// Bridge owns the live MCP clients for one session and the tool adapters
// built over them. It is the single object the ant holds: it hands the
// deferred tools to the registry and it closes every client on session end,
// so no server outlives the session.
type Bridge struct {
	clients []*Client
	tools   []tool.Tool
	// Warnings records a server that could not be reached, so doctor and
	// the user learn a configured server is down without the session
	// failing over it.
	Warnings []string
}

// Setup connects to every configured server, inspects its tools, and builds
// a deferred adapter for each one the denylist allows. A server that fails
// to connect is a warning, not a fatal error: the session runs with the
// servers that did come up. The returned bridge must be closed on session
// end.
func Setup(ctx context.Context, cfg Config) *Bridge {
	b := &Bridge{}
	for _, name := range sortedKeys(cfg.Servers) {
		spec := cfg.Servers[name]
		sctx, cancel := context.WithTimeout(ctx, startupTimeout)
		client, err := Connect(sctx, spec)
		if err != nil {
			cancel()
			b.Warnings = append(b.Warnings, fmt.Sprintf("mcp server %q: %v", name, err))
			continue
		}
		descs, err := client.ListTools(sctx)
		cancel()
		if err != nil {
			b.Warnings = append(b.Warnings, fmt.Sprintf("mcp server %q: listing tools: %v", name, err))
			_ = client.Close()
			continue
		}
		b.clients = append(b.clients, client)
		b.addTools(cfg, name, client, descs)
	}
	return b
}

// addTools builds one adapter per advertised tool, skipping any the
// denylist forbids so the model never sees a denied tool at all.
func (b *Bridge) addTools(cfg Config, server string, client *Client, descs []ToolDesc) {
	for _, d := range descs {
		t := &mcpTool{
			server: server,
			short:  d.Name,
			desc:   d.Description,
			schema: d.InputSchema,
			client: client,
		}
		if cfg.Denied(t.Name()) {
			continue
		}
		b.tools = append(b.tools, t)
	}
}

// Tools returns the adapters to register, sorted by name for a stable tool
// list and deterministic golden output.
func (b *Bridge) Tools() []tool.Tool {
	out := make([]tool.Tool, len(b.tools))
	copy(out, b.tools)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Close shuts every client, ending the child processes.
func (b *Bridge) Close() error {
	for _, c := range b.clients {
		_ = c.Close()
	}
	b.clients = nil
	return nil
}

func sortedKeys(m map[string]ServerSpec) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
