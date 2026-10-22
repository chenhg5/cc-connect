package copilot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCNotification is a JSON-RPC 2.0 notification (server push, no id).
type jsonRPCNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError is the error object in a JSON-RPC 2.0 response.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// lspWriter writes Content-Length framed JSON-RPC messages.
type lspWriter struct {
	w  io.Writer
	mu sync.Mutex
}

func newLSPWriter(w io.Writer) *lspWriter {
	return &lspWriter{w: w}
}

func (lw *lspWriter) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))

	lw.mu.Lock()
	defer lw.mu.Unlock()

	if _, err := io.WriteString(lw.w, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := lw.w.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// lspReader reads Content-Length framed JSON-RPC messages.
type lspReader struct {
	reader *bufio.Reader
}

func newLSPReader(r io.Reader) *lspReader {
	return &lspReader{reader: bufio.NewReaderSize(r, 64*1024)}
}

// readMessage reads one Content-Length framed message and returns the raw JSON body.
func (lr *lspReader) readMessage() ([]byte, error) {
	contentLength := -1
	for {
		line, err := lr.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// End of headers
			break
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			val := strings.TrimPrefix(line, "Content-Length: ")
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = n
		}
		// Ignore other headers (Content-Type, etc.)
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	if contentLength > 10*1024*1024 {
		return nil, fmt.Errorf("Content-Length too large: %d", contentLength)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(lr.reader, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// rpcClient manages JSON-RPC request IDs and pending responses.
type rpcClient struct {
	writer *lspWriter
	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan *jsonRPCResponse
}

func newRPCClient(w io.Writer) *rpcClient {
	c := &rpcClient{
		writer:  newLSPWriter(w),
		pending: make(map[int64]chan *jsonRPCResponse),
	}
	c.nextID.Store(1)
	return c
}

// call sends a JSON-RPC request and returns the response channel.
func (c *rpcClient) call(method string, params any) (int64, <-chan *jsonRPCResponse) {
	id := c.nextID.Add(1) - 1
	ch := make(chan *jsonRPCResponse, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.writer.writeMessage(req); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		ch <- &jsonRPCResponse{Error: &jsonRPCError{Code: -1, Message: err.Error()}}
		return id, ch
	}
	return id, ch
}

// notify sends a JSON-RPC notification (no response expected).
func (c *rpcClient) notify(method string, params any) error {
	// Notification: no ID field. We use a separate struct to omit it.
	type notification struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	return c.writer.writeMessage(notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// dispatch routes a response to the pending caller.
func (c *rpcClient) dispatch(resp *jsonRPCResponse) bool {
	var id int64
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		return false
	}

	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	if ok {
		ch <- resp
		return true
	}
	return false
}

// respond sends a JSON-RPC 2.0 response to a server-to-client request.
func (c *rpcClient) respond(id json.RawMessage, result any, rpcErr *jsonRPCError) error {
	type response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result,omitempty"`
		Error   *jsonRPCError   `json:"error,omitempty"`
	}
	return c.writer.writeMessage(response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	})
}

// cancelAll cancels all pending requests with the given error.
func (c *rpcClient) cancelAll(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan *jsonRPCResponse)
	c.pendingMu.Unlock()

	resp := &jsonRPCResponse{
		Error: &jsonRPCError{Code: -1, Message: err.Error()},
	}
	for _, ch := range pending {
		ch <- resp
	}
}
