import { useEffect, useRef, useState } from "preact/hooks";
import type { Point, Series } from "./types";
import { clock, niceMax } from "./format";

/** A 24h sparkline with an optional dashed baseline (same time last week). */
export function Spark({ series, baseline }: { series?: Point[]; baseline?: Point[] }) {
  if (!series || series.length < 2) return <svg class="spark" aria-hidden="true" />;
  const all = [...series, ...(baseline ?? [])].map((p) => p.v);
  let lo = Math.min(...all);
  let hi = Math.max(...all);
  if (hi - lo < 1e-9) { hi += 1; lo -= 1; }
  const pad = (hi - lo) * 0.12;
  lo -= pad; hi += pad;
  const W = 120, H = 30;
  const path = (pts: Point[]) =>
    pts.map((p, i) => `${i ? "L" : "M"}${((i / (pts.length - 1)) * W).toFixed(1)} ${(H - ((p.v - lo) / (hi - lo)) * H).toFixed(1)}`).join(" ");
  const d = path(series);
  return (
    <svg class="spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" aria-hidden="true">
      {baseline && baseline.length > 1 && <path class="base" d={path(baseline)} />}
      <path class="ar" d={`${d} L${W} ${H} L0 ${H}Z`} />
      <path class="ln" d={d} />
    </svg>
  );
}

const STYLES = [
  { stroke: "var(--fg)", dash: "" },
  { stroke: "var(--muted)", dash: "4 3" },
  { stroke: "var(--ok)", dash: "" },
  { stroke: "var(--faint)", dash: "1 3" },
  { stroke: "var(--crit)", dash: "" },
];

/** A line chart drawn to one scale. The series whose last value is highest
 * and that grew most is drawn in the warning color so it reads first. */
export function LineChart({ series, unit }: { series: Series[]; unit: string }) {
  // Draw at the container's real width so text stays at its CSS size.
  const box = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(560);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setW(Math.max(280, Math.round(e.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const shown = series.filter((s) => s.points.length > 1);
  if (!shown.length) return <p class="note">No data in this window.</p>;
  const H = Math.round(Math.min(260, Math.max(170, W * 0.32))), L = 52, R = 12, T = 12, B = 24;
  const max = niceMax(Math.max(...shown.flatMap((s) => s.points.map((p) => p.v))) * 1.05);
  const t0 = Date.parse(shown[0].points[0].t);
  const t1 = Date.parse(shown[0].points[shown[0].points.length - 1].t);
  const x = (t: string) => L + ((Date.parse(t) - t0) / Math.max(1, t1 - t0)) * (W - L - R);
  const y = (v: number) => T + (1 - v / max) * (H - T - B);
  const growth = (s: Series) => s.points[s.points.length - 1].v / Math.max(1e-9, s.points[0].v);
  let hot = -1;
  if (shown.length > 1) {
    const g = shown.map(growth);
    const i = g.indexOf(Math.max(...g));
    if (g[i] > 1.3) hot = i;
  }
  let k = 0;
  const styled = shown.map((s, i) => {
    const st = i === hot ? { stroke: "var(--warn)", dash: "" } : STYLES[k++ % STYLES.length];
    return { s, st };
  });
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
  const xt = [0, 0.5, 1].map((f) => new Date(t0 + f * (t1 - t0)).toISOString());
  const unitLabel = unit === "count" ? "" : ` ${unit}`;
  return (
    <div ref={box}>
      <svg class="chart" width={W} height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Line chart of ${shown.map((s) => s.label).join(", ")}`}>
        {ticks.map((v, i) => (
          <g key={i}>
            <line class="gr" x1={L} x2={W - R} y1={y(v)} y2={y(v)} />
            <text x={L - 6} y={y(v) + 3} text-anchor="end">{Math.round(v)}{i === 0 ? unitLabel : ""}</text>
          </g>
        ))}
        {xt.map((t, i) => (
          <text key={t} x={i === 0 ? L : i === 2 ? W - R : (L + W - R) / 2} y={H - 6} text-anchor={i === 0 ? "start" : i === 2 ? "end" : "middle"}>
            {clock(t, false)}
          </text>
        ))}
        {styled.map(({ s, st }) => (
          <path key={s.label} fill="none" stroke={st.stroke} stroke-width="1.5" stroke-dasharray={st.dash}
            d={s.points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)} ${y(p.v).toFixed(1)}`).join(" ")} />
        ))}
        {styled.map(({ s, st }) => {
          const p = s.points[s.points.length - 1];
          return <circle key={s.label + "end"} cx={x(p.t)} cy={y(p.v)} r="2.5" fill={st.stroke} />;
        })}
      </svg>
      <div class="legend">
        {styled.map(({ s, st }) => (
          <span key={s.label} style={{ color: st.stroke }}>
            <i style={{ borderTopStyle: st.dash ? "dashed" : "solid" }} />
            <span style={{ color: "var(--muted)" }}>{s.label} · {Math.round(s.points[s.points.length - 1].v)}{unitLabel}</span>
          </span>
        ))}
      </div>
    </div>
  );
}
