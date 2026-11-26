package main

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
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
	label, icon string
	kind        activityKind
}

var activityKinds = [...]activityInfo{
	{kind: activityEdit, label: "编辑了文件", icon: "pencil"},
	{kind: activityRead, label: "读取了文件", icon: "book-open"},
	{kind: activityList, label: "列出了文件", icon: "folder"},
	{kind: activitySearch, label: "进行了搜索", icon: "search"},
	{kind: activityCommand, label: "运行了命令", icon: "terminal"},
	{kind: activityTool, label: "调用了工具", icon: "wrench"},
	{kind: activityReasoning, label: "思考", icon: "wrench"},
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

func renderActivityGroup(b *markup.Builder, bindings []peb.EditorBinding, items []*chatItem, sweep bool) []peb.EditorBinding {
	first := items[0]
	if first.groupRef == "" {
		first.groupRef = "activity/" + first.data.ID
	}
	var kinds activityKind
	running, declined := false, false
	for _, i := range items {
		kinds |= i.activityKinds()
		switch i.data.Status {
		case "declined":
			declined = true
		case "inProgress":
			running = true
		}
	}
	// Keep expansion and streamed activity updates inside this group so the
	// transcript, its scroll container and the composer retain their nodes.
	b.Open("section", "class", "codex-activity-group", "data-ref", first.groupRef+"/region", "data-pencode-region", "")
	b.Open("button", "class", "codex-activity-summary", "type", "button", "data-ref", first.groupRef, "aria-expanded", strconv.FormatBool(first.groupExpanded))
	b.Element("img", "src", "resources/icons/"+kinds.primary().icon+".svg", "alt", "")
	b.Open("span")
	// Thinking stays in the timeline without making mixed group titles verbose.
	if kinds != activityReasoning {
		kinds &^= activityReasoning
	}
	label := first.reasoningLabel()
	if kinds != activityReasoning {
		var title strings.Builder
		renderActivityTitle(&title, kinds)
		label = title.String()
	}
	if sweep && !first.groupExpanded {
		renderSweepText(b, label)
	} else {
		b.Text(label)
	}
	b.End()
	if running {
		renderActivityStatus(b, "inProgress")
	}
	if declined {
		renderActivityStatus(b, "declined")
	}
	b.Element("img", "class", "codex-activity-chevron", "src", "resources/icons/chevron-down.svg", "alt", "")
	b.End()
	bindings = append(bindings, peb.EditorBinding{Ref: first.groupRef, Kind: "click"})
	if first.groupExpanded {
		b.Open("div", "class", "codex-activity-list", "data-ref", first.groupRef+"/list", "data-pencode-scroll", "preserve")
		for _, i := range items {
			b.Embed(i.html)
			if i.detailRef != "" {
				bindings = append(bindings, peb.EditorBinding{Ref: i.detailRef, Kind: "click"})
			}
		}
		b.End()
	}
	b.End()
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

func renderActivityStatus(b *markup.Builder, status string) {
	label := status
	switch status {
	case "", "completed", "failed":
		return
	case "inProgress":
		label = "进行中"
	case "declined":
		label = "已拒绝"
	}
	class := "codex-activity-status"
	if status == "declined" {
		class += " codex-status"
	}
	b.TextElement("span", label, "class", class)
}

func (i *chatItem) renderActivityLabel(b *strings.Builder, fallback string) {
	switch i.data.Type {
	case "commandExecution":
		if len(i.data.CommandActions) == 0 {
			b.WriteString("已运行 ")
			b.WriteString(i.data.Command)
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
				b.WriteString(name)
			case "listFiles":
				b.WriteString("已列出 ")
				if action.Path != "" {
					b.WriteString(action.Path)
					b.WriteString(" 中的")
				}
				b.WriteString("文件")
			case "search":
				b.WriteString("已在 ")
				if action.Path == "" {
					b.WriteString("项目")
				} else {
					b.WriteString(action.Path)
				}
				b.WriteString(" 中搜索")
				if action.Query != "" {
					b.WriteString("“")
					b.WriteString(action.Query)
					b.WriteString("”")
				}
			default:
				b.WriteString("已运行 ")
				b.WriteString(action.Command)
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
			b.WriteString(filepath.Base(change.Path))
			added, removed := change.lineCounts()
			b.WriteString(" +")
			writeActivityCount(b, added)
			b.WriteString(" -")
			writeActivityCount(b, removed)
		}
	case "reasoning":
		b.WriteString(i.reasoningLabel())
	default:
		b.WriteString(fallback)
	}
}

func (i *chatItem) reasoningLabel() string {
	line, _, _ := strings.Cut(strings.TrimSpace(i.text.String()), "\n")
	if line = strings.Trim(line, "*# \t\r"); line != "" {
		return line
	}
	return "思考"
}

func writeActivityCount(b *strings.Builder, count int) {
	var buffer [20]byte
	b.Write(strconv.AppendInt(buffer[:0], int64(count), 10))
}
