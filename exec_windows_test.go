package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCodexTokenFixture(t *testing.T) {
	if os.Getenv("PENCODE_CODEX_TOKEN_FIXTURE") != "1" {
		t.Skip("subprocess helper")
	}
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	elevated, err := tokenInformation[uint32](token, syscall.TokenElevation)
	if err != nil || elevated != 0 {
		t.Fatalf("Codex child inherited administrator privileges: elevation=%d, err=%v", elevated, err)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "stdio probe" {
		t.Fatalf("Codex child lost stdin: %q, %v", input, err)
	}
	fmt.Println("Codex child token verified")
}

func TestLiveCodexShellParent(t *testing.T) {
	if os.Getenv("PENCODE_CODEX_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_INTEGRATION=1 for a local CLI smoke test")
	}
	if windows.GetShellWindow() == 0 {
		t.Skip("requires an interactive desktop shell")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	for _, detached := range []bool{false, true} {
		name := "client"
		if detached {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			cmd := codexCommand(t.Context(), home, path, "--version")
			if detached {
				detachServer(cmd)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := startCodexWithShellParent(cmd, token); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("Codex startup: %v\n%s", err, &output)
			}
			if !strings.Contains(output.String(), "codex") {
				t.Fatalf("missing Codex version:\n%s", &output)
			}
		})
	}
}

// Force the shell-parent path even in ordinary test runs, so it is exercised
// without requiring an elevated test runner.
func TestCodexProcessRunsUnelevated(t *testing.T) {
	t.Setenv("PENCODE_CODEX_TOKEN_FIXTURE", "1")
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client", "server", "desktop-client", "desktop-server"} {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(name, "desktop-") && windows.GetShellWindow() == 0 {
				t.Skip("requires an interactive desktop shell")
			}
			cmd := codexCommand(t.Context(), t.TempDir(), path, "-test.run=^TestCodexTokenFixture$")
			cmd.Stdin = strings.NewReader("stdio probe")
			if strings.HasSuffix(name, "server") {
				detachServer(cmd)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			var err error
			if strings.HasPrefix(name, "desktop-") {
				token, tokenErr := syscall.OpenCurrentProcessToken()
				if tokenErr != nil {
					t.Fatal(tokenErr)
				}
				defer token.Close()
				err = startCodexWithShellParent(cmd, token)
			} else {
				err = startCodexCommand(cmd)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("Codex child: %v\n%s", err, &output)
			}
			if !strings.Contains(output.String(), "Codex child token verified") {
				t.Fatalf("Codex child lost its inherited environment:\n%s", &output)
			}
		})
	}
}
