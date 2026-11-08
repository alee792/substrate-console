import { useEffect, useRef, useState } from "preact/hooks";
import type { Action, Agent, AgentList, AgentPage, AgentVersion, Approval, Playground, SessionPage, SessionRow, SessionStatus, StartArgs, TItem, Turn } from "./types";
import { absolute, ago, clock, plural } from "./format";
import { Btn, Hero, Prop, Sec, type Nav } from "./pages";

/* Agents are Canopy's: Anthropic or OpenAI agents running on Substrate.
   The console shows their definition as the lab API returns it and never
   edits it; a change is a push from git or an edit through that API. */

const HARNESS: Record<string, string> = { "claude-code": "Claude Code", codex: "Codex" };
const LAB: Record<string, string> = { anthropic: "Anthropic API", openai: "OpenAI API" };
const harness = (h: string) => HARNESS[h] ?? h;

const openAgent = (id: string, version?: number): Action => ({ label: "Open", tool: "get_agent", args: version ? { agent: id, version } : { agent: id } });
const openSession = (id: string): Action => ({ label: "Open", tool: "get_session", args: { session: id } });

/** github.com/alee792/substrate-console, without the scheme. */
function repoName(url: string) {
  return url.replace(/^https?:\/\//, "").replace(/\.git$/, "");
}

function whereFrom(a: { source?: Agent["source"] }) {
  if (!a.source) return "Defined through the API";
  const s = a.source;
  return [repoName(s.url), s.path].filter(Boolean).join(" · ");
}

const short = (sha?: string) => (sha ? sha.slice(0, 7) : "");

const STATUS: Record<SessionStatus, string> = { idle: "Idle", in_progress: "Working", requires_action: "Waiting for you", failed: "Failed" };

export function SessionState({ status, env }: { status: SessionStatus; env?: string }) {
  const suspended = status === "idle" && env === "suspended";
  const label = suspended ? "Suspended" : env === "pending" && status !== "failed" ? "Starting" : STATUS[status] ?? status;
  const cls = suspended ? "suspended" : status === "in_progress" || env === "pending" ? "working" : status === "requires_action" ? "waiting" : status;
  return <span class={`ss ${cls}`}>{label}</span>;
}

export function AgentsPage({ list, nav }: { list: AgentList; nav: Nav }) {
  const active = list.agents.reduce((n, a) => n + a.active, 0);
  const sub = [plural(list.agents.length, "agent"), active ? `${active} working now` : "", `on ${list.canopy}`].filter(Boolean).join(" · ");
  return (
    <div class="content">
      <Hero title="Agents" sub={sub} />
      {list.agents.length === 0 ? (
        <div class="empty">
          <p class="lead">No agents yet.</p>
          <p class="note">An agent is an Anthropic or OpenAI agent. Create one through either API with the official SDK pointed at Canopy, or keep it in a repository as <span class="mono">agent.anthropic.yaml</span> and run <span class="mono">canopy push</span>.</p>
        </div>
      ) : (
        <div class="tlist">
          {list.agents.map((a) => (
            <div key={a.id} class="trow arow cursor-interaction" role="button" tabIndex={0}
              onClick={() => nav.act(openAgent(a.id))} onKeyDown={(e) => e.key === "Enter" && nav.act(openAgent(a.id))}>
              <div class="nm">
                <b>{a.name}</b>
                <span>{whereFrom(a)}</span>
              </div>
              <span class="opt dim">{harness(a.harness)} · {a.model}</span>
              <span class="num dim">v{a.version}</span>
              <span class="ct">{a.active > 0 ? <em class="live">{a.active} working</em> : a.sessions > 0 ? `${plural(a.sessions, "session")}` : <span class="dim">No sessions</span>}</span>
              <span class="opt dim p95" title={absolute(a.updatedAt)}>{ago(a.lastActive ?? a.updatedAt)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function contentsLine(v: AgentVersion) {
  const c = v.contents;
  if (!c) return "";
  return [c.instructions ? "Instructions" : "", c.skills.length ? plural(c.skills.length, "skill") : "", c.mcpServers.length ? plural(c.mcpServers.length, "MCP server") : "", c.subagents.length ? plural(c.subagents.length, "subagent") : ""].filter(Boolean).join(", ");
}

export function AgentPageView({ d, nav }: { d: AgentPage; nav: Nav }) {
  const a = d.agent;
  const latest = d.versions.find((v) => v.version === a.version);
  const shown = d.versions.find((v) => v.version === d.shown);
  const file = a.lab === "openai" ? "agent.openai.yaml" : "agent.anthropic.yaml";
  const ordered = [...d.actions].sort((x, y) => Number(!!x.primary) - Number(!!y.primary));
  const c = (shown ?? latest)?.contents;
  return (
    <div class="content">
      <Hero title={a.name} sub={<><span>{harness(a.harness)}</span><span>{a.model}</span><span>v{a.version}</span>{a.active > 0 && <span class="ss working">{a.active} working</span>}</>}>
        <div class="acts">
          <button type="button" class="btn quiet cursor-interaction" onClick={() => nav.ask(`What does the ${a.name} agent do, and how have its versions changed?`)}>Ask</button>
          {ordered.map((x) => <Btn key={x.label} a={x} nav={nav} />)}
        </div>
      </Hero>
      <div class="detail">
        <div class="stack">
          {a.description && <p class="lead">{a.description}</p>}
          <Sec title="Sessions" right={d.sessions.length ? "Latest 20" : undefined}>
            {d.sessions.length === 0 ? <p class="note">No sessions yet. Try it to start one.</p> : <SessionTable rows={d.sessions} nav={nav} />}
          </Sec>
          <Sec title="Versions" right="A push or an API edit makes a new one">
            <div class="tblwrap">
              <table class="tbl">
                <thead><tr><th>Version</th><th>From</th><th class="opt">Bundle</th><th>Created</th></tr></thead>
                <tbody>
                  {d.versions.map((v) => (
                    <tr key={v.version} class={`row${v.version === d.shown ? " cur" : ""}`} tabIndex={0}
                      onClick={() => v.version !== d.shown && nav.act(openAgent(a.id, v.version))}
                      onKeyDown={(e) => e.key === "Enter" && v.version !== d.shown && nav.act(openAgent(a.id, v.version))}>
                      <td class="id">v{v.version}</td>
                      <td>{v.origin === "git" && v.source ? <><span class="mono">{short(v.source.commit)}</span><span class="dim"> on {v.source.ref || "a detached head"}</span></> : <span class="dim">API edit</span>}</td>
                      <td class="opt dim">{contentsLine(v) || "–"}</td>
                      <td class="dim" title={absolute(v.createdAt)}>{ago(v.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Sec>
          <Sec title="Definition" right={`v${d.shown}, as the ${LAB[a.lab]} returns it`}>
            {d.definitionNote && <p class="note" style={{ marginBottom: 10 }}>{d.definitionNote}</p>}
            {d.definition != null && <div class="logs code"><pre>{JSON.stringify(d.definition, null, 2)}</pre></div>}
            <p class="note" style={{ marginTop: 10 }}>
              {a.source
                ? <>To change it, edit <span class="mono">{[a.source.path, file].filter(Boolean).join("/")}</span> and run <span class="mono">canopy push</span>, or update it through the {LAB[a.lab]}. Either makes a new version.</>
                : <>To change it, update it through the {LAB[a.lab]}, or bring it into git with <span class="mono">canopy pull {a.name}</span>.</>}
            </p>
          </Sec>
        </div>
        <aside class="props">
          <Prop k="Runs on" v={`${harness(a.harness)}, through the ${LAB[a.lab]}`} />
          <Prop k="Model" v={a.model} />
          <div class="hr" />
          {a.source ? (
            <>
              <Prop k="Repository" v={repoName(a.source.url)} />
              {a.source.path && <Prop k="Directory" v={a.source.path} mono />}
              {a.source.ref && <Prop k="Branch" v={a.source.ref} mono />}
              {a.source.commit && <Prop k="Commit" v={short(a.source.commit)} mono />}
            </>
          ) : <Prop k="Defined" v="Through the API" />}
          {c && (
            <>
              <div class="hr" />
              <Prop k="Instructions" v={c.instructions ? "AGENTS.md" : "None"} />
              {c.skills.length > 0 && <Prop k="Skills" v={c.skills.join(", ")} />}
              {c.mcpServers.length > 0 && <Prop k="MCP servers" v={c.mcpServers.join(", ")} />}
              {c.subagents.length > 0 && <Prop k="Subagents" v={c.subagents.join(", ")} />}
            </>
          )}
          <div class="hr" />
          <Prop k="Updated" v={ago(a.updatedAt)} />
          <Prop k="ID" v={a.id} mono />
        </aside>
      </div>
    </div>
  );
}

function SessionTable({ rows, nav }: { rows: SessionRow[]; nav: Nav }) {
  return (
    <div class="tblwrap">
      <table class="tbl">
        <tbody>
          {rows.map((s) => (
            <tr class="row" key={s.id} tabIndex={0} onClick={() => nav.act(openSession(s.id))} onKeyDown={(e) => e.key === "Enter" && nav.act(openSession(s.id))}>
              <td><SessionState status={s.status} env={s.envStatus} /></td>
              <td class={s.preview ? "pv" : "id"}>{s.preview || s.id}</td>
              <td class="dim num opt">{cents(s.spendCents)}</td>
              <td class="dim" title={absolute(s.createdAt)}>{ago(s.lastActive)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function cents(c: number) {
  if (!c) return "–";
  return c < 100 ? `${c.toFixed(1)}¢` : `$${(c / 100).toFixed(2)}`;
}

/** The playground starts a real session, through the agent's own API. The
 * server fills and checks it; this form edits it and submits it. */
export function PlaygroundPage({ d, nav, start }: { d: Playground; nav: Nav; start: (args: StartArgs) => Promise<void> }) {
  const a = d.agent;
  const anth = a.lab === "anthropic";
  const [f, setF] = useState<StartArgs>({
    agent: a.id, version: d.version || undefined, environment: d.environment, network: d.network,
    allowed_domains: d.allowed_domains, repository: d.repository, branch: d.branch, message: d.message ?? "",
  });
  const [domains, setDomains] = useState((d.allowed_domains ?? []).join(", "));
  const [busy, setBusy] = useState(false);
  const msg = useRef<HTMLTextAreaElement>(null);
  useEffect(() => { if (!nav.inline) msg.current?.focus(); }, []);
  const p = d.problems ?? {};
  const set = <K extends keyof StartArgs>(k: K, v: StartArgs[K]) => setF((s) => ({ ...s, [k]: v }));
  const env = d.environments.find((e) => e.id === f.environment);

  async function go() {
    if (busy || !f.message.trim()) return;
    setBusy(true);
    const list = domains.split(/[\s,]+/).filter(Boolean);
    try { await start({ ...f, allowed_domains: anth ? undefined : list }); } finally { setBusy(false); }
  }

  return (
    <div class="content">
      <Hero title={`Try ${a.name}`} sub={`Starts a real session through the ${LAB[a.lab]}, the way an app would.`} />
      <div class="form">
        <Field label="Message" err={p.message}>
          <textarea ref={msg} rows={4} value={f.message} placeholder="What should it do?" spellcheck={true}
            onInput={(e) => set("message", (e.target as HTMLTextAreaElement).value)}
            onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); go(); } }} />
        </Field>
        {anth ? (
          <>
            <div class="row2">
              <Field label="Version" err={p.version}>
                <select value={f.version ?? 0} onChange={(e) => set("version", +(e.target as HTMLSelectElement).value || undefined)}>
                  <option value={0}>Latest (v{a.version})</option>
                  {d.versions.filter((v) => v !== a.version).map((v) => <option key={v} value={v}>v{v}</option>)}
                </select>
              </Field>
              <Field label="Environment" err={p.environment} hint={env ? networkLine(env) : undefined}>
                <select value={f.environment ?? ""} disabled={d.environments.length === 0} onChange={(e) => set("environment", (e.target as HTMLSelectElement).value)}>
                  {d.environments.length === 0 && <option value="">None yet</option>}
                  {d.environments.map((e) => <option key={e.id} value={e.id}>{e.name || e.id}</option>)}
                </select>
              </Field>
            </div>
            <div class="row2 wide">
              <Field label="Repository" err={p.repository} hint="Optional. Checked out into the session">
                <input class="mono" value={f.repository ?? ""} spellcheck={false} autocomplete="off" placeholder="https://github.com/org/repo"
                  onInput={(e) => set("repository", (e.target as HTMLInputElement).value)} />
              </Field>
              <Field label="Branch">
                <input class="mono" value={f.branch ?? ""} spellcheck={false} autocomplete="off" placeholder="default" disabled={!f.repository}
                  onInput={(e) => set("branch", (e.target as HTMLInputElement).value)} />
              </Field>
            </div>
          </>
        ) : (
          <>
            <Field label="Network" err={p.network} hint={f.network === "enabled" ? "Every host over HTTP and TLS" : domains.trim() ? "The platform's hosts and the domains below" : "Only what the platform needs: the model, MCP servers, git hosts with credentials"}>
              <div class="seg">
                {[["disabled", "Platform only"], ["enabled", "Open"]].map(([v, l]) => (
                  <label key={v} class={f.network === v ? "on" : ""}><input type="radio" name="net" checked={f.network === v} onChange={() => set("network", v)} />{l}</label>
                ))}
              </div>
            </Field>
            {f.network !== "enabled" && (
              <Field label="Allowed domains" hint="Optional, comma-separated">
                <input class="mono" value={domains} spellcheck={false} autocomplete="off" placeholder="github.com, proxy.golang.org" onInput={(e) => setDomains((e.target as HTMLInputElement).value)} />
              </Field>
            )}
            <p class="note">OpenAI sessions run the agent's latest version, v{a.version}.</p>
          </>
        )}
        {p.start && <div class="err" role="alert">{p.start}</div>}
        <p class="note">The environment decides what the session can reach. The agent can't widen it.</p>
        <div class="acts" style={{ justifyContent: "flex-end" }}>
          {nav.back && <button type="button" class="btn quiet cursor-interaction" onClick={nav.back}>Cancel</button>}
          <button type="button" class="btn pri cursor-interaction" disabled={busy || nav.readOnly || !f.message.trim() || (anth && !f.environment)} onClick={go}>{busy ? "Starting…" : "Start session"}</button>
        </div>
      </div>
    </div>
  );
}

function networkLine(e: Playground["environments"][number]) {
  if (e.network !== "limited") return "Open: every host over HTTP and TLS";
  const extra = [e.packageManagers ? "package managers" : "", e.mcpServers ? "MCP servers" : ""].filter(Boolean);
  const hosts = e.hosts ?? [];
  const list = hosts.length > 3 ? `${hosts.slice(0, 3).join(", ")} and ${hosts.length - 3} more` : hosts.join(", ");
  return `Limited to ${[list, ...extra].filter(Boolean).join(", ") || "the platform's hosts"}`;
}

function Field({ label, err, hint, children }: { label: string; err?: string; hint?: string; children: preact.ComponentChildren }) {
  return (
    <label class={`field${err ? " bad" : ""}`}>
      <span class="l">{label}</span>
      {children}
      {(err || hint) && <span class="h">{err || hint}</span>}
    </label>
  );
}

export function SessionPageView({ d, nav, run }: { d: SessionPage; nav: Nav; run: (tool: string, args: Record<string, unknown>) => Promise<boolean> }) {
  const s = d.session;
  const name = d.agent?.name ?? "Session";
  const t = d.transcript;
  const working = s.status === "in_progress";
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const end = useRef<HTMLDivElement>(null);
  const turns = t.turns.length;
  const last = t.turns[turns - 1];
  // Follow the transcript as it grows, in full screen.
  useEffect(() => { if (!nav.inline && working) end.current?.scrollIntoView({ block: "end", behavior: "smooth" }); }, [turns, last?.items.length]);

  async function send() {
    const text = draft.trim();
    if (!text || sending) return;
    setSending(true);
    try { if (await run("send_message", { session: s.id, text })) setDraft(""); } finally { setSending(false); }
  }

  const sb = s.sandbox;
  const ordered = [...d.actions].sort((x, y) => Number(!!y.destructive) - Number(!!x.destructive));
  const shownTurns = nav.inline ? t.turns.slice(-2) : t.turns;
  return (
    <div class="content">
      <Hero eyebrow={d.agent ? undefined : s.id} title={name}
        sub={<><SessionState status={s.status} env={sb.status} /><span title={absolute(s.createdAt)}>started {ago(s.createdAt)}</span><span>{s.model}</span>{s.spendCents > 0 && <span>{cents(s.spendCents)}</span>}</>}>
        <div class="acts">
          {ordered.map((x) => <Btn key={x.label} a={x} nav={nav} sm={nav.inline} />)}
        </div>
      </Hero>
      <div class="detail">
        <div class="stack">
          {(s.error || sb.error) && (
            <div class="cause">
              <div class="h"><b>{s.status === "failed" ? "Session failed" : "Sandbox failed"}</b></div>
              <pre>{s.error || sb.error}</pre>
            </div>
          )}
          <div class="transcript">
            {(t.earlier ?? 0) > 0 && <p class="note">{plural(t.earlier!, "earlier turn")} not shown.</p>}
            {nav.inline && t.turns.length > shownTurns.length && <p class="note">{plural(t.turns.length - shownTurns.length, "earlier turn")}.</p>}
            {t.turns.length === 0 && <p class="note">{sb.status === "pending" ? "Starting the sandbox…" : "Nothing yet."}</p>}
            {shownTurns.map((turn) => <TurnView key={turn.id} t={turn} inline={nav.inline} shown={s.error || sb.error} waiting={s.status === "requires_action"} />)}
            {d.approvals.map((a) => <ApprovalCard key={a.eventId} a={a} session={s.id} run={run} readOnly={nav.readOnly} />)}
            {d.blocked && <div class="waiting"><p>{d.blocked}</p></div>}
            <div ref={end} />
          </div>
          {!nav.readOnly && s.status !== "failed" && (
            <div class="composer">
              <textarea rows={nav.inline ? 1 : 2} value={draft} placeholder={working ? "Queues behind the running turn" : sb.status === "suspended" ? "Sending resumes it" : "Reply"}
                onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
                onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }} />
              <button type="button" class="btn pri cursor-interaction" disabled={!draft.trim() || sending} onClick={send}>{sending ? "Sending…" : "Send"}</button>
            </div>
          )}
          {nav.inline && nav.expand && <button type="button" class="more cursor-interaction" onClick={nav.expand}>Open console →</button>}
        </div>
        {!nav.inline && (
          <aside class="props">
            {d.agent && (
              <div class="prop"><div class="k">Agent</div>
                <button type="button" class="v link cursor-interaction" onClick={() => nav.act(openAgent(d.agent!.id))}>{d.agent.name}</button></div>
            )}
            <Prop k="Runs on" v={`${harness(s.harness)}, through the ${LAB[s.lab]}`} />
            <Prop k="Tokens" v={`${(s.usage.input + s.usage.cached).toLocaleString()} in · ${s.usage.output.toLocaleString()} out`} />
            <Prop k="Spend" v={`${cents(s.spendCents)}${s.spendLimitCents ? ` of ${cents(s.spendLimitCents)}` : ""}`} />
            <div class="hr" />
            <Prop k="Sandbox" v={sandboxLine(sb.status)} />
            <Prop k="Network" v={NETWORK[sb.network] ?? sb.network} />
            {sb.egress && sb.egress.length > 0 && sb.network !== "enabled" && <Prop k="Can reach" v={hostsLine(sb.egress)} wrap />}
            {sb.provider === "substrate" ? (
              <>
                {sb.actor && sb.atespace && (
                  <div class="prop"><div class="k">Actor</div>
                    <button type="button" class="v link mono cursor-interaction" onClick={() => nav.act({ label: "Open", tool: "get_actor", args: { atespace: sb.atespace, name: sb.actor } })}>{sb.atespace}/{sb.actor}</button></div>
                )}
                {sb.node && <Prop k="Worker" v={sb.node} mono />}
                {sb.template && <Prop k="Template" v={sb.template} mono />}
              </>
            ) : sb.provider && <Prop k="Provider" v={sb.provider === "local" ? "Local processes" : sb.provider} />}
            <div class="hr" />
            <Prop k="Compute" v={duration(s.computeSeconds)} />
            <Prop k="Last active" v={ago(s.lastActive)} />
            <Prop k="ID" v={s.id} mono />
          </aside>
        )}
      </div>
    </div>
  );
}

/** A few hosts by name, the rest counted. */
function hostsLine(hosts: string[]) {
  const named = hosts.filter((h) => !/^[\d.:]+$/.test(h));
  const list = (named.length ? named : hosts).slice(0, 3).join(", ");
  return hosts.length > 3 ? `${list} and ${hosts.length - 3} more` : list;
}

const NETWORK: Record<string, string> = { enabled: "Open", disabled: "Platform only", restricted: "Restricted" };

function sandboxLine(st: string) {
  return ({ pending: "Starting", ready: "Running", suspended: "Suspended", failed: "Failed", expired: "Expired" } as Record<string, string>)[st] ?? st;
}

function duration(sec: number) {
  if (sec < 60) return `${Math.round(sec)} s`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min ${Math.round(sec % 60)} s`;
  return `${Math.floor(sec / 3600)} h ${Math.round((sec % 3600) / 60)} min`;
}

function TurnView({ t, inline, shown, waiting }: { t: Turn; inline: boolean; shown?: string; waiting?: boolean }) {
  const took = t.started && t.ended ? (Date.parse(t.ended) - Date.parse(t.started)) / 1000 : undefined;
  return (
    <div class={`turn ${t.status}`}>
      {t.input && <div class="ask">{t.input}</div>}
      <div class="steps">
        {(t.dropped ?? 0) > 0 && <p class="note">{plural(t.dropped!, "earlier step")} not shown.</p>}
        {t.items.map((it) => <Step key={it.id} it={it} inline={inline} />)}
        {t.status === "queued" && <div class="pulse">Queued</div>}
        {t.status === "running" && !waiting && <div class="pulse">Working</div>}
        {t.status === "failed" && t.error !== shown && <div class="err">{t.error || "The turn failed."}</div>}
        {t.status === "cancelled" && <p class="note">Stopped.</p>}
      </div>
      {t.status === "completed" && (t.usage || took !== undefined) && (
        <div class="meta" title={t.ended ? absolute(t.ended) : undefined}>
          {[took !== undefined ? `${took < 10 ? took.toFixed(1) : Math.round(took)} s` : "", t.usage ? `${(t.usage.input + t.usage.cached).toLocaleString()} in · ${t.usage.output.toLocaleString()} out` : "", t.ended ? clock(t.ended, false) : ""].filter(Boolean).join(" · ")}
        </div>
      )}
    </div>
  );
}

function Step({ it, inline }: { it: TItem; inline: boolean }) {
  const failed = it.status === "failed" || (it.exitCode !== undefined && it.exitCode !== 0);
  switch (it.kind) {
    case "assistant_message":
      return <div class="say">{it.text}{it.status === "in_progress" && <span class="caret" />}</div>;
    case "reasoning":
      if (!it.text) return null;
      return <details class="think"><summary>Thought</summary><div>{it.text}</div></details>;
    case "command_execution":
      return (
        <details class={`tool${failed ? " bad" : ""}`} open={!inline && failed}>
          <summary><span class="mono">$ {it.command}</span>{it.exitCode !== undefined && it.exitCode !== 0 && <em>exit {it.exitCode}</em>}{it.status === "in_progress" && <i class="spin" />}{decision(it)}</summary>
          {it.output ? <pre>{it.output}</pre> : <pre class="dim">{it.status === "in_progress" ? "Running…" : "No output."}</pre>}
        </details>
      );
    case "file_change":
      return (
        <div class="tool files">
          {(it.changes ?? []).map((c) => <div key={c.path}><span class="k">{CHANGE[c.kind] ?? c.kind}</span><span class="mono">{c.path}</span></div>)}
        </div>
      );
    default: {
      const label = it.kind === "web_search" ? "Searched" : it.server ? `${it.server} · ${it.name}` : it.name || it.kind;
      const detail = it.kind === "web_search" ? it.query : argsLine(it.args);
      return (
        <details class={`tool${failed ? " bad" : ""}`}>
          <summary><b>{label}</b>{detail && <span class="mono dim">{detail}</span>}{it.status === "in_progress" && <i class="spin" />}{decision(it)}</summary>
          {(it.args || it.output || it.error) && <pre>{[it.args, it.output, it.error].filter(Boolean).join("\n\n")}</pre>}
        </details>
      );
    }
  }
}

const CHANGE: Record<string, string> = { add: "Added", delete: "Deleted", update: "Edited" };

function decision(it: TItem) {
  if (it.decision === "allow") return <span class="dec">Allowed</span>;
  if (it.decision === "deny") return <span class="dec no">Denied</span>;
  return null;
}

/** One line from a tool's arguments: a path or query when there is one. */
function argsLine(args?: string): string {
  if (!args) return "";
  try {
    const v = JSON.parse(args) as Record<string, unknown>;
    for (const k of ["file_path", "path", "pattern", "url", "query", "command"]) if (typeof v[k] === "string") return v[k] as string;
  } catch { /* not JSON */ }
  return args.length > 80 ? args.slice(0, 79) + "…" : args;
}

function ApprovalCard({ a, session, run, readOnly }: { a: Approval; session: string; run: (tool: string, args: Record<string, unknown>) => Promise<boolean>; readOnly?: boolean }) {
  const [busy, setBusy] = useState<"" | "allow" | "deny">("");
  async function answer(decision: "allow" | "deny") {
    setBusy(decision);
    try { await run("answer_approval", { session, event: a.eventId, decision }); } finally { setBusy(""); }
  }
  return (
    <div class="approval" role="group" aria-label="Approval">
      <div class="h"><b>{a.command ? "Wants to run a command" : `Wants to use ${a.server ? `${a.server} · ${a.tool}` : a.tool}`}</b></div>
      <pre>{a.command ?? a.input ?? ""}</pre>
      {!readOnly && (
        <div class="acts">
          <button type="button" class="btn cursor-interaction" disabled={!!busy} onClick={() => answer("deny")}>{busy === "deny" ? "Denying…" : "Deny"}</button>
          <button type="button" class="btn pri cursor-interaction" disabled={!!busy} onClick={() => answer("allow")}>{busy === "allow" ? "Allowing…" : "Allow"}</button>
        </div>
      )}
    </div>
  );
}
