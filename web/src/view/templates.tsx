import { useState } from "preact/hooks";
import type { Action, Draft, Golden, TemplateDetail, TemplateList, TemplateRow, TemplateSpec } from "./types";
import { absolute, ago, bytes, plural } from "./format";
import { LineChart } from "./charts";
import { Btn, Hero, Sec, StatePill, type Nav } from "./pages";

const cores = (m?: number) => (m ? `${+(m / 1000).toFixed(2)} CPU` : "–");
const size = (t: { cpuMillicores?: number; memoryLimitBytes?: number }) => [cores(t.cpuMillicores), t.memoryLimitBytes ? bytes(t.memoryLimitBytes) : ""].filter(Boolean).join(" · ");

const GOLDEN: Record<Golden["state"], string> = { ready: "Golden ready", building: "Building golden", failed: "Golden failed", off: "No golden" };

export function GoldenPill({ g }: { g: Golden }) {
  return <span class={`golden ${g.state}`} title={g.error || (g.time ? absolute(g.time) : "")}>{GOLDEN[g.state]}</span>;
}

function open(t: { atespace: string; name: string }): Action {
  return { label: "Open", tool: "get_template", args: { atespace: t.atespace, name: t.name } };
}

export function TemplatesPage({ list, nav }: { list: TemplateList; nav: Nav }) {
  const spaces = [...new Set(list.templates.map((t) => t.atespace))];
  const newT: Action = { label: "New template", tool: "create_template", args: list.scope ? { atespace: list.scope } : {} };
  return (
    <div class="content">
      <Hero title="Templates" sub={plural(list.templates.length, "template")}>
        {!nav.readOnly && <div class="acts"><Btn a={{ ...newT, primary: true }} nav={nav} /></div>}
      </Hero>
      {list.templates.length === 0 && <p class="note">No templates yet.</p>}
      {spaces.map((sp) => (
        <section key={sp}>
          {(!list.scope || spaces.length > 1) && <div class="sec"><h2>{sp}</h2></div>}
          <div class="tlist">
            {list.templates.filter((t) => t.atespace === sp).map((t) => <TemplateRowView key={t.name} t={t} nav={nav} />)}
          </div>
        </section>
      ))}
    </div>
  );
}

function TemplateRowView({ t, nav }: { t: TemplateRow; nav: Nav }) {
  return (
    <div class="trow cursor-interaction" role="button" tabIndex={0} onClick={() => nav.act(open(t))} onKeyDown={(e) => e.key === "Enter" && nav.act(open(t))}>
      <div class="nm">
        <b class="mono">{t.name}</b>
        <span>{t.image}</span>
      </div>
      <span class="opt dim">{size(t)}{t.sandboxClass ? ` · ${t.sandboxClass}` : ""}</span>
      <GoldenPill g={t.golden} />
      <span class="num ct">{t.actors.toLocaleString()} actors{t.crashed > 0 && <em> · {t.crashed} crashed</em>}</span>
      <span class="opt num dim p95">{t.resumeP95Ms ? `${Math.round(t.resumeP95Ms)} ms` : ""}</span>
    </div>
  );
}

export function TemplatePage({ d, nav }: { d: TemplateDetail; nav: Nav }) {
  const t = d.template;
  const c = t.containers?.[0];
  const ordered = [...d.actions].sort((x, y) => Number(!!x.primary) - Number(!!y.primary));
  const others = d.versions.filter((v) => v.name !== t.name);
  return (
    <div class="content">
      <Hero title={t.name} mono sub={<><GoldenPill g={t.golden} /><span>{size(t)}</span>{t.sandboxClass && <span>{t.sandboxClass}</span>}<span title={absolute(t.createdAt)}>created {ago(t.createdAt)}</span></>}>
        <div class="acts">
          <button type="button" class="btn quiet cursor-interaction" onClick={() => nav.ask(`How is the ${t.name} template doing, and should I change anything in the next version?`)}>Ask</button>
          {ordered.map((x) => <Btn key={x.label} a={x} nav={nav} />)}
        </div>
      </Hero>
      <div class="detail">
        <div class="stack">
          {t.golden.state === "failed" && (
            <div class="cause">
              <div class="h"><b>Golden snapshot failed</b></div>
              <p>New actors from this template can't start from a warm snapshot, and the controller won't retry. Fix the cause and create a new version.</p>
              <pre>{t.golden.error}</pre>
            </div>
          )}
          {d.hint && <p class="lead">{d.hint}</p>}
          {others.length > 0 && (
            <Sec title="Versions">
              <div class="tblwrap">
                <table class="tbl">
                  <thead><tr><th>Version</th><th>Created</th><th>Size</th><th>Actors</th><th>Crashed</th><th>Resume p95</th></tr></thead>
                  <tbody>
                    {d.versions.map((v) => (
                      <tr key={v.name} class={`row${v.name === t.name ? " cur" : ""}`} onClick={() => v.name !== t.name && nav.act(open(v))}>
                        <td class="id">{v.name}</td>
                        <td class="dim">{ago(v.createdAt)}</td>
                        <td>{size(v)}</td>
                        <td class="num">{v.actors}</td>
                        <td class={`num${v.crashed ? " crit" : " dim"}`}>{v.crashed}</td>
                        <td class="num">{v.resumeP95Ms ? `${Math.round(v.resumeP95Ms)} ms` : "–"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </Sec>
          )}
          {d.latency && (
            <Sec title="Resume p95" right="Last 24 h, all versions">
              <LineChart series={d.latency.series} unit="ms" />
            </Sec>
          )}
          <Sec title="Actors" right={t.actors > d.actors.length ? `${d.actors.length} of ${t.actors.toLocaleString()}` : undefined}>
            {d.actors.length === 0 ? <p class="note">No actors yet.</p> : (
              <div class="tblwrap">
                <table class="tbl">
                  <tbody>
                    {d.actors.map((a) => (
                      <tr class="row" key={a.name} onClick={() => nav.act({ label: "Open", tool: "get_actor", args: { atespace: a.atespace, name: a.name } })}>
                        <td class="id">{a.name}</td>
                        <td><StatePill a={a} /></td>
                        <td class="dim" title={absolute(a.lastActive)}>{ago(a.lastActive ?? a.createdAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {t.actors > d.actors.length && (
              <button type="button" class="more cursor-interaction" style={{ marginTop: 12 }}
                onClick={() => nav.act({ label: "All", tool: "list_actors", args: { atespace: t.atespace, template: t.name } })}>All actors →</button>
            )}
          </Sec>
        </div>
        <aside class="props">
          <div class="prop"><div class="k">Image</div><div class="v mono wrap">{t.image}</div></div>
          {c?.command && <div class="prop"><div class="k">Command</div><div class="v mono wrap">{[...(c.command ?? []), ...(c.args ?? [])].join(" ")}</div></div>}
          {c?.envNames && c.envNames.length > 0 && <div class="prop"><div class="k">Environment</div><div class="v mono wrap">{c.envNames.join(", ")}</div></div>}
          {c?.probe && <div class="prop"><div class="k">Wakeup probe</div><div class="v mono">{c.probe}</div></div>}
          {(t.containers?.length ?? 0) > 1 && <div class="prop"><div class="k">Containers</div><div class="v">{t.containers!.map((x) => x.name).join(", ")}</div></div>}
          <div class="hr" />
          <div class="prop"><div class="k">Runs on</div><div class="v">{t.pools.length ? t.pools.join(", ") : "No matching pool"}</div></div>
          <div class="prop"><div class="k">Snapshots</div><div class="v">{t.fidelity ? `${t.fidelity[0].toUpperCase()}${t.fidelity.slice(1)}` : "Default"}</div></div>
          {t.golden.tag && <div class="prop"><div class="k">Golden tag</div><div class="v mono">{t.golden.tag}</div></div>}
          <div class="hr" />
          <div class="prop"><div class="k">Atespace</div><div class="v">{t.atespace}</div></div>
          {t.uid && <div class="prop"><div class="k">UID</div><div class="v mono">{t.uid}</div></div>}
        </aside>
      </div>
    </div>
  );
}

const MEMORY = [512, 1024, 2048, 4096, 8192, 16384];
const mib = (n: number) => (n >= 1024 ? `${n / 1024} GiB` : `${n} MiB`);

/** The template creator. The server fills and checks the draft; this form
 * edits it and submits it with dry_run=false. */
export function DraftPage({ d, nav, submit }: { d: Draft; nav: Nav; submit: (spec: TemplateSpec) => Promise<void> }) {
  const [spec, setSpec] = useState<TemplateSpec>(d.spec);
  const [cmd, setCmd] = useState([...(d.spec.command ?? []), ...(d.spec.args ?? [])].join(" "));
  const [busy, setBusy] = useState(false);
  const p = d.problems ?? {};
  const set = (k: keyof TemplateSpec, v: unknown) => setSpec((s) => ({ ...s, [k]: v }));
  const memOpts = MEMORY.includes(spec.memoryMiB ?? 0) || !spec.memoryMiB ? MEMORY : [...MEMORY, spec.memoryMiB].sort((a, b) => a - b);
  const pools = d.pools.filter((x) => x.pinnable);

  // No <form> submit: hosts sandbox the view without allow-forms, which
  // blocks submission before any handler runs.
  async function go() {
    setBusy(true);
    const words = cmd.trim() ? cmd.trim().split(/\s+/) : undefined;
    try { await submit({ ...spec, command: words, args: [] }); } finally { setBusy(false); }
  }

  return (
    <div class="content">
      <Hero title={d.summary} sub={spec.from ? "Templates can't be edited, so a change is a new version. Existing actors keep using the old one." : "What actors run, how big they are, and where."} />
      <div class="form" onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLElement).tagName === "INPUT" && (e.target as HTMLInputElement).type !== "checkbox" && go()}>
        <Field label="Atespace" err={p.atespace}>
          <select value={spec.atespace} disabled={!!spec.from} onChange={(e) => set("atespace", (e.target as HTMLSelectElement).value)}>
            <option value="" disabled>Choose…</option>
            {d.atespaces.map((a) => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Name" err={p.name} hint={spec.from ? `Next version of ${spec.from}` : "Add -v1 so later versions sort together"}>
          <input class="mono" value={spec.name ?? ""} spellcheck={false} autocomplete="off" onInput={(e) => set("name", (e.target as HTMLInputElement).value)} />
        </Field>
        <Field label="Image" err={p.image} hint="Pin by digest so every actor runs the same bits">
          <input class="mono" value={spec.image ?? ""} spellcheck={false} placeholder="ghcr.io/org/agent@sha256:…" onInput={(e) => set("image", (e.target as HTMLInputElement).value)} />
        </Field>
        <Field label="Command" hint="Optional. Leave empty to use the image's entrypoint">
          <input class="mono" value={cmd} spellcheck={false} placeholder="node server.js" onInput={(e) => setCmd((e.target as HTMLInputElement).value)} />
        </Field>
        <div class="row2">
          <Field label="CPU" err={p.cpuMillicores}>
            <div class="unit"><input type="number" min="0.25" step="0.25" value={(spec.cpuMillicores ?? 0) / 1000} onInput={(e) => set("cpuMillicores", Math.round(+(e.target as HTMLInputElement).value * 1000))} /><span>cores</span></div>
          </Field>
          <Field label="Memory" err={p.memoryMiB} hint="The sandbox OOM-kills above this">
            <select value={spec.memoryMiB} onChange={(e) => set("memoryMiB", +(e.target as HTMLSelectElement).value)}>
              {memOpts.map((m) => <option key={m} value={m}>{mib(m!)}</option>)}
            </select>
          </Field>
        </div>
        <Field label="Runs on" err={p.pool} hint="The pool sets the sandbox class">
          <div class="seg">
            <label class={!spec.pool ? "on" : ""}><input type="radio" name="pool" checked={!spec.pool} onChange={() => set("pool", "")} />Any pool</label>
            {pools.map((x) => (
              <label key={x.name} class={spec.pool === x.name ? "on" : ""}>
                <input type="radio" name="pool" checked={spec.pool === x.name} onChange={() => set("pool", x.name)} />{x.name}{x.sandboxClass && <span>{x.sandboxClass}</span>}
              </label>
            ))}
          </div>
        </Field>
        <Field label="Snapshots" err={p.fidelity} hint={FIDELITY[spec.fidelity || "memory"]}>
          <div class="seg">
            {["memory", "rootfs", "volumes"].map((f) => (
              <label key={f} class={(spec.fidelity || "memory") === f ? "on" : ""}>
                <input type="radio" name="fid" checked={(spec.fidelity || "memory") === f} onChange={() => set("fidelity", f)} />{f[0].toUpperCase() + f.slice(1)}
              </label>
            ))}
          </div>
        </Field>
        <label class="check">
          <input type="checkbox" checked={spec.golden !== false} onChange={(e) => set("golden", (e.target as HTMLInputElement).checked)} />
          <span><b>Golden snapshot</b>Boot once, warm up, snapshot. New actors start from that instead of a cold boot.</span>
        </label>
        {d.carried && d.carried.length > 0 && <p class="note">Kept from {spec.from}: {d.carried.join(", ")}.</p>}
        <div class="acts" style={{ justifyContent: "flex-end" }}>
          <button type="button" class="btn quiet cursor-interaction" onClick={nav.back}>Cancel</button>
          <button type="button" class="btn pri cursor-interaction" disabled={busy || nav.readOnly} onClick={go}>{busy ? "Creating…" : "Create template"}</button>
        </div>
      </div>
    </div>
  );
}

const FIDELITY: Record<string, string> = {
  memory: "Full memory: actors resume exactly where they were",
  rootfs: "Filesystem only: processes restart, files survive",
  volumes: "Durable volumes only: everything else starts fresh",
};

function Field({ label, err, hint, children }: { label: string; err?: string; hint?: string; children: preact.ComponentChildren }) {
  return (
    <label class={`field${err ? " bad" : ""}`}>
      <span class="l">{label}</span>
      {children}
      {(err || hint) && <span class="h">{err || hint}</span>}
    </label>
  );
}
