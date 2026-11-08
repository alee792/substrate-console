// Package web embeds the built view and shell. Run `npm run build` in this
// directory after changing anything under src/; dist/ is committed so that
// `go install` works without Node.
package web

import _ "embed"

// ConsoleHTML is the MCP App view served at ui://substrate-console/console.html.
//
//go:embed dist/console.html
var ConsoleHTML []byte

// ShellHTML is the local web shell served at / by `substrate-console serve`.
//
//go:embed dist/shell.html
var ShellHTML []byte
