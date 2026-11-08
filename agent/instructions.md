You work on substrate-console, a console for Substrate clusters. The
repository is checked out at `substrate-console/` in your workspace.

## What it is

One Go binary that is three things: an MCP server whose tools wrap
Substrate's control API (ateapi), Kubernetes and Prometheus; the MCP App
view those tools render inside Claude, ChatGPT and other hosts; and a small
local web page for using it without an assistant. The README is the
product description. Read it before changing behavior.

The people using it run Substrate clusters. The console leads with what
needs their attention (issues), then vitals, then templates, which are the
thing they actually author, then individual actors. Keep that order of
importance in anything you add.

## Layout

```
cmd/substrate-console   CLI: mcp (stdio), serve (HTTP + web page), version
internal/mcpapp         MCP server, tools, MCP Apps and OpenAI metadata
internal/rules          turns a cluster snapshot into "what needs you"
internal/backend        Backend interface; ate talks to a cluster (real or substrate-fake)
internal/metrics        Prometheus querier
internal/shell          HTTP handler for /mcp and the web page
web/                    Preact view and shell; web/dist is committed and embedded
```

## Working here

- `make build` builds the view and the binary. `bin/substrate-console serve --demo`
  runs against a fake cluster from substrate-fake on http://127.0.0.1:7878,
  with real problems seeded into it. Use the demo to see your change.
- `make test` runs go vet, the Go tests and the TypeScript check. CI also
  checks that `web/dist` matches a fresh build, so rebuild and commit
  `web/dist` whenever you touch `web/src`.
- Use the `check` skill before you say a change is done, and the `look`
  skill for anything visible.
- Prefer official SDKs and well-kept libraries to hand-written code, and
  generate code from API definitions where one exists.

## How it should feel

The interface is restrained: strong typography, few colors, nothing that
isn't needed. Copy reads like a careful product team wrote it. Short, plain
sentences that say what something is or what to do. No exclamation marks,
no "seamlessly", no "Let's", no emoji, no filler headings. A hint names the
cause in the user's terms ("Node heap is set to 4 GiB; the sandbox allows
2 GiB"), not a stack trace.

## Commits and pull requests

Work on a branch and open a draft pull request. Commit messages say what
changed and why, in the imperative. Do not add Co-Authored-By or other AI
trailers to commits; say in the pull request description that an AI agent
wrote the change. Never push to main.
