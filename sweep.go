package main

import (
	"html"
	"strings"
	"unicode/utf8"
)

func renderSweepText(b *strings.Builder, text string) {
	b.WriteString(`<span class="codex-sweep-text">`)
	// Bound retained animation tracks, regardless of the label's length.
	width := (utf8.RuneCountInString(text) + 15) / 16
	for band := 0; text != ""; band++ {
		end := 0
		for range width {
			if end == len(text) {
				break
			}
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}
		b.WriteString(`<span style="animation-delay:`)
		writeActivityCount(b, band*65)
		b.WriteString(`ms">`)
		b.WriteString(html.EscapeString(text[:end]))
		b.WriteString(`</span>`)
		text = text[end:]
	}
	b.WriteString(`</span>`)
}
