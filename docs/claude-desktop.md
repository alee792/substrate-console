# Running the console in Claude Desktop

There are two ways to add the console to Claude Desktop. They differ in where Claude connects from, and today only one of them shows the console's views.

| | Local (stdio) | Custom connector (remote) |
|---|---|---|
| Who starts the console | Claude Desktop, on your machine | You, with `serve` |
| Who connects to it | Claude Desktop, locally | Anthropic's cloud, over the internet |
| Cluster access | Your kubeconfig | Your kubeconfig, on the machine running `serve` |
| Tools and text answers | Yes | Yes |
| Interactive views (cards, full console) | Not on current builds, see below | Yes |
| Also works in claude.ai and mobile | No | Yes |

Start with the local setup. It needs nothing exposed to the internet and gives you every tool. Use a custom connector when you want the views, and only with the safeguards below.

## Local (stdio)

1. Build or download the binary and note its absolute path, for example `/usr/local/bin/substrate-console`.
2. In Claude Desktop, open **Settings → Developer → Edit Config**. This opens `claude_desktop_config.json` (`~/Library/Application Support/Claude/` on macOS, `%APPDATA%\Claude\` on Windows).
3. Add the console:

   ```json
   {
     "mcpServers": {
       "substrate": {
         "command": "/usr/local/bin/substrate-console",
         "args": ["mcp", "--context", "kind-dev", "--service-account", "ate-users/alice"],
         "env": {
           "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
         }
       }
     }
   }
   ```

   To try it without a cluster, use `"args": ["mcp", "--demo"]`.

4. Quit Claude Desktop completely and open it again. **Settings → Connectors** (or the tools menu in a chat) should list `substrate` with its tools.
5. Ask "What needs attention on the cluster?". Claude calls `status` and answers from its text.

Things that trip this up:

- **Use absolute paths.** Claude Desktop does not start servers from your shell, so `command` must be a full path.
- **Set `PATH` when your kubeconfig runs a credential plugin.** Claude Desktop on macOS starts servers with a minimal `PATH`, so plugins such as `gke-gcloud-auth-plugin`, `aws` or `kubelogin` aren't found and the console can't reach the cluster. Put the directories that hold them in `env.PATH`. A kind cluster's kubeconfig needs no plugin.
- **Pick who the console acts as.** Without `--service-account` (or `--token-file` / `--token-command`) the console acts as the shared `ate-client` owner. See [Who the console acts as](../README.md#who-the-console-acts-as).
- **Logs.** Claude Desktop writes each server's stderr to `~/Library/Logs/Claude/mcp-server-substrate.log` (macOS) or `%APPDATA%\Claude\logs\` (Windows). The console logs only to stderr.
- **Two processes.** Claude Desktop may start the console twice, one per internal connection. Each is independent; that's harmless.

### Why views don't show locally yet

The console declares its views the way the MCP Apps spec asks: the `io.modelcontextprotocol/ui` extension in its capabilities, `_meta.ui.resourceUri` on each tool, and `text/html;profile=mcp-app` resources with a CSP. Claude Desktop currently doesn't render views from local servers in a normal chat. These open reports describe it, and they apply to every server, whatever its language:

- [anthropics/claude-code#108122](https://github.com/anthropics/claude-code/issues/108122): Desktop 4.23118.0 on macOS never reads the `ui://` resource for stdio servers. It worked on 4.16336.0.
- [anthropics/claude-ai-mcp#1069](https://github.com/anthropics/claude-ai-mcp/issues/1069): Desktop 2.x on Windows drops `_meta.ui` from local tools; the same server renders as a custom connector.
- [modelcontextprotocol/ext-apps#825](https://github.com/modelcontextprotocol/ext-apps/issues/825): chats with a folder attached never show views.

Nothing is needed on the console's side. When a Desktop update fixes this, the cards appear without a config change.

## Custom connector (remote)

Claude connects to custom connectors from Anthropic's cloud, not from your machine, on every client including Claude Desktop. So the console has to be reachable over public HTTPS. That is also what makes it work in claude.ai and on your phone.

This puts a door to your cluster on the internet. Anyone with the URL and token acts as whoever the console acts as. Before you do it:

- Run it as your own ServiceAccount (`--service-account`), never the shared `ate-client` owner.
- Prefer `--read-only` until you have a reason to write from a chat.
- Use a tunnel you control, and stop it when you're done.

Steps:

1. Start a tunnel to `127.0.0.1:7878` and note its HTTPS origin, for example `https://console.example.com`. Any tunnel works (Cloudflare Tunnel, ngrok, Tailscale Funnel).
2. Start the console with that origin:

   ```sh
   substrate-console serve --context kind-dev --service-account ate-users/alice --read-only \
     --public-url https://console.example.com
   ```

   `--public-url` turns on auth, makes `/mcp` accept requests addressed to that host (a tunnel normally forwards your public host name, which the console would otherwise refuse as a DNS rebinding attempt), and prints the connector URL with a fresh token: `https://console.example.com/mcp?key=…`. To keep the same token across restarts, set `SUBSTRATE_CONSOLE_TOKEN` or pass `--auth-token-file`.
3. In Claude, open **Settings → Connectors → Add custom connector**. Paste the connector URL and choose **No sign-in**. If your organization has request headers, you can instead use the plain `/mcp` URL and send `Authorization: Bearer <token>`, which keeps the token out of proxy logs.
4. Enable the connector in a chat and ask "What needs attention on the cluster?". The answer should come with the console's card.

If you change the console's tools, remove and re-add the connector, because Claude caches the tool list per connector.

Sign-in with your own identity provider (OAuth) isn't supported yet. That's the remote-connectors follow-up in the README.

## Checklist to confirm on a real Claude Desktop

These need a Mac or Windows machine with Claude Desktop. Everything above was checked in a sandbox against the spec, the console's own MCP Apps host and simulated tunnel requests, but not in Claude Desktop itself.

- [ ] Local, `--demo`: `substrate` appears under Connectors with its tools, and "What needs attention?" gets an answer from `status`.
- [ ] Local, real cluster: the console connects with your kubeconfig. If it uses a credential plugin, it connects only once `env.PATH` is set.
- [ ] Local: note whether the card renders, and the Desktop version. A card here means the Desktop bug above is fixed.
- [ ] Custom connector, `--demo` behind a tunnel: the connector adds with **No sign-in**, the card renders inline, and "Open console" goes full screen.
- [ ] Custom connector: a card's "Ask" button puts a question in the chat.
- [ ] Custom connector without `?key=`: Claude reports the connector can't be reached (the console answers 401).
