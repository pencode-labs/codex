package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func testImageURL(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 320, 200))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestPastedImagesFollowDraftAndCanBeRemoved(t *testing.T) {
	a := &agent{context: peb.ActionContext{ProjectPath: "one"}}
	e := peb.EditorEvent{Kind: "paste", Ref: "prompt", InputType: "image/png", Data: testImageURL(t), Value: "看这张图"}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: e}}); err != nil {
		t.Fatal(err)
	}
	images := a.images[a.attachmentScope()]
	if len(images) != 1 || images[0].url != e.Data || a.draft() != e.Value {
		t.Fatal("paste lost image bytes or current text")
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(images[0].preview, "data:image/png;base64,"))
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 160 || config.Height != 100 {
		t.Fatal("preview did not preserve aspect ratio", config, err)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-pencode-paste-images`) || !strings.Contains(view.HTML, images[0].preview) || !strings.Contains(view.HTML, `data-ref="remove-image/0"`) {
		t.Fatal("missing paste support, preview or remove action")
	}
	a.selected = &conversation{thread: thread{ID: "other"}}
	if len(a.images[a.attachmentScope()]) != 0 {
		t.Fatal("image leaked into another conversation")
	}
	a.selected = nil
	a.context.ProjectPath = "two"
	if len(a.images[a.attachmentScope()]) != 0 {
		t.Fatal("image leaked into another project")
	}
	a.context.ProjectPath = "one"
	a.removeImage("-1")
	a.removeImage("1")
	if len(a.images[a.attachmentScope()]) != 1 {
		t.Fatal("invalid removal changed images")
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "remove-image/0"}}}); err != nil {
		t.Fatal(err)
	}
	if len(a.images) != 0 || a.draft() != e.Value {
		t.Fatal("removal lost text or retained image")
	}
}

func TestSendImagesWithTextAndRetry(t *testing.T) {
	for _, prompt := range []string{"解释图片", ""} {
		t.Run(prompt, func(t *testing.T) {
			var output bytes.Buffer
			client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2)}
			client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcError{Code: -32000, Message: "retry"}}
			client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"completed"}}`)}
			c := &conversation{thread: thread{ID: "saved", Name: "task"}, loaded: true}
			a := &agent{client: client, selected: c}
			source := testImageURL(t)
			if err := a.pasteImage(peb.EditorEvent{InputType: "image/png", Data: source, Value: prompt}); err != nil {
				t.Fatal(err)
			}
			if err := a.send(); err == nil || c.draft != prompt || len(a.images[a.attachmentScope()]) != 1 || c.busy {
				t.Fatal("failed send must preserve the draft and images", err)
			}
			if err := a.send(); err != nil || c.draft != "" || len(a.images) != 0 {
				t.Fatal("successful retry must clear the draft and images", err)
			}
			decoder := json.NewDecoder(&output)
			for range 2 {
				var request struct {
					Method string
					Params struct{ Input []input }
				}
				if err := decoder.Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Method != "turn/start" || len(request.Params.Input) != 2 || request.Params.Input[0].Text != prompt || request.Params.Input[1].Type != "image" || request.Params.Input[1].URL != source {
					t.Fatalf("image was not sent alongside text: %+v", request)
				}
			}
		})
	}
}

func TestImageDraftMigratesToNewThreadOnFailedSend(t *testing.T) {
	var output bytes.Buffer
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2)}
	client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"new","name":"task"}}`)}
	client.messages <- envelope{ID: json.RawMessage(`2`), Error: &rpcError{Code: -32000, Message: "retry"}}
	a := &agent{client: client, conversations: make(map[string]*conversation)}
	if err := a.pasteImage(peb.EditorEvent{InputType: "image/png", Data: testImageURL(t), Value: "task"}); err != nil {
		t.Fatal(err)
	}
	old := a.attachmentScope()
	if err := a.send(); err == nil || a.selected == nil || a.selected.ID != "new" || len(a.images[a.attachmentScope()]) != 1 || len(a.images[old]) != 0 {
		t.Fatal("new thread lost or duplicated the image draft", err)
	}
	a.client = nil
	a.handleLifecycle(peb.LifecycleEvent{Kind: peb.ProjectClosed})
	if len(a.images) != 0 {
		t.Fatal("project close retained images")
	}
}

func TestImageMessagesAndInvalidPastes(t *testing.T) {
	source := testImageURL(t)
	i := newChatItem(threadItem{Type: "userMessage", Content: []input{{Type: "text", Text: "task"}, {Type: "image", URL: source}}}, true)
	var b strings.Builder
	i.render(&b, "", "")
	if !strings.Contains(b.String(), "task") || !strings.Contains(b.String(), `alt="已发送图片"`) || i.data.Content != nil {
		t.Fatal("message lost image preview or retained large payload")
	}
	a := &agent{}
	for _, data := range []string{"data:image/png;base64,broken", "data:image/svg+xml;base64,PHN2Zz4=", "https://example.com/image.png", strings.Repeat("x", peb.MaxDocument+1)} {
		if err := a.pasteImage(peb.EditorEvent{InputType: "image/png", Data: data, Value: "keep"}); err == nil || len(a.images) != 0 || a.draft() != "keep" {
			t.Fatal("invalid image was accepted or lost text", err)
		}
	}
	if err := a.pasteImage(peb.EditorEvent{InputType: "error", Data: "too large", Value: "keep"}); err == nil || err.Error() != "too large" {
		t.Fatal("paste error was hidden", err)
	}
}
