package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pencode-labs/pencode/peb"
)

type command struct {
	action  bool
	context peb.ActionContext
	event   peb.UIEvent
}

func main() {
	if err := serveAgent(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serveAgent(input io.Reader, output io.Writer) error {
	app := &agent{commands: make(chan command, 64)}
	err := peb.ServeExtension(input, output, peb.Contributions{Actions: []peb.Action{{ID: "chat", Text: "Codex", Tooltip: "Codex Agent", Location: peb.StatusBar}}}, peb.Handlers{
		ActivateActions: func(ctx context.Context, p *peb.ActionPublisher) {
			app.ctx, app.publisher = ctx, p
			app.done = make(chan struct{})
			go app.run()
		},
		Action:  func(c peb.ActionContext) error { return app.submit(command{action: true, context: c}) },
		UIEvent: func(e peb.UIEvent) error { return app.submit(command{event: e}) },
	})
	if app.done != nil {
		<-app.done
	}
	return err
}

func (a *agent) submit(c command) error {
	select {
	case a.commands <- c:
		return nil
	default:
		return fmt.Errorf("Codex 事件队列已满")
	}
}
