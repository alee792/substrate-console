// Mirrors the structuredContent the Go tools return (internal/mcpapp,
// internal/rules, internal/model). Keep in step with those packages.

export type ActorState =
  | "resuming" | "running" | "suspending" | "suspended"
  | "crashed" | "deleting" | "reverting" | "unknown" | "created";

export interface ActorRef { atespace: string; name: string }

export interface Snapshot { generation: number; fidelity: string; storage: string; sizeBytes?: number }

export interface Actor {
  atespace: string; name: string; uid?: string; state: ActorState; stateSince?: string;
  template: string; worker?: string; pool?: string; node?: string; sandboxClass?: string;
  crash?: { message: string; time: string }; snapshots?: Snapshot[];
  lastActive?: string; createdAt?: string; memoryBytes?: number; memoryLimitBytes?: number; resumeP95Ms?: number;
  needsYou?: boolean; reason?: string;
}

export interface Pool { namespace: string; name: string; sandboxClass?: string; desired: number; ready: number; busy: number }

export interface Container { name: string; image: string; command?: string[]; args?: string[]; envNames?: string[]; probe?: string }
export interface Golden { state: "ready" | "building" | "failed" | "off"; tag?: string; time?: string; error?: string }
export interface Template {
  atespace: string; name: string; uid?: string; createdAt?: string; containers?: Container[]; image?: string;
  cpuMillicores?: number; memoryLimitBytes?: number; workerSelector?: Record<string, string>; fidelity?: string; golden: Golden;
}
export interface TemplateRow extends Template {
  family: string; version?: string; sandboxClass?: string; pools: string[];
  actors: number; running: number; suspended: number; crashed: number; resumeP95Ms?: number;
}
export interface TemplateList { kind: "templates"; scope: string; templates: TemplateRow[] }
export interface TemplateDetail {
  kind: "template"; template: TemplateRow; versions: TemplateRow[]; actors: Actor[];
  latency?: MetricResult; hint?: string; actions: Action[];
}
export interface TemplateSpec {
  atespace: string; name?: string; image?: string; command?: string[]; args?: string[]; env?: Record<string, string>;
  cpuMillicores?: number; memoryMiB?: number; pool?: string; fidelity?: string; golden?: boolean; from?: string;
}
export interface Draft {
  kind: "draft"; spec: TemplateSpec; problems: Record<string, string>; summary: string;
  atespaces: string[]; pools: { name: string; sandboxClass?: string; pinnable: boolean }[]; carried?: string[];
}
export interface Point { t: string; v: number }
export interface Series { label: string; points: Point[] }

export interface Action {
  label: string; tool: string; args: Record<string, unknown>; primary?: boolean; destructive?: boolean;
}

export interface Item {
  id: string; severity: "critical" | "warning" | "resolved"; rule: string; title: string;
  facts?: string[]; actors?: ActorRef[]; since?: string; actions?: Action[];
}

export interface Vital {
  key: string; label: string; value: string; unit?: string; delta?: string; tone?: "" | "warn" | "crit";
  note?: string; series?: Point[]; baseline?: Point[]; drill?: Action;
}

export interface Status {
  kind: "status"; cluster: string; version?: string; scope: string; headline: string; summary: string;
  items: Item[]; vitals: Vital[]; pools?: Pool[];
  atespaces: { name: string; actors: number; problems: number }[];
  counts: Record<string, number>; generatedAt: string; notes?: string[];
  access?: Access;
  /** The Canopy agents come from, when the console has one. */
  canopy?: string;
}

/** Who the console read the cluster as. */
export interface Access { principal?: string; shared?: boolean; allAtespaces: boolean; workers: boolean }

export interface ActorList {
  kind: "actors"; scope: string; filter: Record<string, unknown>; total: number; shown: number;
  counts: Record<string, number>; actors: Actor[]; hint?: string;
}

export interface ActorDetail {
  kind: "actor"; actor: Actor; template?: Template;
  timeline: { time: string; state: ActorState; detail?: string }[];
  logs: { time: string; source: string; text: string; error?: boolean }[];
  /** Logs start here: when this actor started on its worker. */
  logsSince?: string;
  hint?: string; actions: Action[];
}

export interface MetricResult {
  kind: "metric"; metric: string; title: string; unit: string; by?: string; window: string; series: Series[];
}

export interface ActionResult {
  kind: "action"; verb: string; tool: string; dryRun: boolean; summary: string;
  targets: (ActorRef & { state: ActorState; ok: boolean; reason?: string })[];
  args: Record<string, unknown>; note?: string;
}

// ---------- agents (internal/agents, internal/mcpapp/agents.go) ----------

export type Lab = "anthropic" | "openai";

export interface Source { url: string; ref?: string; path?: string; commit?: string }

export interface Agent {
  id: string; name: string; description?: string; lab: Lab; harness: string; model: string; version: number;
  source?: Source; createdAt: string; updatedAt: string; sessions: number; active: number; lastActive?: string;
}

export interface AgentVersion {
  version: number; origin: "git" | "api"; source?: Source; createdAt: string;
  contents?: { instructions: boolean; skills: string[]; mcpServers: string[]; subagents: string[] };
  bundle?: string; bundleSize?: number;
}

export interface SessionRow {
  id: string; agentId?: string; status: SessionStatus; envStatus: string; harness: string; model: string;
  createdAt: string; lastActive: string; spendCents: number; preview?: string;
}

export type SessionStatus = "idle" | "in_progress" | "requires_action" | "failed";

export interface AgentList { kind: "agents"; canopy: string; agents: Agent[] }

export interface AgentPage {
  kind: "agent"; agent: Agent; versions: AgentVersion[]; shown: number; sessions: SessionRow[];
  definition?: unknown; definitionNote?: string; actions: Action[];
}

export interface Environment { id: string; name: string; network: string; hosts?: string[]; packageManagers?: boolean; mcpServers?: boolean }

export interface Playground {
  kind: "playground"; agent: Agent; versions: number[]; environments: Environment[];
  version?: number; environment?: string; network?: string; allowed_domains?: string[];
  repository?: string; branch?: string; message?: string; problems: Record<string, string>;
}

export interface StartArgs {
  agent: string; version?: number; environment?: string; network?: string; allowed_domains?: string[];
  repository?: string; branch?: string; message: string;
}

export interface TItem {
  id: string; kind: string; status: string; text?: string; command?: string; output?: string; exitCode?: number;
  name?: string; server?: string; args?: string; changes?: { path: string; kind: string }[]; error?: string; query?: string;
  decision?: string;
}

export interface Turn {
  id: string; status: "queued" | "running" | "completed" | "failed" | "cancelled"; input?: string;
  started?: string; ended?: string; items: TItem[]; error?: string;
  usage?: { input: number; cached: number; output: number }; dropped?: number;
}

export interface Approval { eventId: string; tool: string; server?: string; command?: string; input?: string }

export interface SessionInfo {
  id: string; agentId?: string; lab: Lab; harness: string; model: string; status: SessionStatus; error?: string;
  threadId?: string; createdAt: string; lastActive: string; spendCents: number; spendLimitCents?: number;
  computeSeconds: number; usage: { input: number; cached: number; output: number };
  sandbox: {
    status: string; error?: string; network: string; provider?: string; actor?: string; atespace?: string;
    template?: string; node?: string; image?: string; size?: string; egress?: string[];
  };
  metadata?: Record<string, string>;
}

export interface SessionPage {
  kind: "session"; session: SessionInfo; agent?: Agent;
  transcript: { turns: Turn[]; waiting: unknown[]; earlier?: number; cursor: number };
  approvals: Approval[]; blocked?: string; actions: Action[]; done?: string;
}

export type Result = Status | ActorList | ActorDetail | MetricResult | ActionResult | TemplateList | TemplateDetail | Draft
  | AgentList | AgentPage | Playground | SessionPage;
