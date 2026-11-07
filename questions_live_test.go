package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

// A real CLI must advertise and execute its built-in tool, rather than a fixture
// injecting the server request that the UI already knows how to render.
func TestLiveQuestionToolInDefaultMode(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "new"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) { testLiveQuestionTool(t, resume, false) })
	}
	t.Run("async", func(t *testing.T) { testLiveQuestionTool(t, false, true) })
}

func testLiveQuestionTool(t *testing.T, resume, async bool) {
	liveCodexHome(t)
	toolName := "request_user_input"
	arguments := `{"questions":[{"id":"goal","header":"工作方向","question":"你希望我接下来协助你做什么？","options":[{"label":"审查现有代码","description":"检查问题"},{"label":"实现一个新功能","description":"开发需求"}],"isOther":true}]}`
	if async {
		toolName = "request_user_input_async"
		arguments = `{"questions":[{"title":"你希望我接下来协助你做什么？","options":["审查现有代码","实现一个新功能"]}]}`
	}
	available := make(chan bool, 4)
	answers := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Generate *bool           `json:"generate"`
			Tools    json.RawMessage `json:"tools"`
			Input    []struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Output  string          `json:"output"`
				Tools   json.RawMessage `json:"tools"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Generate != nil && !*request.Generate {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"warmup\",\"status\":\"completed\",\"output\":[]}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(value any) {
			data, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": "question-response", "status": "in_progress", "output": []any{}}})
		answered := false
		for index := len(request.Input) - 1; index >= 0; index-- {
			item := request.Input[index]
			if item.Type == "message" && item.Role == "user" {
				if async && strings.Contains(string(item.Content), "审查现有代码") {
					answers <- string(item.Content)
					answered = true
				}
				break
			}
			if item.Type == "function_call_output" {
				if !async {
					answers <- item.Output
				}
				answered = true
				break
			}
		}
		text := "Question answered"
		if !answered {
			// Match the tool's own name, not mentions in another tool's description.
			var tools any
			_ = json.Unmarshal(request.Tools, &tools)
			enabled := containsQuestionTool(tools, toolName)
			for _, item := range request.Input {
				if item.Type == "additional_tools" {
					_ = json.Unmarshal(item.Tools, &tools)
					enabled = enabled || containsQuestionTool(tools, toolName)
				}
			}
			available <- enabled
			if enabled {
				emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]string{"type": "function_call", "id": "question-call", "call_id": "question-call", "name": toolName, "arguments": arguments}})
			} else {
				text = "Question tool unavailable"
			}
			answered = !enabled
		}
		if answered {
			emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "id": "reply", "role": "assistant", "phase": "final_answer", "content": []map[string]string{{"type": "output_text", "text": text}}}})
		}
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "question-response", "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
	}))
	defer provider.Close()
	config := fmt.Sprintf("model = %q\nmodel_provider = %q\n[model_providers.local_fixture]\nname = %q\nbase_url = %q\nwire_api = %q\nrequires_openai_auth = false\nsupports_websockets = false\n", "gpt-6.1-sol", "local_fixture", "Local fixture", provider.URL, "responses")
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation)}
	defer a.closeClient()
	wait := func(t *testing.T, done func() bool) {
		t.Helper()
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		for !done() {
			select {
			case message, ok := <-a.client.messages:
				if !ok {
					t.Fatal("app-server disconnected", a.client.err)
				}
				a.onMessage(message)
			case <-timeout.C:
				t.Fatal("app-server did not finish the question flow")
			}
		}
	}
	var c *conversation
	if resume {
		if !t.Run("seed-disabled-thread", func(t *testing.T) {
			liveAppServer(t, root, "")
			a.ctx = t.Context()
			defer a.closeClient()
			if err := a.connect(); err != nil {
				t.Fatal(err)
			}
			// Persist an old thread with the default feature disabled.
			result, err := call[struct {
				Thread thread `json:"thread"`
			}](a.client, "thread/start", map[string]any{"cwd": root})
			if err != nil {
				t.Fatal(err)
			}
			c = &conversation{thread: result.Thread, context: a.context, loaded: true}
			a.conversations[c.ID] = c
			a.selectConversation(c)
			if err := a.startTurn(c, []input{{Type: "text", Text: "Seed the existing thread"}}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-available:
			case <-time.After(10 * time.Second):
				t.Fatal("baseline did not reach the local provider")
			}
			wait(t, func() bool { return !c.busy })
			select {
			case answer := <-answers:
				if !strings.Contains(answer, "unavailable in Default mode") {
					t.Fatal("baseline did not reproduce the disabled Default mode handler", answer)
				}
			default:
				t.Fatal("baseline did not exercise the question tool")
			}
		}) {
			return
		}
		// Loaded sessions keep their feature set; restart the owned test server
		// before resuming the persisted thread with the new configuration.
		a.ctx = t.Context()
		liveAppServer(t, root, "")
		if err := a.resume(c.ID); err != nil {
			t.Fatal(err)
		}
		c = a.selected
	} else {
		liveAppServer(t, root, "")
		var err error
		c, err = a.startThread()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.startTurn(c, []input{{Type: "text", Text: "用提问工具问我一个问题"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case enabled := <-available:
		if !enabled {
			t.Fatal("Default mode did not advertise request_user_input to the model")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("local provider was not reached")
	}
	wait(t, func() bool { return c.question("") != nil })
	if async {
		wait(t, func() bool { return !c.busy })
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `class="codex-question-card"`) || strings.Index(view.HTML, "你希望我接下来协助你做什么？") < strings.Index(view.HTML, `<section class="codex-composer">`) {
		t.Fatal("real question tool did not show a card above the composer", view.HTML)
	}
	ref := c.question("").ref + "/0/"
	questionEvent(t, a, ref+"select/0", "click", "")
	questionEvent(t, a, ref+"next", "click", "")
	wait(t, func() bool { return !c.busy })
	select {
	case answer := <-answers:
		if !strings.Contains(answer, "审查现有代码") {
			t.Fatal("question answer did not reach the model", answer)
		}
	default:
		t.Fatal("model did not receive a question answer")
	}
	if c.question("") != nil || !strings.Contains(a.composerView().HTML, "Question answered") {
		t.Fatal("question did not resolve and resume the assistant")
	}
}

func containsQuestionTool(value any, name string) bool {
	switch value := value.(type) {
	case map[string]any:
		if value["name"] == name {
			return true
		}
		for _, child := range value {
			if containsQuestionTool(child, name) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsQuestionTool(child, name) {
				return true
			}
		}
	}
	return false
}
