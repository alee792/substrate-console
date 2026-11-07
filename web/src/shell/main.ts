// The local web shell: a minimal MCP Apps host. It connects to the console's
// own MCP endpoint, loads the ui:// view into a sandboxed iframe, and bridges
// the two with AppBridge. The view cannot tell this page from Claude or
// ChatGPT, which is the point: one view, one set of tools, every host.
import { Client, StreamableHTTPClientTransport } from "@modelcontextprotocol/client";
import { AppBridge, PostMessageTransport } from "@modelcontextprotocol/ext-apps/app-bridge";

declare const __VERSION__: string;

const VIEW_URI = "ui://substrate-console/console.html";
const dark = window.matchMedia("(prefers-color-scheme: dark)");
const theme = () => (dark.matches ? "dark" : "light") as "dark" | "light";

function el<K extends keyof HTMLElementTagNameMap>(tag: K, props: Partial<HTMLElementTagNameMap[K]> = {}, ...kids: (Node | string)[]) {
  const e = Object.assign(document.createElement(tag), props);
  e.append(...kids);
  return e;
}

/** Shows a question the view wanted to ask, since this page has no model. */
function showPrompt(text: string) {
  document.querySelector(".ask-panel")?.remove();
  const copy = el("button", { type: "button", className: "pri", textContent: "Copy" });
  const close = el("button", { type: "button", textContent: "Close" });
  const panel = el("div", { className: "ask-panel", role: "dialog" },
    el("p", { textContent: "This page has no assistant. Paste the question into Claude, ChatGPT or Codex with substrate-console connected." }),
    el("pre", { textContent: text }),
    el("div", { className: "row" }, copy, close));
  copy.onclick = async () => {
    try { await navigator.clipboard.writeText(text); copy.textContent = "Copied"; }
    catch { getSelection()?.selectAllChildren(panel.querySelector("pre")!); }
  };
  close.onclick = () => panel.remove();
  document.body.append(panel);
}

/** The CSP the spec asks hosts to enforce, built from the resource's _meta.ui.csp. */
function cspFor(meta: Record<string, unknown> | undefined): string {
  const csp = ((meta?.ui as Record<string, unknown> | undefined)?.csp ?? {}) as { connectDomains?: string[]; resourceDomains?: string[] };
  const res = (csp.resourceDomains ?? []).join(" ");
  const conn = (csp.connectDomains ?? []).join(" ") || "'none'";
  return [
    "default-src 'none'",
    `script-src 'unsafe-inline' ${res}`,
    `style-src 'unsafe-inline' ${res}`,
    `font-src ${res || "'none'"}`,
    `img-src data: ${res}`,
    `connect-src ${conn}`,
    "object-src 'none'",
  ].join("; ");
}

async function main() {
  const status = document.getElementById("status")!;
  const client = new Client({ name: "substrate-console-shell", version: __VERSION__ });
  await client.connect(new StreamableHTTPClientTransport(new URL("/mcp", location.href)));

  const res = await client.readResource({ uri: VIEW_URI });
  const view = res.contents[0] as { text?: string; _meta?: Record<string, unknown> };
  if (!view?.text) throw new Error("The server returned no view.");
  const html = view.text.replace(/<head>/i, `<head><meta http-equiv="Content-Security-Policy" content="${cspFor(view._meta)}">`);

  // ?mode=inline previews the view as a chat host shows it; ?tool= and
  // ?args= pick the tool call to render. Both are for development.
  const params = new URLSearchParams(location.search);
  const inline = params.get("mode") === "inline";
  const tool = params.get("tool") ?? "status";
  const args = JSON.parse(params.get("args") ?? "{}") as Record<string, unknown>;
  if (inline) document.body.classList.add("inline-preview");

  const iframe = el("iframe", { title: "Substrate console" });
  iframe.setAttribute("sandbox", "allow-scripts allow-popups");
  document.body.append(iframe);

  const bridge = new AppBridge(
    client,
    { name: "substrate-console-shell", version: __VERSION__ },
    { openLinks: {}, serverTools: {}, serverResources: {}, logging: {}, updateModelContext: { text: {} }, message: { text: {} } },
    {
      hostContext: {
        theme: theme(), displayMode: inline ? "inline" : "fullscreen",
        availableDisplayModes: inline ? ["inline", "fullscreen"] : ["fullscreen"], platform: "web",
        locale: navigator.language, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        containerDimensions: { width: innerWidth, height: innerHeight },
      },
    },
  );
  bridge.onmessage = async (p) => {
    const text = p.content.map((c) => ("text" in c ? c.text : "")).join("\n");
    showPrompt(text);
    return {};
  };
  bridge.onopenlink = async (p) => { window.open(p.url, "_blank", "noopener"); return {}; };
  bridge.onupdatemodelcontext = async () => ({});
  bridge.onrequestdisplaymode = async (p) => {
    const mode = inline ? p.mode : "fullscreen";
    document.body.classList.toggle("inline-preview", mode === "inline");
    iframe.style.height = "";
    await bridge.sendHostContextChange({ displayMode: mode });
    return { mode };
  };
  bridge.onsizechange = (p) => {
    if (document.body.classList.contains("inline-preview") && p.height) iframe.style.height = `${p.height}px`;
  };
  bridge.onloggingmessage = (p) => console.debug("[view]", p.data);
  bridge.oninitialized = async () => {
    status.remove();
    await bridge.sendToolInput({ arguments: args });
    const result = await client.callTool({ name: tool, arguments: args });
    await bridge.sendToolResult(result as Parameters<typeof bridge.sendToolResult>[0]);
  };

  dark.addEventListener("change", () => bridge.sendHostContextChange({ theme: theme() }));
  addEventListener("resize", () => bridge.sendHostContextChange({ containerDimensions: { width: innerWidth, height: innerHeight } }));

  // Connect before the view loads so its ui/initialize is not missed. The
  // iframe's WindowProxy stays the same object across the srcdoc navigation.
  await bridge.connect(new PostMessageTransport(iframe.contentWindow!, iframe.contentWindow!));
  iframe.srcdoc = html;
}

main().catch((e) => {
  const s = document.getElementById("status");
  if (s) s.textContent = `Could not start the console: ${e instanceof Error ? e.message : e}`;
  console.error(e);
});
