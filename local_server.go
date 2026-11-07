package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var localServerStartup sync.Mutex

// The CLI serializes socket binding across processes. Serialize our own callers
// too, so reconnects and title generation do not launch redundant servers.
func startLocalServer(ctx context.Context, root, path, endpoint string) (string, error) {
	localServerStartup.Lock()
	defer localServerStartup.Unlock()
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	socket := strings.TrimPrefix(endpoint, "unix://")
	connect := func() (string, error) {
		resolved, err := filepath.EvalSymlinks(socket)
		if err != nil {
			return "", err
		}
		conn, err := (&net.Dialer{}).DialContext(probe, "unix", resolved)
		if err != nil {
			return "", err
		}
		_ = conn.Close()
		return resolved, nil
	}
	if resolved, err := connect(); err == nil {
		return resolved, nil
	}
	if err := probe.Err(); err != nil {
		return "", err
	}
	// The CLI creates the private control directory itself. Keep diagnostics
	// outside it so Windows ACL validation is not affected by inherited ACLs.
	log, err := os.OpenFile(filepath.Join(filepath.Dir(filepath.Dir(socket)), "pencode-app-server.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("创建 Codex 服务日志：%w", err)
	}
	defer log.Close()
	offset, err := log.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	// This service is shared and outlives a client connection or extension.
	cmd := codexCommand(context.Background(), root, path, "app-server", "--listen", endpoint)
	detachServer(cmd)
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 Codex 共享服务（%s app-server --listen %s）：%w", path, endpoint, err)
	}
	codexLog.Printf("直接启动共享 app-server: pid=%d endpoint=%q", cmd.Process.Pid, endpoint)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if resolved, err := connect(); err == nil {
			codexLog.Printf("共享 app-server 已就绪: socket=%q", resolved)
			return resolved, nil
		}
		select {
		case err = <-done:
			// A concurrent client may have bound the shared socket first.
			if resolved, probeErr := connect(); probeErr == nil {
				return resolved, nil
			}
		case <-probe.Done():
			_ = cmd.Process.Kill()
			<-done
			err = probe.Err()
		case <-ticker.C:
			continue
		}
		var diagnostic [4096]byte
		n, _ := log.ReadAt(diagnostic[:], offset)
		if err == nil {
			err = errors.New("服务已退出")
		}
		return "", fmt.Errorf("启动 Codex 共享服务（%s app-server --listen %s）失败：%w；%s", path, endpoint, err, strings.TrimSpace(string(diagnostic[:n])))
	}
}
