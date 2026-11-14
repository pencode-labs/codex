package main

import "unsafe"

// Completed background transcripts are reloadable from app-server. Keep a
// small recent cache, while live subscriptions, approvals and edits own their
// full working state independently of whether their tab is visible.
func (a *agent) trimConversations() {
	for {
		var oldest *conversation
		count, bytes := 0, 0
		for _, c := range a.conversations {
			if c == a.selected && a.workspaceOpen && !a.workspaceHidden || c.busy || c.observing || len(c.requests) != 0 || c.editing != nil {
				continue
			}
			c.releasePaintBefore(len(c.items))
			if len(c.items) == 0 {
				continue
			}
			count++
			bytes += c.transcriptBytes()
			if oldest == nil || c.viewed < oldest.viewed {
				oldest = c
			}
		}
		if count <= 4 && bytes <= 1<<20 {
			return
		}
		oldest.items, oldest.progress, oldest.files = nil, nil, nil
		oldest.cursor, oldest.loaded = "", false
	}
}

func (c *conversation) transcriptBytes() int {
	bytes := cap(c.items) * int(unsafe.Sizeof((*chatItem)(nil)))
	var files *turnFiles
	for _, item := range c.items {
		bytes += int(unsafe.Sizeof(chatItem{})) + item.text.Cap() + item.output.Cap() + len(item.data.Command)
		for _, change := range item.data.Changes {
			bytes += len(change.Diff)
		}
		for _, image := range item.images {
			bytes += len(image.url) + len(image.preview)
		}
		if item.files != nil && item.files != files {
			files = item.files
			for _, file := range files.files {
				for _, diff := range file.diffs {
					bytes += len(diff.text)
				}
			}
		}
	}
	return bytes
}

func (c *conversation) releasePaintBefore(end int) {
	var visible *turnFiles
	if end < len(c.items) {
		visible = c.items[end].files
	}
	for _, item := range c.items[:end] {
		item.html, item.linkBindings = "", nil
		if files := item.files; files != nil && files != visible {
			files.html, files.liveHTML = "", ""
			files.bindings = nil
		}
	}
}
