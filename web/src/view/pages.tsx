import type { ComponentChildren } from "preact";
import type { Action, ActionResult, Actor, ActorDetail, ActorList, Item, MetricResult, Status, Vital } from "./types";
import { absolute, ago, bytes, clock, plural, shortWorker } from "./format";
import { LineChart, Spark } from "./charts";

export interface Nav {
  /** Run an action: navigate for reads, dry-run then confirm for writes. */
  act(a: Action): void;
  /** Ask the model in the host conversation. */
  ask(prompt: string): void;
  inline: boolean;
}

const WRITE_TOOLS = new Set(["suspend_actors", "resume_actors", "revert_actors", "delete_actors", "drain_worker"]);
export const isWrite = (tool: string) => WRITE_TOOLS.has(tool);

function Btn({ a, nav }: { a: Action; nav: Nav }) {
  return (
    <button type="button" class={`btn${a.primary ? " pri" : a.destructive && !a.primary ? " ghost" : ""}`} onClick={() => nav.act(a)}>
      {a.label}
    </button>
  );
}

function Title({ title, sub, children }: { title: string; sub?: string; children?: ComponentChildren }) {
  return (
    <div class="pagetitle">
      <div>
        <h1>{title}</h1>
        {sub && <div class="sub">{sub}</div>}
      </div>
      <span class="sp" />
      {children}
    </div>
  );
}

function AttentionItem({ it, nav }: { it: Item; nav: Nav }) {
  return (
    <div class={`ai ${it.severity}`}>
      <div class="bar" />
      <div class="tx">
        <b>{it.title}</b>
        {it.facts && it.facts.length > 0 && (
          <div class="m">{it.facts.map((f) => <span key={f}>{f}</span>)}</div>
        )}
      </div>
      <div class="acts">{(it.actions ?? []).map((a) => <Btn key={a.label} a={a} nav={nav} />)}</div>
    </div>
  );
}

function VitalTile({ v, nav }: { v: Vital; nav: Nav }) {
  return (
    <button type="button" class={`vt ${v.tone ?? ""}`} onClick={() => v.drill && nav.act(v.drill)} title={v.drill?.label}>
      <span class="lbl">{v.label}</span>
      <div class="v">{v.value}<small>{v.unit}</small></div>
      <Spark series={v.series} baseline={v.baseline} />
      <span class="d">{[v.delta, v.note].filter(Boolean).join(" · ") || " "}</span>
    </button>
  );
}

export function StatusPage({ st, nav }: { st: Status; nav: Nav }) {
  const open = st.items.filter((i) => i.severity !== "resolved");
  const resolved = st.items.filter((i) => i.severity === "resolved");
  return (
    <div class="content">
      <Title title={st.headline} sub={st.summary} />
      {open.length === 0 ? (
        <div class="allclear"><span class="dot" />Nothing needs you right now.</div>
      ) : (
        <div class="att">
          {open.map((it) => <AttentionItem key={it.id} it={it} nav={nav} />)}
          {!nav.inline && resolved.map((it) => <AttentionItem key={it.id} it={it} nav={nav} />)}
        </div>
      )}
      <div>
        <div class="blockh"><span class="lbl">Vitals · 24h</span><span class="r">dashed line: same time last week</span></div>
        <div class="vitals">{st.vitals.map((v) => <VitalTile key={v.key} v={v} nav={nav} />)}</div>
      </div>
      {st.pools && st.pools.length > 0 && (
        <div>
          <div class="blockh"><span class="lbl">Worker pools</span><span class="r">busy of ready workers</span></div>
          <div class="pools">
            {st.pools.map((p) => {
              const pct = p.ready ? (100 * p.busy) / p.ready : 0;
              return (
                <div class="pool" key={p.name}>
                  <span class="nm">{p.name}</span>
                  <div class="meter"><i class={pct >= 95 ? "w" : ""} style={{ width: `${pct}%` }} /></div>
                  <span class="n">{p.busy} / {p.ready}{p.ready < p.desired ? ` (wants ${p.desired})` : ""}</span>
                </div>
              );
            })}
          </div>
        </div>
      )}
      {(st.notes ?? []).map((n) => <p class="note" key={n}>{n}</p>)}
    </div>
  );
}

function stateLabel(a: Actor): string {
  if (a.state === "crashed") return a.reason ? `Crashed · ${a.reason === "Out of memory" ? "OOM" : a.reason}` : "Crashed";
  if (a.needsYou) return `Stuck ${a.state}`;
  return a.state[0].toUpperCase() + a.state.slice(1);
}

function StatePill({ a }: { a: Actor }) {
  return <span class={`st ${a.state}${a.needsYou && a.state !== "crashed" ? " stuck" : ""}`}>{stateLabel(a)}</span>;
}

function openActor(a: Actor): Action {
  return { label: "Open", tool: "get_actor", args: { atespace: a.atespace, name: a.name } };
}

export function ActorsPage({ list, nav }: { list: ActorList; nav: Nav }) {
  const groups: { name: string; rows: Actor[] }[] = [
    { name: "Needs you", rows: list.actors.filter((a) => a.needsYou) },
    { name: "Running", rows: list.actors.filter((a) => !a.needsYou && (a.state === "running" || ["resuming", "suspending", "reverting", "deleting"].includes(a.state))) },
    { name: "Suspended", rows: list.actors.filter((a) => !a.needsYou && a.state === "suspended") },
    { name: "Other", rows: list.actors.filter((a) => !a.needsYou && !["running", "resuming", "suspending", "reverting", "deleting", "suspended"].includes(a.state)) },
  ].filter((g) => g.rows.length);
  const c = list.counts;
  const scope = list.scope ? ` in ${list.scope}` : "";
  const sub = [`${plural(list.total, "actor")}${scope}`, c.running ? `${c.running} running` : "", c.suspended ? `${c.suspended} suspended` : "", c.needsYou ? `${c.needsYou} need you` : ""]
    .filter(Boolean).join(" · ");
  return (
    <div class="content">
      <Title title="Actors" sub={sub + (list.shown < list.total ? ` · showing ${list.shown}` : "")} />
      <div class="ot">
        <table>
          <thead><tr><th>Actor</th><th>State</th><th>Template</th><th>Worker</th><th>Last active</th><th>Memory</th></tr></thead>
          <tbody>
            {groups.map((g) => [
              <tr class="grp" key={g.name}><td colSpan={6}><span class="lbl">{g.name} · {g.rows.length}</span></td></tr>,
              ...g.rows.map((a) => (
                <tr class="row" key={a.atespace + a.name} tabIndex={0} onClick={() => nav.act(openActor(a))}
                  onKeyDown={(e) => e.key === "Enter" && nav.act(openActor(a))}>
                  <td>{list.scope ? a.name : `${a.atespace}/${a.name}`}</td>
                  <td><StatePill a={a} /></td>
                  <td>{a.template}</td>
                  <td class="dim">{shortWorker(a.worker, a.pool)}</td>
                  <td class="dim" title={absolute(a.lastActive)}>{ago(a.lastActive)}</td>
                  <td>{a.memoryBytes ? bytes(a.memoryBytes) : a.snapshots?.[0]?.sizeBytes ? <span class="dim">snapshot {bytes(a.snapshots[0].sizeBytes)}</span> : <span class="dim">–</span>}</td>
                </tr>
              )),
            ])}
          </tbody>
        </table>
      </div>
      {list.hint && (
        <div class="hintbox"><span>{list.hint}</span>
          <div class="acts"><button type="button" class="btn" onClick={() => nav.ask(`${list.hint} Why, and what should I change?`)}>Ask why</button></div>
        </div>
      )}
    </div>
  );
}

export function ActorPage({ d, nav }: { d: ActorDetail; nav: Nav }) {
  const a = d.actor;
  const ref = `${a.atespace}/${a.name}`;
  const crashed = a.state === "crashed";
  const since = a.crash?.time ?? a.stateSince;
  const askText = crashed ? `Why did ${ref} crash, and what should I do about it?` : `What is ${ref} doing, and is anything wrong with it?`;
  const snap = a.snapshots?.[0];
  return (
    <div class="detail">
      <div class="content">
        <div class="pagetitle">
          <div>
            <h1 class="mono" style={{ fontFamily: "var(--mono)", fontSize: nav.inline ? "15px" : "19px", letterSpacing: "-.02em" }}>{a.name}</h1>
            <div class="sub"><StatePill a={a} /> <span class="dim" title={absolute(since)}>{ago(since)}</span></div>
          </div>
          <span class="sp" />
          <div class="acts">
            <button type="button" class="btn" onClick={() => nav.ask(askText)}>Ask why</button>
            {d.actions.map((x) => <Btn key={x.label} a={x} nav={nav} />)}
          </div>
        </div>
        {a.crash && (
          <div class="crashbox"><b>{a.reason || "Crashed"}</b><pre>{a.crash.message}</pre></div>
        )}
        {d.hint && <div class="hintbox"><span>{d.hint}</span></div>}
        {d.timeline.length > 0 && (
          <div>
            <div class="blockh"><span class="lbl">Timeline</span></div>
            <div class="tl">
              {d.timeline.map((e, i) => (
                <div class={`ev ${e.state}`} key={i}>
                  <span class="p" />
                  <span class="w"><b>{e.state[0].toUpperCase() + e.state.slice(1)}</b>{e.detail && <span class="mono"> · {e.detail}</span>}</span>
                  <span class="t" title={absolute(e.time)}>{clock(e.time)}</span>
                </div>
              ))}
            </div>
          </div>
        )}
        {d.logs.length > 0 && (
          <div>
            <div class="blockh"><span class="lbl">Logs</span><span class="r">last {d.logs.length} lines</span></div>
            <div class="logs"><pre>{d.logs.map((l, i) => (
              <span key={i}><span class="ts">{clock(l.time)}</span> <span class={l.error ? "e" : ""}>{l.source.padEnd(8)} {l.text}</span>{"\n"}</span>
            ))}</pre></div>
          </div>
        )}
      </div>
      <aside class="props">
        <Prop k="Atespace" v={a.atespace} />
        <Prop k="Template" v={a.template} mono />
        {d.template?.memoryLimitBytes ? <Prop k="Memory" v={`${a.memoryBytes ? bytes(a.memoryBytes) + " of " : "limit "}${bytes(d.template.memoryLimitBytes)}`} /> : null}
        <Prop k={a.worker && a.state !== "running" ? "Last worker" : "Worker"} v={a.worker ?? "–"} mono />
        {a.sandboxClass && <Prop k="Sandbox" v={a.sandboxClass} />}
        <div class="hr" />
        <Prop k="Snapshot" v={snap ? `gen ${snap.generation} · ${snap.fidelity}` : "none"} />
        {snap && <Prop k="Stored" v={`${snap.storage}${snap.sizeBytes ? " · " + bytes(snap.sizeBytes) : ""}`} />}
        <div class="hr" />
        <Prop k="Created" v={ago(a.createdAt)} />
        {a.node && <Prop k="Node" v={a.node} mono />}
        {a.uid && <Prop k="UID" v={a.uid} mono />}
      </aside>
    </div>
  );
}

function Prop({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return <div class="prop"><span class="k">{k}</span><span class={`v${mono ? " mono" : ""}`} title={v}>{v}</span></div>;
}

export function MetricPage({ m }: { m: MetricResult }) {
  return (
    <div class="content">
      <Title title={m.title} sub={`last ${m.window}`} />
      <LineChart series={m.series} unit={m.unit} />
    </div>
  );
}

export function ActionPage({ r, nav, onConfirm }: { r: ActionResult; nav: Nav; onConfirm?: () => void }) {
  return (
    <div class="content">
      <Confirm r={r} onConfirm={onConfirm} onCancel={undefined} />
      {!r.dryRun && <p class="note">The cluster view refreshes on the next poll.</p>}
      {nav.inline && !r.dryRun && (
        <div class="acts" style={{ justifyContent: "flex-start" }}>
          <button type="button" class="btn" onClick={() => nav.act({ label: "Status", tool: "status", args: {} })}>Show status</button>
        </div>
      )}
    </div>
  );
}

export function Confirm({ r, onConfirm, onCancel, busy }: { r: ActionResult; onConfirm?: () => void; onCancel?: () => void; busy?: boolean }) {
  const ok = r.targets.filter((t) => t.ok).length;
  const destructive = r.verb === "delete" || r.verb === "revert" || r.verb === "drain";
  return (
    <div class="confirm" role={r.dryRun ? "dialog" : undefined} aria-label={r.dryRun ? `Confirm ${r.verb}` : undefined}>
      <h2>{r.summary}</h2>
      <ul>
        {r.targets.map((t) => (
          <li key={t.atespace + t.name}>
            <span class={`st ${t.state}`} />
            <span class="mono">{t.atespace}/{t.name}</span>
            <span class="why">{t.ok ? (r.dryRun ? "will change" : "done") : t.reason}</span>
          </li>
        ))}
      </ul>
      {r.verb === "revert" && r.dryRun && ok > 0 && <p class="note">Reverting discards everything since each actor's latest snapshot.</p>}
      {r.verb === "delete" && r.dryRun && ok > 0 && <p class="note">Deleting removes the actors and their snapshots. It cannot be undone.</p>}
      {r.dryRun && (onConfirm || onCancel) && (
        <div class="acts">
          {onConfirm && ok > 0 && (
            <button type="button" class={`btn ${destructive ? "danger" : "pri"}`} disabled={busy} onClick={onConfirm}>
              {r.verb[0].toUpperCase() + r.verb.slice(1)} {ok === 1 ? "1 actor" : `${ok} actors`}
            </button>
          )}
          {onCancel && <button type="button" class="btn" onClick={onCancel}>Cancel</button>}
        </div>
      )}
    </div>
  );
}
