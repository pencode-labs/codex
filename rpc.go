package main

import (
	"bytes"
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
	"sync"
	"time"

	"github.com/coder/websocket"
)

type rpcError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("Codex (%d): %s", e.Code, e.Message) }

type envelope struct {
	Error  *rpcError       `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	ID     json.RawMessage `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type rpcClient struct {
	ctx      context.Context
	err      error // Published before messages closes.
	cancel   context.CancelFunc
	writer   *json.Encoder
	messages chan envelope
	done     chan struct{}
	ready    chan struct{}
	handle   func(envelope)
	commands <-chan command
	dispatch func(command)
	next     uint64
}

func startClient(parent context.Context, root, path, serverURL string, handle func(envelope)) (*rpcClient, error) {
	serverURL = strings.TrimSpace(serverURL)
	source := "setting"
	if serverURL == "" {
		serverURL = strings.TrimSpace(os.Getenv("PENCODE_CODEX_SERVER_URL"))
		source = "PENCODE_CODEX_SERVER_URL"
	}
	if serverURL == "" {
		source = "auto"
	}
	ctx, cancel := context.WithCancel(parent)
	c := &rpcClient{ctx: ctx, cancel: cancel, messages: make(chan envelope, 128), done: make(chan struct{}), ready: make(chan struct{}), handle: handle}
	codexLog.Printf("连接 %p: cwd=%q endpoint=%q source=%s", c, root, logEndpoint(serverURL), source)
	go func() {
		defer close(c.done)
		defer close(c.messages)
		defer func() {
			if ctx.Err() != nil {
				codexLog.Printf("连接 %p 已关闭: %v", c, ctx.Err())
			} else {
				codexLog.Printf("连接 %p 失败或断开: %v", c, c.err)
			}
		}()
		conn, err := connectServer(ctx, root, path, serverURL)
		if err != nil {
			c.err = err
			close(c.ready)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(8 << 20)
		c.writer = json.NewEncoder(socketWriter{ctx, conn})
		codexLog.Printf("连接 %p WebSocket 已就绪", c)
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

// A proxy forwards raw socket bytes, including the HTTP upgrade and WebSocket
// frames. Its pipes form the connection; it does not translate JSON-RPC to JSONL.
type proxyConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	stderr proxyStderr
	once   sync.Once
}

func (p *proxyConn) Read(data []byte) (int, error) {
	n, err := p.output.Read(data)
	if err != nil && p.ctx.Err() == nil {
		_ = p.Close()
		err = fmt.Errorf("Codex proxy 已断开：%w；%s", err, strings.TrimSpace(string(p.stderr.data[:p.stderr.n])))
	}
	return n, err
}

func (p *proxyConn) Write(data []byte) (int, error) { return p.input.Write(data) }

func (p *proxyConn) Close() error {
	p.once.Do(func() {
		p.cancel()
		_ = p.input.Close()
		_ = p.output.Close()
		err := p.cmd.Wait()
		codexLog.Printf("proxy 已回收: pid=%d result=%v", p.cmd.Process.Pid, err)
	})
	return nil
}

func (*proxyConn) LocalAddr() net.Addr  { return &proxyAddress }
func (*proxyConn) RemoteAddr() net.Addr { return &proxyAddress }
func (p *proxyConn) SetReadDeadline(t time.Time) error {
	return p.output.(*os.File).SetReadDeadline(t)
}
func (p *proxyConn) SetWriteDeadline(t time.Time) error {
	return p.input.(*os.File).SetWriteDeadline(t)
}
func (p *proxyConn) SetDeadline(t time.Time) error {
	return errors.Join(p.SetReadDeadline(t), p.SetWriteDeadline(t))
}

var proxyAddress = net.UnixAddr{Name: "codex app-server proxy", Net: "stdio"}

func connectServer(ctx context.Context, root, path, endpoint string) (*websocket.Conn, error) {
	if endpoint != "" && !strings.HasPrefix(endpoint, "unix://") {
		return dialServer(ctx, endpoint, nil)
	}
	path, err := resolveExecutablePath(root, path)
	if err != nil {
		return nil, err
	}
	codexLog.Printf("Codex 可执行文件: %q", path)
	socket, err := proxySocket(ctx, root, path, endpoint)
	if err != nil {
		return nil, err
	}
	args := []string{"app-server", "proxy"}
	if socket != "" {
		args = append(args, "--sock", socket)
	}
	proxyCtx, cancel := context.WithCancel(ctx)
	p := &proxyConn{ctx: proxyCtx, cancel: cancel}
	p.cmd = codexCommand(proxyCtx, root, path, args...)
	p.cmd.Stderr = io.MultiWriter(stderrWriter, &p.stderr)
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	p.output, err = p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = p.input.Close()
		return nil, err
	}
	if err := startCodexCommand(p.cmd); err != nil {
		cancel()
		_ = p.input.Close()
		_ = p.output.Close()
		return nil, fmt.Errorf("启动 Codex proxy（%s）：%w", path, err)
	}
	codexLog.Printf("proxy 已启动: pid=%d socket=%q", p.cmd.Process.Pid, socket)
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return p, nil }}
	defer transport.CloseIdleConnections()
	conn, err := dialServer(ctx, "ws://localhost/", transport)
	if err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("连接 Codex proxy：%w；%s", err, strings.TrimSpace(string(p.stderr.data[:p.stderr.n])))
	}
	return conn, nil
}

// Keep proxy failures visible without retaining an unbounded diagnostic stream.
type proxyStderr struct {
	data [4096]byte
	n    int
}

func (s *proxyStderr) Write(data []byte) (int, error) {
	s.n += copy(s.data[s.n:], data)
	return len(data), nil
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

func proxySocket(ctx context.Context, root, path, endpoint string) (string, error) {
	if endpoint == "" {
		endpoint, err := defaultServerURL(root)
		if err != nil {
			return "", err
		}
		codexLog.Printf("探测默认 app-server: %s", endpoint)
		socket, err := filepath.EvalSymlinks(strings.TrimPrefix(endpoint, "unix://"))
		probe, cancel := context.WithTimeout(ctx, time.Second)
		if err == nil {
			var conn net.Conn
			conn, err = (&net.Dialer{}).DialContext(probe, "unix", socket)
			if err == nil {
				_ = conn.Close()
			}
		}
		cancel()
		if err == nil {
			codexLog.Printf("复用已有 app-server: socket=%q", socket)
			return socket, nil
		}
		// Only an absent listener permits startup. The proxy reports handshake
		// failures without starting a second server.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, connectionRefused) {
			return "", err
		}
		codexLog.Printf("默认 app-server 不可用，启动共享 daemon: %v", err)
		cmd := codexCommand(ctx, root, path, "app-server", "daemon", "start")
		var diagnostic proxyStderr
		cmd.Stderr = io.MultiWriter(stderrWriter, &diagnostic)
		var output bytes.Buffer
		cmd.Stdout = &output
		started := time.Now()
		err = startCodexCommand(cmd)
		if err == nil {
			err = cmd.Wait()
		}
		if err != nil {
			message := strings.TrimSpace(string(diagnostic.data[:diagnostic.n]))
			if strings.Contains(message, "this CLI has no complete local package") {
				return startLocalServer(ctx, root, path, endpoint)
			}
			return "", fmt.Errorf("启动 Codex 共享服务（%s app-server daemon start）：%w；%s", path, err, message)
		}
		var daemon struct {
			SocketPath string `json:"socketPath"`
		}
		if err := json.Unmarshal(output.Bytes(), &daemon); err != nil {
			return "", fmt.Errorf("读取 Codex 共享服务地址：%w", err)
		}
		if !filepath.IsAbs(daemon.SocketPath) {
			return "", fmt.Errorf("Codex daemon 未返回绝对 socketPath，请检查 CLI 版本或设置共享服务地址")
		}
		codexLog.Printf("共享 daemon 已就绪: socket=%q elapsed=%s", daemon.SocketPath, time.Since(started))
		return daemon.SocketPath, nil
	}
	socket := strings.TrimPrefix(endpoint, "unix://")
	if socket != "" && !filepath.IsAbs(socket) {
		return "", fmt.Errorf("Codex Unix socket 路径必须是绝对路径：%q", socket)
	}
	if socket == "" {
		codexLog.Print("使用 proxy 默认 socket，跳过 daemon 自动启动")
	} else {
		codexLog.Printf("使用指定 app-server: socket=%q，跳过 daemon 自动启动", socket)
	}
	return socket, nil
}

func codexCommand(ctx context.Context, root, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir, cmd.Stderr, cmd.WaitDelay = root, stderrWriter, time.Second
	if runtime.GOOS == "darwin" {
		if shellPath := loginShellPATH(os.Getenv("SHELL")); shellPath != "" {
			cmd.Env = appendPath(os.Environ(), shellPath)
			codexLog.Print("启动环境使用登录交互式 shell 的 PATH")
		}
	}
	hideWindow(cmd)
	codexLog.Printf("启动进程: executable=%q args=%q cwd=%q", cmd.Path, args, cmd.Dir)
	return cmd
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

func dialServer(ctx context.Context, endpoint string, transport *http.Transport) (*websocket.Conn, error) {
	var options websocket.DialOptions
	if !strings.HasPrefix(endpoint, "ws://") && !strings.HasPrefix(endpoint, "wss://") {
		return nil, fmt.Errorf("Codex 服务地址必须使用 ws://、wss:// 或 unix://绝对路径")
	}
	if token := os.Getenv("PENCODE_CODEX_SERVER_TOKEN"); token != "" {
		options.HTTPHeader = http.Header{"Authorization": {"Bearer " + token}}
	}
	if transport != nil {
		options.HTTPClient = &http.Client{Transport: transport}
	}
	codexLog.Printf("WebSocket 握手: endpoint=%q proxy=%t bearer=%t", logEndpoint(endpoint), transport != nil, len(options.HTTPHeader) != 0)
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
	err := c.writer.Encode(struct {
		Params any    `json:"params"`
		Method string `json:"method"`
	}{Method: method, Params: params})
	if err != nil {
		codexLog.Printf("连接 %p RPC 通知失败: method=%s error=%v", c, method, err)
	}
	return err
}

func (c *rpcClient) reply(id json.RawMessage, result any, rpcErr *rpcError) error {
	err := c.writer.Encode(struct {
		Result any             `json:"result,omitempty"`
		Error  *rpcError       `json:"error,omitempty"`
		ID     json.RawMessage `json:"id"`
	}{ID: id, Result: result, Error: rpcErr})
	if err != nil || rpcErr != nil {
		codexLog.Printf("连接 %p RPC 回复失败: rpcError=%v error=%v", c, rpcErr, err)
	}
	return err
}

// The actor owns all writes; requests continue dispatching streamed events while
// awaiting their response. The transport reader never touches conversation state.
func call[T any](c *rpcClient, method string, params any) (result T, err error) {
	defer func() {
		if err != nil {
			codexLog.Printf("连接 %p RPC 失败: method=%s error=%v", c, method, err)
		}
	}()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	if err := c.awaitReady(timeout.C); err != nil {
		return result, err
	}
	c.next++
	id := c.next
	if err := c.writer.Encode(struct {
		Params any    `json:"params"`
		Method string `json:"method"`
		ID     uint64 `json:"id"`
	}{ID: id, Method: method, Params: params}); err != nil {
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
