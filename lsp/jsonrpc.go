package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// rpcConn is a minimal JSON-RPC 2.0 connection over the Content-Length
// framed stream an LSP server speaks on stdio. It correlates request ids
// to responses, dispatches server-to-client notifications to a handler,
// and answers server-to-client requests with a null result so a server
// that asks for configuration or a progress token does not stall.
type rpcConn struct {
	w      io.Writer
	closer io.Closer

	wmu sync.Mutex // serializes frame writes

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcMessage
	handler func(method string, params json.RawMessage)
	closed  bool
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// newConn starts the read loop over r and returns a connection that
// writes to w and closes via closer. Incoming notifications are delivered
// to the handler set with setHandler.
func newConn(r io.Reader, w io.Writer, closer io.Closer) *rpcConn {
	c := &rpcConn{w: w, closer: closer, pending: map[int]chan rpcMessage{}}
	go c.readLoop(r)
	return c
}

// setHandler installs the notification handler. It is set once, before
// the first request, so the read loop never races a nil handler on the
// server's early notifications.
func (c *rpcConn) setHandler(h func(method string, params json.RawMessage)) {
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
}

// call sends a request and blocks until the response arrives, the context
// is done, or the connection closes.
func (c *rpcConn) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return io.ErrClosedPipe
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	idBytes, _ := json.Marshal(id)
	raw := json.RawMessage(idBytes)
	if err := c.write(rpcMessage{JSONRPC: "2.0", ID: &raw, Method: method, Params: mustRaw(params)}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return io.ErrClosedPipe
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
}

// notify sends a notification, which expects no response.
func (c *rpcConn) notify(method string, params any) error {
	return c.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: mustRaw(params)})
}

// close shuts the underlying stream; pending callers unblock with a
// closed-pipe error.
func (c *rpcConn) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = map[int]chan rpcMessage{}
	c.mu.Unlock()
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

func (c *rpcConn) write(msg rpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

func (c *rpcConn) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		msg, err := readFrame(br)
		if err != nil {
			_ = c.close()
			return
		}
		c.dispatch(msg)
	}
}

func (c *rpcConn) dispatch(msg rpcMessage) {
	// A response carries an id and no method.
	if msg.ID != nil && msg.Method == "" {
		var id int
		if json.Unmarshal(*msg.ID, &id) == nil {
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
		return
	}
	// A request from the server carries an id and a method: answer it so
	// the server does not block waiting on us.
	if msg.ID != nil {
		c.answer(msg)
		return
	}
	// Otherwise it is a notification.
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	if h != nil {
		h(msg.Method, msg.Params)
	}
}

// answer replies to a server-to-client request. workspace/configuration
// wants an array sized to its items; everything else gets a null result,
// which satisfies registerCapability and progress-token creation.
func (c *rpcConn) answer(req rpcMessage) {
	var result any
	if req.Method == "workspace/configuration" {
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(req.Params, &p)
		arr := make([]any, len(p.Items))
		for i := range arr {
			arr[i] = map[string]any{}
		}
		result = arr
	}
	_ = c.write(rpcMessage{JSONRPC: "2.0", ID: req.ID, Result: mustRaw(result)})
}

// readFrame reads one Content-Length framed message.
func readFrame(br *bufio.Reader) (rpcMessage, error) {
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return rpcMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, val, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return rpcMessage{}, fmt.Errorf("bad Content-Length: %q", val)
			}
			length = n
		}
	}
	if length < 0 {
		return rpcMessage{}, fmt.Errorf("frame with no Content-Length")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(br, buf); err != nil {
		return rpcMessage{}, err
	}
	var msg rpcMessage
	if err := json.Unmarshal(buf, &msg); err != nil {
		return rpcMessage{}, err
	}
	return msg, nil
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
