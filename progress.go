package main

import (
	"strconv"
	"time"

	"github.com/onix-engine/onix/html/markup"
)

type turnProgress struct {
	started   time.Time
	id        string
	toggleRef string
	elapsed   time.Duration
	// Live item positions stay stable when older history is prepended.
	liveItems, lastOutput, followupFrom int
	received, complete                  bool
	expanded                            bool
}

func (c *conversation) turnProgress(id string) *turnProgress {
	if c.progress != nil && c.progress.id == id {
		return c.progress
	}
	for _, i := range c.items {
		if i.progress != nil && i.progress.id == id {
			return i.progress
		}
	}
	return nil
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

func (i *chatItem) receivedModelOutput() {
	if p := i.progress; p != nil {
		p.received = true
		p.lastOutput = max(p.lastOutput, i.liveOrder)
	}
}

func (p *turnProgress) hasReply() bool {
	return p.received && p.lastOutput >= p.followupFrom
}

func (a *agent) progressVisible() bool {
	c := a.selected
	return a.workspaceOpen && c != nil && c.busy && c.progress != nil && !c.progress.complete && c.progress.hasReply()
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

func (p *turnProgress) render(b *markup.Builder, collapsible bool) {
	thinking := !p.complete && !p.hasReply()
	progress := b.Open("div", "class", "codex-turn-progress", "role", "status")
	if p.id != "" {
		progress.SetAttr("data-ref", "progress/"+p.id)
		progress.SetAttr("data-pencode-region", "")
	}
	if thinking {
		progress.SetAttr("aria-label", "正在思考")
	}
	if collapsible {
		if p.toggleRef == "" {
			p.toggleRef = "turn/" + p.id
		}
		label := "展开历史消息"
		if p.expanded {
			label = "收起历史消息"
		}
		b.Open("button", "class", "codex-turn-toggle", "type", "button", "data-ref", p.toggleRef, "aria-expanded", strconv.FormatBool(p.expanded), "title", label, "aria-label", label)
	}
	if thinking {
		renderSweepText(b, "正在思考")
	} else {
		label, hours, minutes, unit := "已处理 ", "小时 ", "分钟 ", "秒"
		if p.complete {
			label, hours, minutes, unit = "用时 ", "h ", "m ", "s"
		}
		b.Text(label)
		elapsed := p.elapsed
		if !p.complete {
			elapsed = max(0, time.Since(p.started))
		}
		seconds := int(elapsed / time.Second)
		if seconds >= 3600 {
			b.Text(strconv.Itoa(seconds / 3600))
			b.Text(hours)
		}
		if seconds >= 60 {
			b.Text(strconv.Itoa(seconds / 60 % 60))
			b.Text(minutes)
		}
		b.Text(strconv.Itoa(seconds % 60))
		b.Text(unit)
	}
	if collapsible {
		b.Element("img", "src", "resources/icons/chevron-down.svg", "alt", "")
		b.End()
	}
	b.End()
}
