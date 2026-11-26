package main

import (
	"strconv"
	"unicode/utf8"

	"github.com/onix-engine/onix/html/markup"
)

func renderSweepText(b *markup.Builder, text string) {
	b.Open("span", "class", "codex-sweep-text")
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
		b.TextElement("span", text[:end], "style", "animation-delay:"+strconv.Itoa(band*65)+"ms")
		text = text[end:]
	}
	b.End()
}
