package main

import (
	"html"
	"strings"
	"time"
)

type turnProgress struct {
	id                 string
	started            time.Time
	elapsed            time.Duration
	received, complete bool
}

func (p *turnProgress) update(t turn, now time.Time) {
	p.id = t.ID
	if p.started.IsZero() {
		p.started = now
		if t.StartedAt > 0 {
			p.started = time.Unix(t.StartedAt, 0)
		}
	}
	p.complete = t.Status != "inProgress"
	p.elapsed = max(0, now.Sub(p.started))
	if p.complete {
		if t.DurationMs != nil {
			p.elapsed = time.Duration(max(0, *t.DurationMs)) * time.Millisecond
		} else if t.CompletedAt > 0 && t.StartedAt > 0 {
			p.elapsed = time.Duration(max(0, t.CompletedAt-t.StartedAt)) * time.Second
		}
	}
}

func (c *conversation) startProgress(t turn, now time.Time) {
	if c.progress == nil || c.progress.complete || c.progress.id != "" && c.progress.id != t.ID {
		c.progress = &turnProgress{}
	}
	c.progress.update(t, now)
}

func (c *conversation) finishProgress(t turn, now time.Time) {
	if c.progress != nil && !c.progress.complete {
		c.progress.update(t, now)
	}
}

func (i *chatItem) modelText() bool {
	return (i.data.Type == "agentMessage" || i.data.Type == "reasoning" || i.data.Type == "plan") && i.text.Len() > 0
}

func (a *agent) progressVisible() bool {
	c := a.selected
	return a.workspaceOpen && c != nil && c.busy && c.progress != nil && !c.progress.complete && c.progress.received
}

func (a *agent) updateProgress(now time.Time) {
	if !a.progressVisible() {
		return
	}
	p := a.selected.progress
	elapsed := max(0, now.Sub(p.started))
	if elapsed/time.Second != p.elapsed/time.Second {
		p.elapsed = elapsed
		a.selected.dirty = true
	}
}

func (p *turnProgress) render(b *strings.Builder) {
	b.WriteString(`<div class="codex-turn-progress" role="status"`)
	if p.id != "" {
		b.WriteString(` data-ref="progress/`)
		b.WriteString(html.EscapeString(p.id))
		b.WriteString(`" data-pencode-region`)
	}
	b.WriteString(`>`)
	if !p.received && !p.complete {
		b.WriteString("正在思考")
	} else {
		b.WriteString("已处理 ")
		elapsed := p.elapsed
		if !p.complete {
			elapsed = max(0, time.Since(p.started))
		}
		seconds := int(elapsed / time.Second)
		if seconds >= 3600 {
			writeActivityCount(b, seconds/3600)
			b.WriteString("小时 ")
		}
		if seconds >= 60 {
			writeActivityCount(b, seconds/60%60)
			b.WriteString("分钟 ")
		}
		writeActivityCount(b, seconds%60)
		b.WriteString("秒")
	}
	b.WriteString(`</div>`)
}
