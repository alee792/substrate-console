import { render } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { App, applyDocumentTheme, applyHostStyleVariables, type McpUiHostContext } from "@modelcontextprotocol/ext-apps";
import type { Action, ActionResult, Result, Status } from "./types";
import { ActionPage, ActorPage, ActorsPage, Confirm, MetricPage, StatusPage, isWrite, type Nav } from "./pages";
import "./styles.css";

declare const __VERSION__: string;

interface Entry { tool: string; args: Record<string, unknown>; data: Result }

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
}

/** One line the model can read about what the user is looking at. */
function describe(e: Entry): string {
  const d = e.data;
  switch (d.kind) {
    case "status": return `The user is viewing substrate status for ${d.scope || "the whole cluster"}: ${d.headline}.`;
    case "actors": return `The user is viewing a list of ${d.total} actors${d.scope ? " in " + d.scope : ""}.`;
    case "actor": return `The user is viewing actor ${d.actor.atespace}/${d.actor.name}, which is ${d.actor.state}.`;
    case "metric": return `The user is viewing a chart: ${d.title}, last ${d.window}.`;
    case "action": return `The user is looking at the result of ${d.verb}: ${d.summary}`;
  }
}

function Root() {
  const [ctx, setCtx] = useState<McpUiHostContext | undefined>();
  const [stack, setStack] = useState<Entry[]>([]);
  const [status, setStatus] = useState<Status | undefined>();
  const [confirm, setConfirm] = useState<{ r: ActionResult; busy: boolean } | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [toast, setToast] = useState<string | undefined>();
  const [connected, setConnected] = useState(false);
  const input = useRef<Record<string, unknown>>({});

  const full = ctx?.displayMode === "fullscreen";
  const canExpand = !full && (ctx?.availableDisplayModes ?? []).includes("fullscreen");
  const top = stack[stack.length - 1];

  useEffect(() => {
    app.ontoolinput = (p) => { input.current = (p.arguments ?? {}) as Record<string, unknown>; };
    app.ontoolresult = (p) => {
      const data = p.structuredContent as Result | undefined;
      const tool = app.getHostContext()?.toolInfo?.tool.name ?? data?.kind ?? "status";
      if (!data || !("kind" in data)) {
        setError(p.content?.map((c) => ("text" in c ? c.text : "")).join("\n") || "The tool returned no data.");
        return;
      }
      setStack([{ tool, args: input.current, data }]);
      if (data.kind === "status") setStatus(data);
    };
    app.onhostcontextchanged = (p) => {
      setCtx((prev) => { const next = { ...prev, ...p }; applyContext(next); return next; });
    };
    app.onteardown = async () => ({});
    app.connect().then(() => {
      const c = app.getHostContext();
      applyContext(c);
      setCtx(c);
      setConnected(true);
    }).catch((e) => setError(`Could not connect to the host: ${e}`));
  }, []);

  useEffect(() => { document.body.classList.toggle("inline", !full); }, [full]);

  // Tell the model what the user is looking at.
  useEffect(() => {
    if (!top || !connected) return;
    app.updateModelContext({ content: [{ type: "text", text: describe(top) }] }).catch(() => {});
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
      if (data.kind === "status" && !args.atespace) setStatus(data);
      setStack((s) => (replace ? [{ tool, args, data }] : [...s, { tool, args, data }]));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  }

  async function refresh() {
    if (!top) return;
    try {
      const data = await call(top.tool, top.args);
      setStack((s) => [...s.slice(0, -1), { ...top, data }]);
      if (data.kind === "status" && !top.args.atespace) setStatus(data);
    } catch { /* keep the last good view; the footer shows the age */ }
  }

  // Poll in full-screen only; an inline card in a chat should stay quiet.
  useEffect(() => {
    if (!full || !top || top.data.kind === "action") return;
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, [full, top]);

  // The sidebar needs the cluster status even when the first view is an actor.
  useEffect(() => {
    if (full && connected && !status) call("status", {}).then((d) => d.kind === "status" && setStatus(d)).catch(() => {});
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
      setToast(done.summary);
      setTimeout(() => setToast(undefined), 4000);
      if (top?.data.kind === "action") setStack((s) => [...s.slice(0, -1), { ...top, data: done }]);
      else await refresh();
    } catch (e) {
      setConfirm(undefined);
      setError(String((e as Error).message ?? e));
    }
  }

  async function ask(prompt: string) {
    try {
      const r = await app.sendMessage({ role: "user", content: [{ type: "text", text: prompt }] });
      if (r && "isError" in r && r.isError) throw new Error("rejected");
      return; // the host shows the question in its conversation
    } catch {
      try { await navigator.clipboard.writeText(prompt); setToast("Copied the question; paste it into your assistant"); }
      catch { setToast(prompt); }
    }
    setTimeout(() => setToast(undefined), 4000);
  }

  const nav: Nav = useMemo(() => ({ act, ask, inline: !full }), [full, top, stack]);

  const page = !top ? (
    <div class="loading">{error ? "" : "Waiting for the tool result…"}</div>
  ) : top.data.kind === "status" ? <StatusPage st={top.data} nav={nav} />
    : top.data.kind === "actors" ? <ActorsPage list={top.data} nav={nav} />
    : top.data.kind === "actor" ? <ActorPage d={top.data} nav={nav} />
    : top.data.kind === "metric" ? <MetricPage m={top.data} />
    : <ActionPage r={top.data} nav={nav} onConfirm={top.data.dryRun ? () => apply(top.data as ActionResult) : undefined} />;

  const overlay = (
    <>
      {error && <div class="content" style={{ paddingBottom: 0 }}><div class="err" role="alert">{error}</div></div>}
      {confirm && <div class="content" style={{ paddingBottom: 0 }}><Confirm r={confirm.r} busy={confirm.busy} onConfirm={() => apply(confirm.r)} onCancel={() => setConfirm(undefined)} /></div>}
      {toast && <div class="toast" role="status">{toast}</div>}
    </>
  );

  if (!full) {
    return (
      <div>
        <div class="ihead">
          {stack.length > 1 && <button type="button" class="btn ghost" onClick={() => setStack((s) => s.slice(0, -1))}>← Back</button>}
          <span class="lbl">Substrate</span>
          <span class="src">{[status?.cluster, (top?.args.atespace as string) || (top?.data.kind === "actor" ? top.data.actor.atespace : "")].filter(Boolean).join(" / ")}</span>
          <span class="sp" />
          {canExpand && <button type="button" class="btn ghost" onClick={() => app.requestDisplayMode({ mode: "fullscreen" })}>Expand</button>}
        </div>
        {overlay}
        {page}
      </div>
    );
  }

  const scope = (top?.args.atespace as string) || "";
  const at = (tool: string, sc = "") => top?.tool === tool && scope === sc && stack.length === 1;
  return (
    <div class="app">
      <aside class="side">
        <div class="scope"><div class="logo"><i /></div>
          <div class="t"><b>{scope || status?.cluster || "substrate"}</b><span>{scope ? status?.cluster : "All atespaces"}</span></div>
        </div>
        <div class="navg">
          <button type="button" class={`ni${at("status", scope) ? " on" : ""}`} onClick={() => go("status", scope ? { atespace: scope } : {}, true)}>
            Overview{status && <span class={`ct${openCount(status, scope) ? " c" : ""}`}>{openCount(status, scope) || ""}</span>}
          </button>
          <button type="button" class={`ni${at("list_actors", scope) ? " on" : ""}`} onClick={() => go("list_actors", scope ? { atespace: scope } : {}, true)}>
            Actors{status && <span class="ct">{actorCount(status, scope)}</span>}
          </button>
          <button type="button" class="ni" onClick={() => go("query_metric", { metric: "resume_p95", by: "template", window: "6h", ...(scope ? { atespace: scope } : {}) }, true)}>
            Resume latency
          </button>
        </div>
        {status && (
          <div class="navg">
            <div class="gl lbl">Scope</div>
            <button type="button" class={`ni${!scope ? " on" : ""}`} onClick={() => go("status", {}, true)}>Whole cluster</button>
            {status.atespaces.map((a) => (
              <button type="button" key={a.name} class={`ni${scope === a.name ? " on" : ""}`} onClick={() => go("status", { atespace: a.name }, true)}>
                {a.name}<span class={`ct${a.problems ? " c" : ""}`}>{a.problems || ""}</span>
              </button>
            ))}
          </div>
        )}
        <div class="foot"><span class={`dot${error ? " err" : ""}`} />{status?.version ?? ""}{status ? " · live" : ""}</div>
      </aside>
      <section class="main">
        <div class="top">
          {stack.length > 1 && <button type="button" class="btn ghost" onClick={() => setStack((s) => s.slice(0, -1))}>←</button>}
          <div class="crumb">{scope && <>{scope} <span>/</span></>} <b>{crumb(top)}</b></div>
          <span class="sp" />
          {(ctx?.availableDisplayModes ?? []).includes("inline") && ctx?.platform !== undefined && (
            <button type="button" class="btn ghost" onClick={() => app.requestDisplayMode({ mode: "inline" })}>Collapse</button>
          )}
        </div>
        {overlay}
        {page}
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
