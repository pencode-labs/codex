package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("Codex (%d): %s", e.Code, e.Message) }

type envelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	writer   *json.Encoder
	messages chan envelope
	done     chan struct{}
	ready    chan struct{}
	err      error // Published before messages closes.
	next     uint64
	handle   func(envelope)
	commands <-chan command
	dispatch func(command)
}

func startClient(parent context.Context, root, path, serverURL string, handle func(envelope)) (*rpcClient, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = strings.TrimSpace(os.Getenv("PENCODE_CODEX_SERVER_URL"))
	}
	ctx, cancel := context.WithCancel(parent)
	c := &rpcClient{ctx: ctx, cancel: cancel, messages: make(chan envelope, 128), done: make(chan struct{}), ready: make(chan struct{}), handle: handle}
	go func() {
		defer close(c.done)
		defer close(c.messages)
		conn, err := connectServer(ctx, root, path, serverURL)
		if err != nil {
			c.err = err
			close(c.ready)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(8 << 20)
		c.writer = json.NewEncoder(socketWriter{ctx, conn})
		close(c.ready)
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				c.err = err
				return
			}
			if kind != websocket.MessageText {
				c.err = errors.New("Codex 返回了非文本 WebSocket 消息")
				return
			}
			var message envelope
			if err := json.Unmarshal(data, &message); err != nil {
				c.err = fmt.Errorf("解析 Codex 消息: %w", err)
				return
			}
			select {
			case c.messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	return c, nil
}

func (c *rpcClient) close() {
	if c == nil {
		return
	}
	c.cancel()
	<-c.done
}

// Encoder writes one complete JSON-RPC message per text frame.
type socketWriter struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (w socketWriter) Write(data []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageText, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func connectServer(ctx context.Context, root, path, endpoint string) (*websocket.Conn, error) {
	if endpoint == "" {
		endpoint, err := defaultServerURL(root)
		if err != nil {
			return nil, err
		}
		probe, cancel := context.WithTimeout(ctx, time.Second)
		conn, err := dialServer(probe, endpoint)
		cancel()
		if err == nil {
			return conn, nil
		}
		// Only an absent listener permits startup. Authentication, permissions,
		// handshake failures and timeouts must not spawn another shared server.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		path, err = resolveExecutablePath(root, path)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, path, "app-server", "daemon", "start")
		cmd.Dir, cmd.Stderr, cmd.WaitDelay = root, stderrWriter, time.Second
		if runtime.GOOS == "darwin" {
			if shellPath := loginShellPATH(os.Getenv("SHELL")); shellPath != "" {
				cmd.Env = appendPath(os.Environ(), shellPath)
			}
		}
		hideWindow(cmd)
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("启动 Codex 共享服务（%s app-server daemon start）：%w", path, err)
		}
		var daemon struct {
			SocketPath string `json:"socketPath"`
		}
		if err := json.Unmarshal(output, &daemon); err != nil {
			return nil, fmt.Errorf("读取 Codex 共享服务地址：%w", err)
		}
		if !filepath.IsAbs(daemon.SocketPath) {
			return nil, fmt.Errorf("Codex daemon 未返回绝对 socketPath，请检查 CLI 版本或设置共享服务地址")
		}
		return dialServer(ctx, "unix://"+daemon.SocketPath)
	}
	if endpoint == "unix://" {
		var err error
		endpoint, err = defaultServerURL(root)
		if err != nil {
			return nil, err
		}
	}
	return dialServer(ctx, endpoint)
}

// Codex publishes its default control socket here, including a symlink when
// CODEX_HOME is too long for a Unix socket.
func defaultServerURL(root string) (string, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("读取 Codex 用户目录：%w", err)
		}
		home = filepath.Join(home, ".codex")
	} else if !filepath.IsAbs(home) {
		home = filepath.Join(root, home)
	}
	return "unix://" + filepath.Join(home, "app-server-control", "app-server-control.sock"), nil
}

func dialServer(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	var options websocket.DialOptions
	if socket, ok := strings.CutPrefix(endpoint, "unix://"); ok {
		if !filepath.IsAbs(socket) {
			return nil, fmt.Errorf("Codex Unix socket 路径必须是绝对路径：%q", socket)
		}
		// Resolve before dial: sockaddr_un limits the supplied path length,
		// even when that path is a symlink to a short socket in /tmp.
		socket, err := filepath.EvalSymlinks(socket)
		if err != nil {
			return nil, fmt.Errorf("解析 Codex socket：%w", err)
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		defer transport.CloseIdleConnections()
		options.HTTPClient = &http.Client{Transport: transport}
		endpoint = "ws://localhost/"
	} else if !strings.HasPrefix(endpoint, "ws://") && !strings.HasPrefix(endpoint, "wss://") {
		return nil, fmt.Errorf("Codex 服务地址必须使用 ws://、wss:// 或 unix://绝对路径")
	}
	if token := os.Getenv("PENCODE_CODEX_SERVER_TOKEN"); token != "" {
		options.HTTPHeader = http.Header{"Authorization": {"Bearer " + token}}
	}
	conn, _, err := websocket.Dial(ctx, endpoint, &options)
	if err != nil {
		return nil, fmt.Errorf("连接 Codex 共享服务：%w", err)
	}
	return conn, nil
}

func appendPath(environment []string, path string) []string {
	for i, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") {
			environment[i] = "PATH=" + path
			return environment
		}
	}
	return append(environment, "PATH="+path)
}

// Daemon startup can include installation; lifecycle events must remain responsive.
func (c *rpcClient) awaitReady(timeout <-chan time.Time) error {
	if c.ready == nil {
		return nil
	}
	for {
		select {
		case cmd := <-c.commands:
			c.dispatch(cmd)
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-timeout:
			c.cancel()
			return context.DeadlineExceeded
		case <-c.ready:
			if c.writer == nil {
				return c.err
			}
			return nil
		}
	}
}

func (c *rpcClient) notify(method string, params any) error {
	return c.writer.Encode(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params})
}

func (c *rpcClient) reply(id json.RawMessage, result any, rpcErr *rpcError) error {
	return c.writer.Encode(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result,omitempty"`
		Error  *rpcError       `json:"error,omitempty"`
	}{id, result, rpcErr})
}

// The actor owns all writes; requests continue dispatching streamed events while
// awaiting their response. The socket reader never touches conversation state.
func call[T any](c *rpcClient, method string, params any) (T, error) {
	var result T
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	if err := c.awaitReady(timeout.C); err != nil {
		return result, err
	}
	c.next++
	id := c.next
	if err := c.writer.Encode(struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{id, method, params}); err != nil {
		return result, err
	}
	for {
		select {
		case cmd := <-c.commands:
			c.dispatch(cmd)
		case <-c.ctx.Done():
			return result, c.ctx.Err()
		case <-timeout.C:
			// A timed-out mutation has an unknown outcome. Disconnect instead of
			// letting a later response alter the next request's state.
			c.cancel()
			return result, fmt.Errorf("%s: %w", method, context.DeadlineExceeded)
		case message, ok := <-c.messages:
			if !ok {
				if c.err != nil {
					return result, c.err
				}
				return result, io.EOF
			}
			if message.Method != "" {
				if c.handle != nil {
					c.handle(message)
				}
				continue
			}
			var responseID uint64
			if json.Unmarshal(message.ID, &responseID) != nil || responseID != id {
				return result, errors.New("Codex response ID mismatch")
			}
			if message.Error != nil {
				return result, message.Error
			}
			return result, json.Unmarshal(message.Result, &result)
		}
	}
}
