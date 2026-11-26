module github.com/pencode-labs/codex

go 1.27.0

require (
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/coder/websocket v1.8.15
	github.com/onix-engine/onix v0.1.10
	github.com/pencode-labs/pencode v0.1.0-rc.15
	github.com/yuin/goldmark/v2 v2.1.16
	golang.org/x/sys v0.50.0
)

require (
	github.com/dlclark/regexp2/v2 v2.13.2 // indirect
	golang.org/x/net v0.63.0 // indirect
)

exclude github.com/onix-engine/onix v0.0.0-00010101000000-000000000000

replace github.com/onix-engine/onix => ../../../onix
