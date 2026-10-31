package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/pencode-labs/pencode/peb"
)

type command struct {
	title    *titleJob
	action   bool
	settings peb.SettingValues
	context  peb.ActionContext
	event    peb.UIEvent
}

func main() {
	if err := serveAgent(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serveAgent(input io.Reader, output io.Writer) error {
	app := workspaceHost{tabs: make(map[string]*workspaceTab)}
	defer app.close()
	return peb.ServeExtension(input, output, peb.Contributions{
		Settings: []peb.Setting{{ID: "executablePath", Label: "Codex 可执行文件路径", Type: peb.SettingString, Default: json.RawMessage(`""`),
			Description: "填写 Codex 可执行文件或其所在目录，留空自动查找。修改后重启扩展生效。"}},
		Actions: []peb.Action{{ID: "chat", Tooltip: "Codex Agent", Icon: "resources/icons/chatgpt.svg", Location: peb.StatusBar}},
	}, peb.Handlers{
		Settings: app.configure,
		ActivateActions: func(ctx context.Context, p *peb.ActionPublisher) {
			app.ctx, app.publisher = ctx, p
		},
		Action:  app.open,
		UIEvent: app.handleEvent,
	})
}

func (a *agent) submit(c command) error {
	select {
	case a.commands <- c:
		return nil
	default:
		return fmt.Errorf("Codex 事件队列已满")
	}
}
