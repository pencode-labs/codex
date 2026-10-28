package main

import (
	"html"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type activityKind uint8

const (
	activityEdit activityKind = 1 << iota
	activityRead
	activityList
	activitySearch
	activityCommand
	activityTool
	activityReasoning
)

type activityInfo struct {
	kind        activityKind
	label, icon string
}

var activityKinds = [...]activityInfo{
	{activityEdit, "编辑了文件", "pencil"},
	{activityRead, "读取了文件", "book-open"},
	{activityList, "列出了文件", "folder"},
	{activitySearch, "进行了搜索", "search"},
	{activityCommand, "运行了命令", "terminal"},
	{activityTool, "调用了工具", "wrench"},
	{activityReasoning, "思考摘要", "wrench"},
}

func (k activityKind) primary() (result activityInfo) {
	for _, entry := range activityKinds {
		if k&entry.kind != 0 {
			return entry
		}
	}
	return
}

func (a commandAction) kind() activityKind {
	switch a.Type {
	case "read":
		return activityRead
	case "listFiles":
		return activityList
	case "search":
		return activitySearch
	default:
		return activityCommand
	}
}

func (i *chatItem) activityKinds() activityKind {
	switch i.data.Type {
	case "reasoning":
		return activityReasoning
	case "fileChange":
		return activityEdit
	case "webSearch":
		return activitySearch
	case "mcpToolCall", "dynamicToolCall", "collabToolCall":
		return activityTool
	case "commandExecution":
		if len(i.data.CommandActions) == 0 {
			return activityCommand
		}
		var kinds activityKind
		for _, action := range i.data.CommandActions {
			kinds |= action.kind()
		}
		return kinds
	}
	return 0
}

func renderActivityGroup(b *strings.Builder, bindings []peb.EditorBinding, items []*chatItem, sweep bool) []peb.EditorBinding {
	first := items[0]
	if first.groupRef == "" {
		first.groupRef = "activity/" + first.data.ID
	}
	var kinds activityKind
	running, failed, declined := false, false, false
	for _, i := range items {
		kinds |= i.activityKinds()
		// Keep failures visible even when the group is collapsed or still running.
		switch i.data.Status {
		case "failed":
			failed = true
		case "declined":
			declined = true
		case "inProgress":
			running = true
		}
	}
	// Keep expansion and streamed activity updates inside this group so the
	// transcript, its scroll container and the composer retain their nodes.
	b.WriteString(`<section class="codex-activity-group" data-ref="`)
	b.WriteString(html.EscapeString(first.groupRef))
	b.WriteString(`/region" data-pencode-region><button class="codex-activity-summary" type="button" data-ref="`)
	b.WriteString(html.EscapeString(first.groupRef))
	b.WriteString(`" aria-expanded="`)
	b.WriteString(strconv.FormatBool(first.groupExpanded))
	b.WriteString(`"><img src="resources/icons/`)
	b.WriteString(kinds.primary().icon)
	b.WriteString(`.svg" alt=""><span>`)
	// Thinking stays in the timeline without making mixed group titles verbose.
	if kinds != activityReasoning {
		kinds &^= activityReasoning
	}
	if sweep && !first.groupExpanded {
		var label strings.Builder
		renderActivityTitle(&label, kinds)
		renderSweepText(b, label.String())
	} else {
		renderActivityTitle(b, kinds)
	}
	b.WriteString(`</span>`)
	if running {
		renderActivityStatus(b, "inProgress")
	}
	if failed {
		renderActivityStatus(b, "failed")
	}
	if declined {
		renderActivityStatus(b, "declined")
	}
	b.WriteString(`<img class="codex-activity-chevron" src="resources/icons/chevron-down.svg" alt=""></button>`)
	bindings = append(bindings, peb.EditorBinding{Ref: first.groupRef, Kind: "click"})
	if first.groupExpanded {
		b.WriteString(`<div class="codex-activity-list" data-ref="`)
		b.WriteString(html.EscapeString(first.groupRef))
		b.WriteString(`/list" data-pencode-scroll="preserve">`)
		for _, i := range items {
			b.WriteString(i.html)
			if i.detailRef != "" {
				bindings = append(bindings, peb.EditorBinding{Ref: i.detailRef, Kind: "click"})
			}
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return bindings
}

func renderActivityTitle(b *strings.Builder, kinds activityKind) {
	separator := ""
	for _, entry := range activityKinds {
		if kinds&entry.kind != 0 {
			b.WriteString(separator)
			b.WriteString(entry.label)
			separator = "、"
		}
	}
}

func renderActivityStatus(b *strings.Builder, status string) {
	label := status
	switch status {
	case "", "completed":
		return
	case "inProgress":
		label = "进行中"
	case "failed":
		label = "失败"
	case "declined":
		label = "已拒绝"
	}
	b.WriteString(`<span class="codex-activity-status`)
	if status == "failed" || status == "declined" {
		b.WriteString(` codex-status`)
	}
	b.WriteString(`">`)
	b.WriteString(html.EscapeString(label))
	b.WriteString(`</span>`)
}

func (i *chatItem) renderActivityLabel(b *strings.Builder, fallback string) {
	switch i.data.Type {
	case "commandExecution":
		if len(i.data.CommandActions) == 0 {
			b.WriteString("已运行 ")
			b.WriteString(html.EscapeString(i.data.Command))
		}
		for index, action := range i.data.CommandActions {
			if index > 0 {
				b.WriteString("；")
			}
			switch action.Type {
			case "read":
				b.WriteString("已读取 ")
				name := action.Name
				if name == "" {
					name = action.Path
				}
				b.WriteString(html.EscapeString(name))
			case "listFiles":
				b.WriteString("已列出 ")
				if action.Path != "" {
					b.WriteString(html.EscapeString(action.Path))
					b.WriteString(" 中的")
				}
				b.WriteString("文件")
			case "search":
				b.WriteString("已在 ")
				if action.Path == "" {
					b.WriteString("项目")
				} else {
					b.WriteString(html.EscapeString(action.Path))
				}
				b.WriteString(" 中搜索")
				if action.Query != "" {
					b.WriteString("“")
					b.WriteString(html.EscapeString(action.Query))
					b.WriteString("”")
				}
			default:
				b.WriteString("已运行 ")
				b.WriteString(html.EscapeString(action.Command))
			}
		}
	case "fileChange":
		if len(i.data.Changes) == 0 {
			b.WriteString(fallback)
			return
		}
		b.WriteString("已编辑 ")
		for index, change := range i.data.Changes {
			if index > 0 {
				b.WriteString("、")
			}
			b.WriteString(html.EscapeString(filepath.Base(change.Path)))
			added, removed := 0, 0
			for line := range strings.Lines(change.Diff) {
				switch {
				case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
					added++
				case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
					removed++
				}
			}
			b.WriteString(" +")
			writeActivityCount(b, added)
			b.WriteString(" -")
			writeActivityCount(b, removed)
		}
	case "reasoning":
		if text := strings.TrimSpace(i.text.String()); text != "" {
			line, _, _ := strings.Cut(text, "\n")
			if line = strings.Trim(line, "*# \t\r"); line != "" {
				b.WriteString(html.EscapeString(line))
				return
			}
		}
		b.WriteString(fallback)
	default:
		b.WriteString(html.EscapeString(fallback))
	}
}

func writeActivityCount(b *strings.Builder, count int) {
	var buffer [20]byte
	b.Write(strconv.AppendInt(buffer[:0], int64(count), 10))
}
