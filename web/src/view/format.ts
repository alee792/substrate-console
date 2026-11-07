export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "–";
  const d = (now - Date.parse(iso)) / 1000;
  if (d < 5) return "now";
  if (d < 60) return `${Math.floor(d)}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 172800) return `${Math.floor(d / 3600)}h ago`;
  return `${Math.floor(d / 86400)}d ago`;
}

/** Time of day, with the date in front when it isn't today. */
export function clock(iso: string, withSeconds = true): string {
  const t = new Date(iso);
  const time = t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", ...(withSeconds ? { second: "2-digit" } : {}), hour12: false });
  if (new Date().toDateString() === t.toDateString()) return time;
  return `${t.toLocaleDateString([], { month: "short", day: "numeric" })} ${time}`;
}

export function absolute(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : "";
}

export function bytes(n?: number): string {
  if (!n) return "–";
  if (n >= 2 ** 30) {
    const v = n / 2 ** 30;
    return `${Number.isInteger(v) ? v : v.toFixed(1)} GiB`;
  }
  return `${Math.round(n / 2 ** 20)} MiB`;
}

export function plural(n: number, one: string, many = one + "s"): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

export function shortWorker(worker?: string, pool?: string): string {
  if (!worker) return "–";
  return pool && worker.startsWith(pool + "/") ? worker.slice(pool.length + 1) : worker;
}

/** Round a max up to a tidy axis value. */
export function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}
