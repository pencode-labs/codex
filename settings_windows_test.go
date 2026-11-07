package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWindowsCodexExecutable(t *testing.T) {
	for _, relative := range []string{"codex.exe", filepath.Join("version", "bin", "CODEX.EXE")} {
		t.Run(relative, func(t *testing.T) {
			local := t.TempDir()
			t.Setenv("LOCALAPPDATA", local)
			t.Setenv("PENCODE_CODEX_PATH", "")
			directory := filepath.Join(local, "OpenAI", "Codex", "bin")
			executable := filepath.Join(directory, relative)
			if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, nil, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"", "  "} {
				if got, err := resolveExecutablePath(local, path); err != nil || got != executable {
					t.Fatalf("resolve %q = %q, %v; want %q", path, got, err, executable)
				}
			}
			if got, err := resolveExecutablePath(local, "custom-codex"); err != nil || got != "custom-codex" {
				t.Fatalf("explicit command overridden: %q, %v", got, err)
			}
			t.Setenv("PENCODE_CODEX_PATH", "env-codex")
			if got, err := resolveExecutablePath(local, ""); err != nil || got != "env-codex" {
				t.Fatalf("environment command overridden: %q, %v", got, err)
			}
		})
	}
}

func TestResolveWindowsCodexExecutableMissing(t *testing.T) {
	local := t.TempDir()
	t.Setenv("PENCODE_CODEX_PATH", "")
	for _, directory := range []string{"", local} {
		t.Setenv("LOCALAPPDATA", directory)
		if got, err := resolveExecutablePath(local, ""); err != nil || got != "codex" {
			t.Fatalf("missing installation = %q, %v; want PATH command", got, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(local, "OpenAI", "Codex", "bin", "codex.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveExecutablePath(local, ""); err != nil || got != "codex" {
		t.Fatalf("directory treated as executable: %q, %v", got, err)
	}
}
