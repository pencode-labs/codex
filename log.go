package main

import (
	"log"
	"net/url"
)

// stderr is streamed to the host's Output channel; stdout carries the SDK protocol.
var codexLog = log.New(stderrWriter, "[Codex] ", log.LstdFlags|log.Lmicroseconds)

func logEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "<invalid URL>"
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
	return u.String()
}
