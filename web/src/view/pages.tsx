import { useState } from "preact/hooks";
import type { Action, ActionResult, Actor, ActorDetail, ActorList, Item, MetricResult, Status, Vital } from "./types";
import { absolute, ago, bytes, clock, plural, shortWorker } from "./format";
import { LineChart, Spark } from "./charts";

export interface Nav {
  /** Run an action: navigate for reads, dry-run then confirm for writes. */
  act(a: Action): void;
  /** Put a question in the host's composer. */
  ask(prompt: string): void;
  /** Open the full console, when the host allows it. */
  expand?: () => void;
  /** Go back one page. */
  back?: () => void;
  inline: boolean;
  readOnly?: boolean;
}

const WRITE_TOOLS = new Set(["suspend_actors", "resume_actors", "revert_actors", "delete_actors", "drain_worker", "create_actor", "delete_template"]);
export const isWrite = (tool: string) => WRITE_TOOLS.has(tool);

const cap = (s: string) => s[0].toUpperCase() + s.slice(1);

export function Btn({ a, nav, sm }: { a: Action; nav: Nav; sm?: boolean }) {
  return (
    <button type="button" class={`btn cursor-interaction${a.primary ? " pri" : ""}${sm ? " sm" : ""}`}
      onClick={(e) => { e.stopPropagation(); nav.act(a); }}>
      {a.label}
    </button>
  );
}

export function Hero({ eyebrow, title, mono, sub, children }: { eyebrow?: string; title: string; mono?: boolean; sub?: preact.ComponentChildren; children?: preact.ComponentChildren }) {
  return (
    <header class="hero">
      <div style={{ minWidth: 0 }}>
        {eyebrow && <div class="eyebrow">{eyebrow}</div>}
        <h1 class={mono ? "mono" : ""}>{title}</h1>
        {sub && <div class="sub">{sub}</div>}
      </div>
      <span class="sp" />
      {children}
    </header>
  );
}

export function Sec({ title, right, children }: { title: string; right?: string; children: preact.ComponentChildren }) {
  return (
    <section>
      <div class="sec"><h2>{title}</h2>{right && <span class="r">{right}</span>}</div>
      {children}
    </section>
  );
}

function Issue({ it, nav }: { it: Item; nav: Nav }) {
  const open = (it.actions ?? []).find((a) => !isWrite(a.tool));
  const primary = (it.actions ?? []).find((a) => a.primary && isWrite(a.tool));
  return (
    <div class={`issue ${it.severity}${open ? " cursor-interaction" : ""}`} role={open ? "button" : undefined} tabIndex={open ? 0 : undefined}
      onClick={() => open && nav.act(open)} onKeyDown={(e) => open && e.key === "Enter" && nav.act(open)}>
      <span class="sev" />
      <div style={{ minWidth: 0 }}>
        <div class="t">{it.title}</div>
        {it.facts && it.facts.length > 0 && <div class="m">{it.facts.map((f) => <span key={f}>{f}</span>)}</div>}
      </div>
      {primary && <div class="acts"><Btn a={{ ...primary, primary: false }} nav={nav} sm /></div>}
    </div>
  );
}

function VitalBlock({ v, nav }: { v: Vital; nav: Nav }) {
  return (
    <button type="button" class={`vital cursor-interaction ${v.tone ?? ""}`} onClick={() => v.drill && nav.act(v.drill)} title={v.drill?.label}>
      <span class="l">{v.label}</span>
      <span class="v">{v.value}{v.unit && <small>{v.unit}</small>}{v.delta && <em>{v.delta}</em>}</span>
      <Spark series={v.series} baseline={v.baseline} />
      <span class="n">{v.note || " "}</span>
    </button>
  );
}

function Stat({ v, nav }: { v: Vital; nav: Nav }) {
  return (
    <button type="button" class={`stat cursor-interaction ${v.tone ?? ""}`} onClick={() => v.drill && nav.act(v.drill)}>
      <div class="v">{v.value}{v.unit && <small>{v.unit}</small>}</div>
      <div class="l">{v.label}</div>
    </button>
  );
}

export function StatusPage({ st, nav }: { st: Status; nav: Nav }) {
  const [showResolved, setShowResolved] = useState(false);
  const open = st.items.filter((i) => i.severity !== "resolved");
  const resolved = st.items.filter((i) => i.severity === "resolved");
  const shown = nav.inline ? open.slice(0, 3) : open;
  const where = st.scope ? `${st.scope} · ${st.cluster}` : st.cluster;

  if (nav.inline) {
    return (
      <div class="content">
        <Hero title={st.headline} sub={<span>{where} · {st.summary}</span>} />
        {open.length > 0 ? (
          <div class="issues">{shown.map((it) => <Issue key={it.id} it={it} nav={nav} />)}</div>
        ) : (
          <div class="clear"><span class="dot" />Nothing needs attention.</div>
        )}
        <div class="stats">{st.vitals.map((v) => <Stat key={v.key} v={v} nav={nav} />)}</div>
        {nav.expand && (
          <button type="button" class="more cursor-interaction" onClick={nav.expand}>
            Open console{open.length > shown.length && <span>{open.length - shown.length} more issues</span>} →
          </button>
        )}
      </div>
    );
  }

  return (
    <div class="content">
      <Hero title={st.headline} sub={st.summary} />
      {open.length > 0 || resolved.length > 0 ? (
        <div class="issues">
          {open.map((it) => <Issue key={it.id} it={it} nav={nav} />)}
          {showResolved && resolved.map((it) => <Issue key={it.id} it={it} nav={nav} />)}
          {resolved.length > 0 && (
            <button type="button" class="resolvedtoggle cursor-interaction" onClick={() => setShowResolved(!showResolved)}>
              {showResolved ? "Hide resolved" : `${plural(resolved.length, "issue")} resolved in the last 24 h`}
            </button>
          )}
        </div>
      ) : (
        <div class="clear"><span class="dot" />Nothing needs attention.</div>
      )}
      <Sec title="Last 24 hours" right="Dashed line is the same time last week">
        <div class="vitals">{st.vitals.map((v) => <VitalBlock key={v.key} v={v} nav={nav} />)}</div>
      </Sec>
      {st.pools && st.pools.length > 0 && (
        <Sec title="Worker pools">
          <div class="pools">
            {st.pools.map((p) => {
              const pct = p.ready ? (100 * p.busy) / p.ready : 0;
              return (
                <div class="pool" key={p.name}>
                  <span class="nm">{p.name}{p.sandboxClass && <span>{p.sandboxClass}</span>}</span>
                  <div class="meter"><i class={pct >= 95 ? "w" : ""} style={{ width: `${pct}%` }} /></div>
                  <span class="n"><b>{p.busy}</b> / {p.ready}{p.ready < p.desired ? ` of ${p.desired}` : ""}</span>
                </div>
              );
            })}
          </div>
        </Sec>
      )}
      {(st.notes ?? []).map((n) => <p class="note" key={n}>{n}</p>)}
    </div>
  );
}

function stateLabel(a: Actor): string {
  if (a.state === "crashed") return a.reason === "Out of memory" ? "Crashed, OOM" : "Crashed";
  if (a.needsYou) return `Stuck ${a.state}`;
  return cap(a.state);
}

export function StatePill({ a }: { a: Actor }) {
  return <span class={`st ${a.state}${a.needsYou && a.state !== "crashed" ? " stuck" : ""}`}>{stateLabel(a)}</span>;
}

function openActor(a: Actor): Action {
  return { label: "Open", tool: "get_actor", args: { atespace: a.atespace, name: a.name } };
}

const MOVING = ["running", "resuming", "suspending", "reverting", "deleting"];

export function ActorsPage({ list, nav }: { list: ActorList; nav: Nav }) {
  const groups: { name: string; rows: Actor[] }[] = [
    { name: "Needs attention", rows: list.actors.filter((a) => a.needsYou) },
    { name: "Running", rows: list.actors.filter((a) => !a.needsYou && MOVING.includes(a.state)) },
    { name: "Suspended", rows: list.actors.filter((a) => !a.needsYou && a.state === "suspended") },
    { name: "Other", rows: list.actors.filter((a) => !a.needsYou && !MOVING.includes(a.state) && a.state !== "suspended") },
  ].filter((g) => g.rows.length);
  const c = list.counts;
  const sub = [plural(list.total, "actor"), c.running ? `${c.running} running` : "", list.shown < list.total ? `showing ${list.shown}` : ""].filter(Boolean).join(" · ");
  return (
    <div class="content">
      <Hero title="Actors" sub={sub} />
      {list.hint && <p class="note" style={{ color: "var(--fg2)" }}>{list.hint}</p>}
      <div class="tblwrap">
        <table class="tbl">
          <thead><tr><th>Name</th><th>State</th><th class="opt">Template</th><th class="opt">Worker</th><th>Active</th><th class="opt">Memory</th></tr></thead>
          <tbody>
            {groups.map((g) => [
              <tr class="grp" key={g.name}><td colSpan={6}><b>{g.name}</b>{g.rows.length}</td></tr>,
              ...g.rows.map((a) => (
                <tr class="row" key={a.atespace + a.name} tabIndex={0} onClick={() => nav.act(openActor(a))}
                  onKeyDown={(e) => e.key === "Enter" && nav.act(openActor(a))}>
                  <td class="id">{list.scope ? a.name : `${a.atespace}/${a.name}`}</td>
                  <td><StatePill a={a} /></td>
                  <td class="opt">{a.template}</td>
                  <td class="opt dim">{shortWorker(a.worker, a.pool)}</td>
                  <td class="dim" title={absolute(a.lastActive)}>{ago(a.lastActive)}</td>
                  <td class="opt dim num">{a.memoryBytes ? bytes(a.memoryBytes) : a.snapshots?.[0]?.sizeBytes ? `${bytes(a.snapshots[0].sizeBytes)} saved` : "–"}</td>
                </tr>
              )),
            ])}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export function ActorPage({ d, nav }: { d: ActorDetail; nav: Nav }) {
  const a = d.actor;
  const ref = `${a.atespace}/${a.name}`;
  const since = a.crash?.time ?? a.stateSince;
  const snap = a.snapshots?.[0];
  const question = a.state === "crashed" ? `Why did ${ref} crash, and what should I change?` : `Is anything wrong with ${ref}?`;
  const ordered = [...d.actions].sort((x, y) => Number(!!x.primary) - Number(!!y.primary));
  return (
    <div class="content">
      <Hero title={a.name} mono
        sub={<><StatePill a={a} /><span title={absolute(since)}>{ago(since)}</span><span>{a.template}</span></>}>
        <div class="acts">
          <button type="button" class="btn quiet cursor-interaction" onClick={() => nav.ask(question)}>Ask</button>
          {ordered.map((x) => <Btn key={x.label} a={x} nav={nav} />)}
        </div>
      </Hero>
      <div class="detail">
        <div class="stack">
          {(a.crash || d.hint) && (
            <div class="cause">
              {a.crash && <div class="h"><b>{a.reason || "Crashed"}</b><span title={absolute(a.crash.time)}>{clock(a.crash.time)}</span></div>}
              {d.hint ? <p>{d.hint}</p> : <div style={{ height: 12 }} />}
              {a.crash && <pre>{a.crash.message}</pre>}
            </div>
          )}
          {d.timeline.length > 0 && (
            <Sec title="Timeline">
              <div class="tl">
                {d.timeline.map((e, i) => (
                  <div class={`ev ${e.state}`} key={i}>
                    <span class="p" />
                    <span class="w"><b>{cap(e.state)}</b>{e.detail && <span> · {e.detail}</span>}</span>
                    <span class="t" title={absolute(e.time)}>{clock(e.time)}</span>
                  </div>
                ))}
              </div>
            </Sec>
          )}
          {d.logs.length > 0 && (
            <Sec title="Logs" right={`Last ${d.logs.length} lines`}>
              <div class="logs"><pre>{d.logs.map((l, i) => (
                <span key={i}><span class="ts">{clock(l.time)}</span>  <span class={l.error ? "e" : ""}>{l.text}</span>{"\n"}</span>
              ))}</pre></div>
            </Sec>
          )}
        </div>
        <aside class="props">
          {d.template?.memoryLimitBytes ? <Prop k="Memory" v={`${a.memoryBytes ? bytes(a.memoryBytes) + " of " : ""}${bytes(d.template.memoryLimitBytes)}${a.memoryBytes ? "" : " limit"}`} /> : null}
          <Prop k={a.state === "running" ? "Worker" : "Last worker"} v={shortWorker(a.worker, a.pool)} mono />
          {a.sandboxClass && <Prop k="Sandbox" v={a.sandboxClass} />}
          <div class="hr" />
          <Prop k="Latest snapshot" v={snap ? `Generation ${snap.generation}` : "None"} />
          {snap && <Prop k="Stored" v={`${cap(snap.storage)}${snap.sizeBytes ? ", " + bytes(snap.sizeBytes) : ""}`} />}
          <div class="hr" />
          <Prop k="Created" v={ago(a.createdAt)} />
          {a.node && <Prop k="Node" v={a.node} mono />}
        </aside>
      </div>
    </div>
  );
}

function Prop({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return <div class="prop"><div class="k">{k}</div><div class={`v${mono ? " mono" : ""}`} title={v}>{v}</div></div>;
}

export function MetricPage({ m }: { m: MetricResult }) {
  return (
    <div class="content">
      <Hero title={m.title} sub={`Last ${m.window}`} />
      <LineChart series={m.series} unit={m.unit} />
    </div>
  );
}

export function ActionPage({ r, nav, onConfirm }: { r: ActionResult; nav: Nav; onConfirm?: () => void }) {
  if (r.dryRun) return <div class="content"><Confirm r={r} onConfirm={onConfirm} /></div>;
  return (
    <div class="content">
      <div class="done">
        <span class="ok"><svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M2.5 6.2 5 8.5l4.5-5" /></svg></span>
        <div>
          <div style={{ fontWeight: 550, fontSize: "15px" }}>{r.summary}</div>
          <div class="note" style={{ marginTop: 2 }}>{r.targets.map((t) => `${t.atespace}/${t.name}`).join(", ")}</div>
        </div>
      </div>
      {nav.inline && (
        <button type="button" class="more cursor-interaction" onClick={() => nav.act({ label: "Status", tool: "status", args: {} })}>Cluster status →</button>
      )}
    </div>
  );
}

const CONSEQUENCE: Record<string, string> = {
  revert: "Each actor restarts from its latest snapshot. Work since then is lost.",
  delete: "Actors and their snapshots are removed. This can't be undone.",
  drain: "No new actors land on this worker until it's back.",
  suspend: "Actors snapshot and stop. Resume brings them back.",
  resume: "Actors start from their latest snapshot.",
};

export function Confirm({ r, onConfirm, onCancel, busy }: { r: ActionResult; onConfirm?: () => void; onCancel?: () => void; busy?: boolean }) {
  const ok = r.targets.filter((t) => t.ok).length;
  const destructive = r.verb.startsWith("delete") || r.verb === "revert" || r.verb === "drain";
  const label = r.targets.length === 0 ? cap(r.verb) : r.verb === "create" ? "Create actor" : `${cap(r.verb)} ${ok === 1 ? "actor" : `${ok} actors`}`;
  return (
    <div class="confirm" role="dialog" aria-label={`Confirm ${r.verb}`}>
      <div>
        <h2>{r.summary}</h2>
        {(r.note || (ok > 0 && CONSEQUENCE[r.verb])) && <p style={{ marginTop: 4 }}>{r.note || CONSEQUENCE[r.verb]}</p>}
      </div>
      {r.targets.length > 0 && <ul>
        {r.targets.map((t) => (
          <li key={t.atespace + t.name}>
            <span class={`st ${t.state}`} />
            <span class="mono">{t.atespace}/{t.name}</span>
            {!t.ok && <span class="why">{t.reason}</span>}
          </li>
        ))}
      </ul>}
      <div class="acts">
        {onCancel && <button type="button" class="btn quiet cursor-interaction" onClick={onCancel}>Cancel</button>}
        {onConfirm && (ok > 0 || r.targets.length === 0) && (
          <button type="button" class={`btn cursor-interaction ${destructive ? "danger" : "pri"}`} disabled={busy} onClick={onConfirm}>
            {busy ? "Working…" : label}
          </button>
        )}
      </div>
    </div>
  );
}
