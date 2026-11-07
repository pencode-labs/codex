package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestLiveModelAsyncQuestion(t *testing.T) {
	if os.Getenv("PENCODE_CODEX_MODEL_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_MODEL_INTEGRATION=1 to exercise the configured model provider")
	}
	t.Setenv("PENCODE_CODEX_INTEGRATION", "1")
	original := os.Getenv("CODEX_HOME")
	if original == "" {
		original = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	liveCodexHome(t)
	for _, name := range []string{"config.toml", "auth.json"} {
		data, err := os.ReadFile(filepath.Join(original, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	liveAppServer(t, root, "")
	a := &agent{ctx: t.Context(), model: "gpt-6.1-sol", context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation)}
	defer a.closeClient()
	c, err := a.startThread()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.startTurn(c, []input{{Type: "text", Text: "你 ask 工具问我一个问题"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	answer, echoed := "", false
	for {
		select {
		case msg, ok := <-a.client.messages:
			if !ok {
				t.Fatal("disconnected", a.client.err)
			}
			a.onMessage(msg)
			if answer != "" {
				for _, item := range c.items {
					if item.data.Type == "userMessage" && strings.Contains(item.text.String(), answer) {
						echoed = true
					}
				}
				if msg.Method == "turn/completed" {
					if !echoed || c.status != "" {
						t.Fatal("answer did not reach the resumed model turn", c.status)
					}
					t.Log("real ask card displayed after turn completion; selected answer reached the next model turn")
					return
				}
				continue
			}
			if !c.busy {
				p := c.question("")
				if p == nil {
					t.Fatal("turn ended without question card", c.status)
				}
				if len(p.Questions) != 1 || len(p.Questions[0].Options) == 0 {
					t.Fatal("ask did not offer a selectable question")
				}
				view := a.composerView()
				validateBindings(t, view)
				if !strings.Contains(view.HTML, `class="codex-question-card"`) {
					t.Fatal("question did not render as card")
				}
				ref := p.ref + "/0/"
				answer = p.Questions[0].Options[0].Label
				questionEvent(t, a, ref+"select/0", "click", "")
				questionEvent(t, a, ref+"next", "click", "")
			}
		case <-deadline.C:
			t.Fatal("question timeout", c.status)
		}
	}
}
