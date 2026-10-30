package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("Codex (%d): %s", e.Code, e.Message) }

type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	writer   *json.Encoder
	messages chan envelope
	done     chan struct{}
	err      error // Published before messages closes.
	next     uint64
	handle   func(envelope)
	commands <-chan command
	dispatch func(command)
}

func startClient(parent context.Context, root, path string, handle func(envelope)) (*rpcClient, error) {
	path, err := resolveExecutablePath(root, path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, path, "app-server")
	cmd.Dir, cmd.Stderr = root, os.Stderr
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	// EOF lets app-server shut down its threads and release their locks. Bound
	// the grace period so an unresponsive server cannot block extension exit.
	cmd.Cancel = func() error { _ = stdin.Close(); return nil }
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("启动 Codex app-server: %w（请检查 Codex 可执行文件路径；留空时需在 PATH 中提供 codex）", err)
	}
	c := &rpcClient{ctx: ctx, cancel: cancel, cmd: cmd, stdin: stdin, writer: json.NewEncoder(stdin), messages: make(chan envelope, 128), done: make(chan struct{}), handle: handle}
	go func() {
		defer close(c.done)
		defer close(c.messages)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			if ctx.Err() != nil {
				continue
			}
			var message envelope
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				c.err = fmt.Errorf("解析 Codex 消息: %w", err)
				cancel()
				continue
			}
			select {
			case c.messages <- message:
			case <-ctx.Done():
				// Drain stdout until shutdown finishes, even if the actor stopped.
			}
		}
		if err := scanner.Err(); err != nil {
			c.err = err
			cancel()
		}
		if err := cmd.Wait(); c.err == nil {
			c.err = err
		}
		if c.err == nil {
			c.err = io.EOF
		}
	}()
	return c, nil
}

func (c *rpcClient) close() {
	if c == nil {
		return
	}
	c.cancel()
	_ = c.stdin.Close()
	<-c.done
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
// awaiting their response. The pipe reader never touches conversation state.
func call[T any](c *rpcClient, method string, params any) (T, error) {
	var result T
	c.next++
	id := c.next
	if err := c.writer.Encode(struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{id, method, params}); err != nil {
		return result, err
	}
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
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
				c.handle(message)
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
