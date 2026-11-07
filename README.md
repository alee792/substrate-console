# substrate-console

A console for [Substrate](https://github.com/agent-substrate/substrate) clusters, built as an **MCP App**. The same binary is:

- an MCP server whose tools wrap the Substrate control API (`ateapi`), Kubernetes and Prometheus, and
- the interactive view those tools render in any host that supports MCP Apps (Claude, ChatGPT, VS Code), plus a small local web page for using it without an assistant.

It is an unofficial, personal project and is not affiliated with the Substrate maintainers.

![Overview, dark](docs/images/overview-dark.png)

## What it shows

The console is built around progressive disclosure:

1. **Needs you.** Crashes grouped by shared cause (worker, node, template), actors stuck in a transition, unavailable workers, pools below their desired size, and capacity at 95% or more. Each item says what happened, how many actors it affects, and what to do.
2. **Vitals.** Capacity, resume p95, density and crashes, each compared against the same time last week.
3. **Objects.** Actors, templates and worker pools. The actor page is the center: state, crash reason, a plain-language hint (for example "4 GiB heap inside a 2 GiB sandbox"), timeline, recent logs and snapshots.
4. **Ask.** Instead of general dashboards, the assistant answers the question and draws the chart with `query_metric`.

Every view can be scoped to the whole cluster or a single atespace.

## Quick start

```sh
make build                              # builds the view, then the Go binary
bin/substrate-console serve --demo      # http://127.0.0.1:7878, fixture cluster
```

`--demo` serves a fixture cluster with a few real problems in it (three OOM crashes on one worker, an actor stuck resuming, a nearly full pool), so you can try everything without a Substrate install. Writes change the fixture's state in memory.

Against a real cluster, the console uses your kubeconfig the same way `kubectl ate` does: it port-forwards to `ate-system/api`, trusts the cluster's ClusterTrustBundle, and mints a token for the `ate-client` ServiceAccount.

```sh
bin/substrate-console serve --context kind-dev --prometheus-url http://localhost:9090
```

| Flag | Meaning |
|---|---|
| `--demo` | Use the fixture cluster. |
| `--read-only` | Register only the read tools. |
| `--kubeconfig`, `--context` | Which cluster. |
| `--endpoint` | Dial ateapi directly instead of port-forwarding. |
| `--token-file` | Bearer token file (`-` for stdin) instead of minting one. |
| `--prometheus-url` | Enables vitals history, baselines and `query_metric`. |
| `--addr` | Listen address for `serve` (default `127.0.0.1:7878`). |
| `--no-web-fonts` | Don't load Geist from Google Fonts; use system fonts. |

## Connecting an assistant

**stdio** (Claude Desktop, Claude Code, VS Code, Codex):

```json
{
  "mcpServers": {
    "substrate": { "command": "substrate-console", "args": ["mcp", "--context", "kind-dev"] }
  }
}
```

**Streamable HTTP**: run `substrate-console serve` and point the host at `http://127.0.0.1:7878/mcp`. ChatGPT needs a public HTTPS URL, so put it behind a tunnel you control. `/mcp` has no authentication of its own yet: it only accepts loopback connections and rejects cross-origin browser requests.

Hosts that don't render MCP Apps (Claude Code's terminal UI today) still get every tool, because each tool also returns a readable text summary.

## MCP Apps and the OpenAI Apps SDK

The server implements the [MCP Apps extension](https://github.com/modelcontextprotocol/ext-apps) (`io.modelcontextprotocol/ui`, spec 2026-01-26) and the OpenAI Apps SDK metadata, so one view works in both families of hosts.

- **View resource.** `ui://substrate-console/console.html`, mimeType `text/html;profile=mcp-app`, a single self-contained HTML file. `_meta.ui.csp` (`resourceDomains` for fonts, no `connectDomains`) and `_meta.ui.prefersBorder` are set, mirrored as `openai/widgetCSP`, `openai/widgetPrefersBorder` and `openai/widgetDescription`.
- **Tools.** Tools with a view carry `_meta.ui.resourceUri` and `_meta.ui.visibility: ["model", "app"]`, plus the deprecated flat `ui/resourceUri` key for older hosts, and `openai/outputTemplate`, `openai/widgetAccessible` and `openai/toolInvocation/invoking|invoked`. Every tool sets `readOnlyHint`, `destructiveHint` and `openWorldHint`.
- **Results.** `structuredContent` (with a `kind` field) drives the view; `content` is a text summary the model can read.
- **View behavior.** The view uses `@modelcontextprotocol/ext-apps`: it follows the host's theme and style variables, calls tools through the host to navigate, requests fullscreen from an inline card, sends follow-up questions with `ui/message`, and keeps the model told what the user is looking at with `ui/update-model-context`. It polls only in fullscreen.
- **Writes.** Every write tool takes `dry_run`. The view always does a dry run first and shows what will change before applying it. `revert_actors` and `delete_actors` are marked destructive.

The local web page (`serve`, then open `/`) is itself a minimal MCP Apps host built on `AppBridge`: it reads the view resource over `/mcp`, applies the resource's CSP, and loads it into a sandboxed iframe. `?mode=inline&tool=get_actor&args={...}` previews the inline card.

## Tools

| Tool | Kind | What it does |
|---|---|---|
| `status` | read | What needs attention, vitals, pools, atespaces. |
| `list_actors` | read | Filter by atespace, state (including `stuck`), template, worker, idle time. |
| `get_actor` | read | One actor with timeline, recent logs, a hint and suggested actions. |
| `actor_logs` | read | Tail logs of a running actor. |
| `list_templates` | read | Actor templates and their limits. |
| `list_worker_pools` | read | Pools, readiness and busy workers. |
| `query_metric` | read | Resume p95, resume failure rate, crashes; by template, pool or atespace. |
| `suspend_actors`, `resume_actors` | write | |
| `revert_actors`, `delete_actors` | destructive | |
| `drain_worker` | write | |

## Layout

```
cmd/substrate-console   CLI: mcp (stdio), serve (HTTP + web page), version
internal/mcpapp         MCP server, tools, MCP Apps / OpenAI metadata
internal/rules          turns a cluster snapshot into "what needs you"
internal/backend        Backend interface; ate (live) and demo (fixture)
internal/metrics        Prometheus querier
internal/shell          HTTP handler for /mcp and the web page
web/                    Preact view and shell; dist/ is committed and embedded
```

Requires Go 1.27 and Node 22. `make test` runs Go vet and tests plus the TypeScript check. CI also checks that `web/dist` matches a fresh build.

## Known gaps

The live backend is written against Substrate v1.0.0's API but has not yet been run against a cluster. Things to verify on a real one:

- Connection: port-forward, ClusterTrustBundle lookup and token minting, copied from `kubectl-ate`.
- `StateSince` comes from the actor's update or crash time; stuck-transition thresholds depend on that being accurate.
- After a crash, ateapi clears the worker assignment, so crash grouping falls back to the assigned node.
- WorkerPool `spec.replicas` and `status.readyReplicas` for the "below desired size" rule.
- Prometheus metric names (dotted UTF-8 or underscored; the querier probes for both). kind's bundled Prometheus doesn't scrape worker-pool metrics, so capacity and running-actor history are unavailable there.

Also not done yet: a full actor timeline (needs state-change events from Substrate), logs for actors that aren't running, sidebar navigation on narrow screens, authentication on `/mcp`, and a smaller view bundle (about 690 KiB, mostly the MCP SDK and zod).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
