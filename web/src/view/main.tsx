import { render } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { App, applyDocumentTheme, applyHostStyleVariables, type McpUiHostContext } from "@modelcontextprotocol/ext-apps";
import type { Action, ActionResult, Result, Status } from "./types";
import { ActionPage, ActorPage, ActorsPage, Confirm, MetricPage, StatusPage, isWrite, type Nav } from "./pages";
import "./styles.css";

declare const __VERSION__: string;

interface Entry { tool: string; args: Record<string, unknown>; data: Result; stale?: boolean }

const app = new App(
  { name: "substrate-console-view", version: __VERSION__ },
  { availableDisplayModes: ["inline", "fullscreen"] },
  { autoResize: true },
);

function applyContext(ctx?: McpUiHostContext) {
  if (!ctx) return;
  if (ctx.theme) {
    applyDocumentTheme(ctx.theme);
    document.documentElement.dataset.theme = ctx.theme;
  }
  if (ctx.styles?.variables) applyHostStyleVariables(ctx.styles.variables);
  const inset = ctx.safeAreaInsets;
  if (inset) {
    const r = document.documentElement.style;
    r.setProperty("--safe-t", `${inset.top}px`); r.setProperty("--safe-r", `${inset.right}px`);
    r.setProperty("--safe-b", `${inset.bottom}px`); r.setProperty("--safe-l", `${inset.left}px`);
  }
}

// ChatGPT's deep links arrive as hostContext["openai/deepLink"].url, an
// app-relative path. Routes: /, /actors, /actors/{atespace}/{name},
// /atespaces/{atespace}, /latency.
function route(url: string): { tool: string; args: Record<string, unknown> } | undefined {
  const u = new URL(url, "https://app.invalid");
  const p = u.pathname.split("/").filter(Boolean).map(decodeURIComponent);
  const scope = u.searchParams.get("atespace") ?? undefined;
  const sc = scope ? { atespace: scope } : {};
  if (p.length === 0) return { tool: "status", args: sc };
  if (p[0] === "atespaces" && p[1]) return { tool: "status", args: { atespace: p[1] } };
  if (p[0] === "actors" && p.length >= 3) return { tool: "get_actor", args: { atespace: p[1], name: p[2] } };
  if (p[0] === "actors") return { tool: "list_actors", args: { ...sc, state: u.searchParams.get("state") ?? undefined } };
  if (p[0] === "latency") return { tool: "query_metric", args: { metric: "resume_p95", by: "template", window: "6h", ...sc } };
  return undefined;
}

// Cache the last overview so the sidebar entrypoint paints at once and
// refreshes in the background (stale-while-revalidate).
const CACHE = "substrate-console:status:v1";
function cached(): Status | undefined {
  try { const s = localStorage.getItem(CACHE); return s ? (JSON.parse(s) as Status) : undefined; } catch { return undefined; }
}
function remember(s: Status) {
  try { if (!s.scope) localStorage.setItem(CACHE, JSON.stringify(s)); } catch { /* storage may be blocked */ }
}

/** What the user is looking at: a line for the model, plus structured state. */
function context(e: Entry): { text: string; data: Record<string, unknown> } {
  const d = e.data;
  switch (d.kind) {
    case "status": return { text: `Viewing Substrate overview for ${d.scope || "the whole cluster"}: ${d.headline}.`, data: { page: "overview", atespace: d.scope || null, issues: d.items.filter((i) => i.severity !== "resolved").map((i) => i.title) } };
    case "actors": return { text: `Viewing ${d.total} actors${d.scope ? " in " + d.scope : ""}.`, data: { page: "actors", atespace: d.scope || null, filter: d.filter } };
    case "actor": return { text: `Viewing actor ${d.actor.atespace}/${d.actor.name}, ${d.actor.state}.`, data: { page: "actor", atespace: d.actor.atespace, name: d.actor.name, state: d.actor.state } };
    case "metric": return { text: `Viewing chart: ${d.title}, last ${d.window}.`, data: { page: "chart", metric: d.metric, by: d.by ?? null, window: d.window } };
    case "action": return { text: `Viewing the result of ${d.verb}: ${d.summary}`, data: { page: "action", verb: d.verb, dryRun: d.dryRun } };
  }
}

const Icon = {
  stack: <svg width="20" height="20" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.33" stroke-linejoin="round"><path d="M10 3 17 6.5 10 10 3 6.5Z" /><path d="M3 10 10 13.5 17 10" /><path d="M3 13.5 10 17 17 13.5" /></svg>,
  overview: <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3"><circle cx="8" cy="8" r="5.5" /><path d="M8 5v3l2 1.5" stroke-linecap="round" /></svg>,
  actors: <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3"><rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1" /><rect x="9" y="2.5" width="4.5" height="4.5" rx="1" /><rect x="2.5" y="9" width="4.5" height="4.5" rx="1" /><rect x="9" y="9" width="4.5" height="4.5" rx="1" /></svg>,
  latency: <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"><path d="M2 12.5 6 8l3 2.5 5-6" /></svg>,
  space: <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3"><path d="M2.5 5.5h11v7h-11z" /><path d="M2.5 5.5 4 3h3l1 2.5" stroke-linejoin="round" /></svg>,
};

function Root() {
  const [ctx, setCtx] = useState<McpUiHostContext | undefined>();
  const [stack, setStack] = useState<Entry[]>(() => {
    const c = cached();
    return c ? [{ tool: "status", args: {}, data: c, stale: true }] : [];
  });
  const [status, setStatus] = useState<Status | undefined>(cached);
  const [confirm, setConfirm] = useState<{ r: ActionResult; busy: boolean } | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [toast, setToast] = useState<string | undefined>();
  const [connected, setConnected] = useState(false);
  const input = useRef<Record<string, unknown>>({});
  const deepLink = useRef<string | undefined>();

  const full = ctx?.displayMode === "fullscreen";
  const canExpand = !full && (ctx?.availableDisplayModes ?? []).includes("fullscreen");
  const top = stack[stack.length - 1];

  function flash(msg: string) {
    setToast(msg);
    setTimeout(() => setToast(undefined), 3500);
  }

  function followDeepLink(c?: McpUiHostContext) {
    const url = (c as Record<string, unknown> | undefined)?.["openai/deepLink"] as { url?: string } | undefined;
    if (!url?.url || url.url === deepLink.current) return;
    deepLink.current = url.url;
    const r = route(url.url);
    if (r) go(r.tool, r.args, true);
  }

  useEffect(() => {
    // Handlers go on before connect so the first tool result isn't missed.
    app.ontoolinput = (p) => { input.current = (p.arguments ?? {}) as Record<string, unknown>; };
    app.ontoolresult = (p) => {
      const data = p.structuredContent as Result | undefined;
      const tool = app.getHostContext()?.toolInfo?.tool.name ?? data?.kind ?? "status";
      if (!data || !("kind" in data)) {
        setError(p.content?.map((c) => ("text" in c ? c.text : "")).join("\n") || "The tool returned no data.");
        return;
      }
      setStack([{ tool, args: input.current, data }]);
      if (data.kind === "status") { setStatus(data); remember(data); }
    };
    app.onhostcontextchanged = (p) => {
      setCtx((prev) => { const next = { ...prev, ...p }; applyContext(next); return next; });
      followDeepLink(p as McpUiHostContext);
    };
    app.onteardown = async () => ({});
    app.connect().then(() => {
      const c = app.getHostContext();
      applyContext(c);
      setCtx(c);
      setConnected(true);
      followDeepLink(c);
    }).catch((e) => setError(`Could not connect to the host: ${e}`));
  }, []);

  useEffect(() => { document.body.classList.toggle("inline", !full); }, [full]);

  // Keep the model told what the user is looking at. The text is background
  // context (audience: assistant), so ChatGPT doesn't show it as an
  // attachment; structuredContent carries the same state for tools.
  useEffect(() => {
    if (!top || top.stale || !connected) return;
    const c = context(top);
    app.updateModelContext({
      content: [{ type: "text", text: c.text, annotations: { audience: ["assistant"] } }],
      structuredContent: c.data,
    }).catch(() => {});
  }, [top, connected]);

  async function call(tool: string, args: Record<string, unknown>) {
    const res = await app.callServerTool({ name: tool, arguments: args });
    const text = res.content?.map((c) => ("text" in c ? c.text : "")).join("\n");
    if (res.isError) throw new Error(text || `${tool} failed`);
    return res.structuredContent as Result;
  }

  async function go(tool: string, args: Record<string, unknown>, replace = false) {
    setError(undefined);
    try {
      const data = await call(tool, args);
      if (data.kind === "status" && !args.atespace) { setStatus(data); remember(data); }
      setStack((s) => (replace ? [{ tool, args, data }] : [...s, { tool, args, data }]));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  }

  async function refresh() {
    if (!top) return;
    try {
      const data = await call(top.tool, top.args);
      setStack((s) => [...s.slice(0, -1), { tool: top.tool, args: top.args, data }]);
      if (data.kind === "status" && !top.args.atespace) { setStatus(data); remember(data); }
    } catch { /* keep the last good view */ }
  }

  // Poll in full screen only; an inline card in a chat stays still.
  useEffect(() => {
    if (!full || !top || top.data.kind === "action") return;
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, [full, top]);

  // Revalidate a cached overview, and load the sidebar's cluster status.
  useEffect(() => {
    if (!connected) return;
    if (top?.stale) refresh();
    if (full && !status) call("status", {}).then((d) => d.kind === "status" && setStatus(d)).catch(() => {});
  }, [full, connected]);

  async function act(a: Action) {
    if (!isWrite(a.tool)) return go(a.tool, a.args);
    setError(undefined);
    try {
      const r = (await call(a.tool, { ...a.args, dry_run: true })) as ActionResult;
      setConfirm({ r, busy: false });
    } catch (e) { setError(String((e as Error).message ?? e)); }
  }

  async function apply(r: ActionResult) {
    setConfirm({ r, busy: true });
    try {
      const done = (await call(r.tool, { ...r.args, dry_run: false })) as ActionResult;
      setConfirm(undefined);
      flash(done.summary);
      if (top?.data.kind === "action") setStack((s) => [...s.slice(0, -1), { ...top, data: done }]);
      else await refresh();
    } catch (e) {
      setConfirm(undefined);
      setError(String((e as Error).message ?? e));
    }
  }

  // Questions go to the host's composer. Where ChatGPT supports it, they land
  // as an editable draft (openai/message send: false) instead of sending.
  async function ask(prompt: string) {
    const draft = !!app.getHostCapabilities()?.experimental?.["openai/message"];
    try {
      const params = { role: "user" as const, content: [{ type: "text" as const, text: prompt }], ...(draft ? { _meta: { "openai/message": { target: "active", send: false } } } : {}) };
      const r = await app.sendMessage(params);
      if (r && "isError" in r && r.isError) throw new Error("rejected");
    } catch {
      try { await navigator.clipboard.writeText(prompt); flash("Question copied. Paste it into your assistant."); }
      catch { flash(prompt); }
    }
  }

  const expand = canExpand ? () => { app.requestDisplayMode({ mode: "fullscreen" }).catch(() => {}); } : undefined;
  const nav: Nav = useMemo(() => ({ act, ask, inline: !full, expand }), [full, top, stack, canExpand]);
  const back = stack.length > 1 ? () => setStack((s) => s.slice(0, -1)) : undefined;

  const page = !top ? (
    <div class="loading">{error ? "" : "Loading…"}</div>
  ) : top.data.kind === "status" ? <StatusPage st={top.data} nav={nav} />
    : top.data.kind === "actors" ? <ActorsPage list={top.data} nav={nav} />
    : top.data.kind === "actor" ? <ActorPage d={top.data} nav={nav} />
    : top.data.kind === "metric" ? <MetricPage m={top.data} />
    : <ActionPage r={top.data} nav={nav} onConfirm={top.data.dryRun ? () => apply(top.data as ActionResult) : undefined} />;

  const overlay = (
    <>
      {error && <div class="content" style={{ paddingBottom: 0 }}><div class="err" role="alert">{error}</div></div>}
      {confirm && (
        <div class="scrim" onClick={(e) => e.target === e.currentTarget && !confirm.busy && setConfirm(undefined)}>
          <Confirm r={confirm.r} busy={confirm.busy} onConfirm={() => apply(confirm.r)} onCancel={() => setConfirm(undefined)} />
        </div>
      )}
      {toast && <div class="toast" role="status">{toast}</div>}
    </>
  );

  if (!full) {
    return (
      <div class={top?.stale ? "stale" : ""}>
        {back && <div class="content" style={{ paddingBottom: 0 }}><button type="button" class="back cursor-interaction" onClick={back}>← Back</button></div>}
        {overlay}
        {page}
      </div>
    );
  }

  const scope = (top?.args.atespace as string) || "";
  const at = (tool: string) => top?.tool === tool && stack.length === 1;
  const withScope = (a: Record<string, unknown>) => (scope ? { ...a, atespace: scope } : a);
  return (
    <div class="app">
      <aside class="side">
        <div class="cluster">{Icon.stack}
          <div style={{ minWidth: 0 }}><b>{status?.cluster ?? "Substrate"}</b><span>{status ? `Substrate ${status.version ?? ""}` : ""}</span></div>
        </div>
        <nav class="nav">
          <button type="button" class={`ni cursor-interaction${at("status") || at("open_console") || at("console_tab") ? " on" : ""}`} onClick={() => go("status", withScope({}), true)}>
            {Icon.overview}Overview{status && openCount(status, scope) > 0 && <span class="ct c">{openCount(status, scope)}</span>}
          </button>
          <button type="button" class={`ni cursor-interaction${at("list_actors") ? " on" : ""}`} onClick={() => go("list_actors", withScope({}), true)}>
            {Icon.actors}Actors{status && <span class="ct">{actorCount(status, scope).toLocaleString()}</span>}
          </button>
          <button type="button" class={`ni cursor-interaction${at("query_metric") ? " on" : ""}`} onClick={() => go("query_metric", withScope({ metric: "resume_p95", by: "template", window: "6h" }), true)}>
            {Icon.latency}Resume latency
          </button>
        </nav>
        {status && (
          <nav class="nav">
            <h6>Atespaces</h6>
            <button type="button" class={`ni cursor-interaction${!scope ? " on" : ""}`} onClick={() => go(top?.tool === "list_actors" ? "list_actors" : "status", {}, true)}>
              {Icon.space}All
            </button>
            {status.atespaces.map((a) => (
              <button type="button" key={a.name} class={`ni cursor-interaction${scope === a.name ? " on" : ""}`} onClick={() => go(top?.tool === "list_actors" ? "list_actors" : "status", { atespace: a.name }, true)}>
                {Icon.space}{a.name}{a.problems > 0 && <span class="ct c">{a.problems}</span>}
              </button>
            ))}
          </nav>
        )}
        <div class="foot"><span class={`dot${error ? " err" : ""}`} />{error ? "Disconnected" : "Live"}</div>
      </aside>
      <section class="main">
        <div class="sheet">
          <div class="top">
            {back && <button type="button" class="btn quiet sm cursor-interaction" onClick={back} aria-label="Back">←</button>}
            <div class="crumb">{scope && <>{scope}<i>/</i></>}<b>{crumb(top)}</b></div>
          </div>
          <div class={top?.stale ? "stale" : ""}>
            {overlay}
            {page}
          </div>
        </div>
      </section>
    </div>
  );
}

function crumb(e?: Entry): string {
  if (!e) return "Substrate";
  switch (e.data.kind) {
    case "status": return "Overview";
    case "actors": return "Actors";
    case "actor": return e.data.actor.name;
    case "metric": return e.data.title;
    case "action": return e.data.dryRun ? "Confirm" : "Done";
  }
}

function openCount(s: Status, scope: string) {
  if (!scope) return s.items.filter((i) => i.severity !== "resolved").length;
  return s.atespaces.find((a) => a.name === scope)?.problems ?? 0;
}

function actorCount(s: Status, scope: string) {
  if (!scope) return Object.values(s.counts).reduce((a, b) => a + b, 0);
  return s.atespaces.find((a) => a.name === scope)?.actors ?? 0;
}

render(<Root />, document.getElementById("root")!);
