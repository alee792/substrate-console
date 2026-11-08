package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
	"github.com/alee792/substrate-console/internal/rules"
)

// ScopeIn selects the whole cluster or one atespace.
type ScopeIn struct {
	Atespace string `json:"atespace,omitempty" jsonschema:"limit to one atespace; omit for the whole cluster"`
}

// ListActorsIn filters list_actors.
type ListActorsIn struct {
	Atespace string `json:"atespace,omitempty" jsonschema:"limit to one atespace"`
	State    string `json:"state,omitempty" jsonschema:"one of running, suspended, resuming, suspending, crashed, deleting, reverting, or stuck (any transitional state held too long)"`
	Template string `json:"template,omitempty" jsonschema:"actor template name"`
	Worker   string `json:"worker,omitempty" jsonschema:"pool/worker, e.g. gvisor-pool-a/w-03"`
	IdleOver string `json:"idle_over,omitempty" jsonschema:"only actors idle longer than this, e.g. 7d or 12h"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum actors to return, default 50"`
}

// ActorIn names one actor.
type ActorIn struct {
	Atespace string `json:"atespace" jsonschema:"the actor's atespace"`
	Name     string `json:"name" jsonschema:"the actor's name"`
}

// LogsIn asks for an actor's logs.
type LogsIn struct {
	Atespace string `json:"atespace" jsonschema:"the actor's atespace"`
	Name     string `json:"name" jsonschema:"the actor's name"`
	Tail     int    `json:"tail,omitempty" jsonschema:"number of lines, default 50"`
}

// MetricIn asks for a chart.
type MetricIn struct {
	Metric   string `json:"metric" jsonschema:"one of resume_p95, resume_failure_rate, capacity, crashes, running_actors"`
	By       string `json:"by,omitempty" jsonschema:"split by template or pool; omit for one series"`
	Atespace string `json:"atespace,omitempty" jsonschema:"limit template-labelled metrics to one atespace"`
	Window   string `json:"window,omitempty" jsonschema:"1h, 6h, 24h or 7d; default 6h"`
}

// ActorList is list_actors' result.
type ActorList struct {
	Kind   string         `json:"kind"` // "actors"
	Scope  string         `json:"scope"`
	Filter ListActorsIn   `json:"filter"`
	Total  int            `json:"total"`
	Shown  int            `json:"shown"`
	Counts map[string]int `json:"counts"`
	Actors []ActorRow     `json:"actors"`
	Hint   string         `json:"hint,omitempty"`
}

// ActorRow is an actor plus whether it needs attention.
type ActorRow struct {
	model.Actor
	NeedsYou bool   `json:"needsYou,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ActorDetail is get_actor's result.
type ActorDetail struct {
	Kind     string          `json:"kind"` // "actor"
	Actor    ActorRow        `json:"actor"`
	Template *model.Template `json:"template,omitempty"`
	Timeline []model.Event   `json:"timeline"`
	Logs     []model.LogLine `json:"logs"`
	// LogsSince is where the logs start: this actor's time on its worker.
	LogsSince *time.Time     `json:"logsSince,omitempty"`
	Hint      string         `json:"hint,omitempty"`
	Actions   []rules.Action `json:"actions"`
}

// MetricResult is query_metric's result.
type MetricResult struct {
	Kind   string         `json:"kind"` // "metric"
	Metric string         `json:"metric"`
	Title  string         `json:"title"`
	Unit   string         `json:"unit"`
	By     string         `json:"by,omitempty"`
	Window string         `json:"window"`
	Series []model.Series `json:"series"`
}

var metricInfo = map[backend.Metric]struct{ title, unit string }{
	backend.MetricResumeP95:      {"Resume p95", "ms"},
	backend.MetricResumeFailures: {"Resume failures for lack of capacity", "%"},
	backend.MetricCapacity:       {"Worker capacity in use", "%"},
	backend.MetricCrashes:        {"Actor crashes", "count"},
	backend.MetricRunning:        {"Running actors", "count"},
}

func (s *Server) addReadTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "status",
		Description: "What needs attention right now, plus four vital signs (capacity, resume latency, density, crashes), for the whole cluster or one atespace. Start here.",
		Annotations: readOnly("Cluster status"),
		Meta:        toolMeta("Checking the cluster…", "Checked the cluster"),
	}, s.status)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_actors",
		Description: "List actors, with ones that need attention first. Filter by atespace, state (including 'stuck'), template, worker or idle time.",
		Annotations: readOnly("List actors"),
		Meta:        toolMeta("Listing actors…", "Listed actors"),
	}, s.listActors)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_actor",
		Description: "One actor in detail: state, crash reason, timeline, snapshots, worker, recent logs, and a hint when the console can see a likely cause.",
		Annotations: readOnly("Get actor"),
		Meta:        toolMeta("Reading actor…", "Read actor"),
	}, s.getActor)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "actor_logs",
		Description: "Recent log lines from an actor.",
		Annotations: readOnly("Actor logs"),
		Meta:        textToolMeta("Reading logs…", "Read logs"),
	}, s.actorLogs)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_worker_pools",
		Description: "Worker pools with desired, ready and busy workers, and any worker that is draining or unavailable.",
		Annotations: readOnly("List worker pools"),
		Meta:        textToolMeta("Listing worker pools…", "Listed worker pools"),
	}, s.listPools)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "query_metric",
		Description: "A chart of one metric over a window, optionally split by template or pool. Use this to answer ad-hoc questions instead of building dashboards. Needs Prometheus unless running with --demo.",
		Annotations: readOnly("Query metric"),
		Meta:        toolMeta("Querying metrics…", "Queried metrics"),
	}, s.queryMetric)
}

func (s *Server) status(ctx context.Context, _ *mcp.CallToolRequest, in ScopeIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	if in.Atespace != "" && !contains(c.Atespaces, in.Atespace) {
		if !c.Access.AllAtespaces {
			return nil, nil, fmt.Errorf("atespace %q doesn't exist or you can't read it; you can read: %s", in.Atespace, strings.Join(c.Atespaces, ", "))
		}
		return nil, nil, fmt.Errorf("atespace %q not found; known atespaces: %s", in.Atespace, strings.Join(c.Atespaces, ", "))
	}
	st := s.engineFor(ctx).Evaluate(ctx, c, in.Atespace, s.metricsFor(c))
	return &mcp.CallToolResult{Content: text(statusText(st))}, st, nil
}

func statusText(st rules.Status) string {
	var b strings.Builder
	scope := "cluster " + st.Cluster
	if st.Scope != "" {
		scope = "atespace " + st.Scope + " on " + st.Cluster
	}
	fmt.Fprintf(&b, "%s: %s. %s.\n", scope, st.Headline, st.Summary)
	for _, it := range st.Items {
		if it.Severity == rules.Resolved {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s", it.Severity, it.Title)
		if len(it.Facts) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(it.Facts, "; "))
		}
		if len(it.Actions) > 0 {
			var acts []string
			for _, a := range it.Actions {
				acts = append(acts, a.Tool)
			}
			fmt.Fprintf(&b, " → %s", strings.Join(acts, " or "))
		}
		b.WriteString("\n")
	}
	b.WriteString("Vitals:")
	for _, v := range st.Vitals {
		fmt.Fprintf(&b, " %s %s", v.Label, strings.TrimSpace(v.Value+" "+v.Unit))
		if v.Note != "" {
			fmt.Fprintf(&b, " (%s)", v.Note)
		}
		b.WriteString(";")
	}
	for _, n := range st.Notes {
		b.WriteString("\nNote: " + n)
	}
	return b.String()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// needsYou says whether an actor belongs in the "needs you" group.
func needsYou(a model.Actor, now time.Time) (bool, string) {
	if a.State == model.StateCrashed {
		msg := ""
		if a.Crash != nil {
			msg = a.Crash.Message
		}
		return true, rules.CrashReason(msg)
	}
	if a.State.Transitional() && a.StateSince != nil {
		limit := map[model.ActorState]time.Duration{
			model.StateResuming: 2 * time.Minute, model.StateSuspending: 5 * time.Minute,
			model.StateReverting: 5 * time.Minute, model.StateDeleting: 10 * time.Minute,
		}[a.State]
		if d := now.Sub(*a.StateSince); d >= limit {
			return true, fmt.Sprintf("%s for %s", a.State, rules.Duration(d))
		}
	}
	return false, ""
}

func parseWindow(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q: use forms like 30m, 6h or 7d", s)
	}
	return d, nil
}

func stateRank(r ActorRow) int {
	switch {
	case r.NeedsYou:
		return 0
	case r.State == model.StateRunning || r.State.Transitional():
		return 1
	case r.State == model.StateSuspended:
		return 2
	}
	return 3
}

func (s *Server) listActors(ctx context.Context, _ *mcp.CallToolRequest, in ListActorsIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	idle, err := parseWindow(in.IdleOver, 0)
	if err != nil {
		return nil, nil, err
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	now := c.FetchedAt
	out := ActorList{Kind: "actors", Scope: in.Atespace, Filter: in, Counts: map[string]int{}, Actors: []ActorRow{}}
	var rows []ActorRow
	for _, a := range c.Actors {
		if in.Atespace != "" && a.Atespace != in.Atespace {
			continue
		}
		if in.Template != "" && a.Template != in.Template {
			continue
		}
		if in.Worker != "" && a.Worker != in.Worker {
			continue
		}
		ny, why := needsYou(a, now)
		if in.State == "stuck" {
			if !(ny && a.State.Transitional()) {
				continue
			}
		} else if in.State != "" && string(a.State) != in.State {
			continue
		}
		if idle > 0 && (a.LastActive == nil || now.Sub(*a.LastActive) < idle) {
			continue
		}
		out.Counts[string(a.State)]++
		if ny {
			out.Counts["needsYou"]++
		}
		rows = append(rows, ActorRow{Actor: a, NeedsYou: ny, Reason: why})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := stateRank(rows[i]), stateRank(rows[j])
		if ri != rj {
			return ri < rj
		}
		ti, tj := rows[i].LastActive, rows[j].LastActive
		if ti != nil && tj != nil && !ti.Equal(*tj) {
			return ti.After(*tj)
		}
		return rows[i].Name < rows[j].Name
	})
	out.Total = len(rows)
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
	}
	out.Actors = append(out.Actors, rows...)
	out.Shown = len(rows)
	out.Hint = templateHint(c, in.Atespace)

	var b strings.Builder
	fmt.Fprintf(&b, "%d actors match", out.Total)
	if out.Shown < out.Total {
		fmt.Fprintf(&b, " (showing %d)", out.Shown)
	}
	b.WriteString(".\n")
	for _, r := range out.Actors {
		fmt.Fprintf(&b, "- %s/%s %s template=%s", r.Atespace, r.Name, r.State, r.Template)
		if r.Worker != "" {
			fmt.Fprintf(&b, " worker=%s", r.Worker)
		}
		if r.Reason != "" {
			fmt.Fprintf(&b, " [needs you: %s]", r.Reason)
		}
		b.WriteString("\n")
	}
	if out.Hint != "" {
		b.WriteString("Hint: " + out.Hint)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, out, nil
}

// templateHint notices when crashes cluster on one template while a sibling
// template in the same atespace has none.
func templateHint(c *model.Cluster, atespace string) string {
	type agg struct{ total, crashed int }
	per := map[string]*agg{}
	for _, a := range c.Actors {
		if atespace != "" && a.Atespace != atespace {
			continue
		}
		k := a.Atespace + "/" + a.Template
		if per[k] == nil {
			per[k] = &agg{}
		}
		per[k].total++
		if a.State == model.StateCrashed {
			per[k].crashed++
		}
	}
	for k, v := range per {
		if v.crashed < 2 {
			continue
		}
		space, tmpl, _ := strings.Cut(k, "/")
		for k2, v2 := range per {
			space2, tmpl2, _ := strings.Cut(k2, "/")
			if space2 == space && tmpl2 != tmpl && v2.crashed == 0 && v2.total >= 5 {
				return fmt.Sprintf("Only %s is crashing in %s. %s has %d actors and no crashes.", tmpl, space, tmpl2, v2.total)
			}
		}
	}
	return ""
}

func findActor(c *model.Cluster, ref model.ActorRef) (model.Actor, bool) {
	for _, a := range c.Actors {
		if a.Atespace == ref.Atespace && a.Name == ref.Name {
			return a, true
		}
	}
	return model.Actor{}, false
}

func findTemplate(c *model.Cluster, atespace, name string) *model.Template {
	for _, t := range c.Templates {
		if t.Atespace == atespace && t.Name == name {
			t := t
			return &t
		}
	}
	return nil
}

func (s *Server) getActor(ctx context.Context, _ *mcp.CallToolRequest, in ActorIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	ref := model.ActorRef{Atespace: in.Atespace, Name: in.Name}
	a, ok := findActor(c, ref)
	if !ok {
		return nil, nil, fmt.Errorf("actor %s not found", ref)
	}
	ny, why := needsYou(a, c.FetchedAt)
	d := ActorDetail{Kind: "actor", Actor: ActorRow{Actor: a, NeedsYou: ny, Reason: why}, Template: findTemplate(c, a.Atespace, a.Template)}
	if d.Timeline, err = s.opts.Backend.Timeline(ctx, ref); err != nil {
		d.Timeline = nil
	}
	var since time.Time
	logsHidden := false
	if d.Logs, since, err = s.opts.Backend.Logs(ctx, ref, 5); err != nil {
		d.Logs = nil
		logsHidden = errors.Is(err, backend.ErrLogsNeedViewer)
	} else if !since.IsZero() {
		d.LogsSince = &since
	}
	if d.Timeline == nil {
		d.Timeline = []model.Event{}
	}
	if d.Logs == nil {
		d.Logs = []model.LogLine{}
	}
	d.Hint = actorHint(c, a, d.Template, d.Logs)
	if logsHidden && d.Hint == "" {
		d.Hint = "Logs need cluster-wide viewer access."
	}
	args := map[string]any{"actors": []map[string]string{{"atespace": a.Atespace, "name": a.Name}}}
	switch {
	case a.State == model.StateCrashed || (ny && a.State.Transitional()):
		label := "Revert"
		if len(a.Snapshots) > 0 {
			label = fmt.Sprintf("Revert to gen %d", a.Snapshots[0].Generation)
		}
		d.Actions = []rules.Action{
			{Label: "Delete", Tool: "delete_actors", Args: args, Destructive: true},
			{Label: label, Tool: "revert_actors", Args: args, Primary: true, Destructive: true},
		}
	case a.State == model.StateRunning:
		d.Actions = []rules.Action{{Label: "Suspend", Tool: "suspend_actors", Args: args}}
	case a.State == model.StateSuspended:
		d.Actions = []rules.Action{{Label: "Delete", Tool: "delete_actors", Args: args, Destructive: true}, {Label: "Resume", Tool: "resume_actors", Args: args, Primary: true}}
	default:
		d.Actions = []rules.Action{}
	}
	if s.opts.ReadOnly {
		d.Actions = []rules.Action{}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Actor %s is %s. Template %s", ref, a.State, a.Template)
	if d.Template != nil && d.Template.MemoryLimit > 0 {
		fmt.Fprintf(&b, " (memory limit %s)", bytes(d.Template.MemoryLimit))
	}
	b.WriteString(".")
	if a.Worker != "" {
		fmt.Fprintf(&b, " Worker %s.", a.Worker)
	}
	if a.Crash != nil {
		fmt.Fprintf(&b, "\nCrash at %s: %s", a.Crash.Time.Format(time.RFC3339), a.Crash.Message)
	}
	if len(a.Snapshots) > 0 {
		sn := a.Snapshots[0]
		fmt.Fprintf(&b, "\nLatest snapshot: gen %d, %s fidelity, %s.", sn.Generation, sn.Fidelity, sn.Storage)
	}
	if len(d.Timeline) > 0 {
		b.WriteString("\nTimeline (newest first):")
		for _, e := range d.Timeline {
			fmt.Fprintf(&b, "\n  %s %s %s", e.Time.Format("15:04:05"), e.State, e.Detail)
		}
	}
	if len(d.Logs) > 0 {
		b.WriteString("\nRecent logs:")
		for _, l := range d.Logs {
			fmt.Fprintf(&b, "\n  %s %s: %s", l.Time.Format("15:04:05"), l.Source, l.Text)
		}
	}
	if d.Hint != "" {
		b.WriteString("\nHint: " + d.Hint)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, d, nil
}

func actorHint(c *model.Cluster, a model.Actor, t *model.Template, logs []model.LogLine) string {
	var parts []string
	if a.Crash != nil && rules.CrashReason(a.Crash.Message) == "Out of memory" && t != nil && t.MemoryLimit > 0 {
		for _, l := range logs {
			if i := strings.Index(l.Text, "--max-old-space-size="); i >= 0 {
				var mb int64
				fmt.Sscanf(l.Text[i+len("--max-old-space-size="):], "%d", &mb)
				if mb<<20 > t.MemoryLimit {
					parts = append(parts, fmt.Sprintf("Node heap is set to %s; the sandbox allows %s.", bytes(mb<<20), bytes(t.MemoryLimit)))
				}
			}
		}
		if len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("Hit the %s memory limit.", bytes(t.MemoryLimit)))
		}
		for _, t2 := range c.Templates {
			if t2.Atespace == a.Atespace && t2.Name != t.Name && t2.MemoryLimit > t.MemoryLimit {
				parts = append(parts, fmt.Sprintf("A revert keeps the same limit. %s allows %s.", t2.Name, bytes(t2.MemoryLimit)))
				break
			}
		}
	}
	if h := templateHint(c, a.Atespace); h != "" && a.State == model.StateCrashed && len(parts) < 2 {
		parts = append(parts, h)
	}
	return strings.Join(parts, " ")
}

func bytes(n int64) string {
	switch {
	case n >= 1<<30:
		v := float64(n) / (1 << 30)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d GiB", int64(v))
		}
		return fmt.Sprintf("%.1f GiB", v)
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d B", n)
}

func (s *Server) actorLogs(ctx context.Context, _ *mcp.CallToolRequest, in LogsIn) (*mcp.CallToolResult, any, error) {
	if in.Tail <= 0 {
		in.Tail = 50
	}
	ref := model.ActorRef{Atespace: in.Atespace, Name: in.Name}
	lines, since, err := s.opts.Backend.Logs(ctx, ref, in.Tail)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	switch {
	case len(lines) == 0:
		fmt.Fprintf(&b, "No log lines for %s since it started on its worker at %s.\n", ref, since.Format(time.RFC3339))
	case !since.IsZero():
		fmt.Fprintf(&b, "Worker log since %s started on it at %s. Earlier lines belong to whatever ran there before.\n", ref, since.Format(time.RFC3339))
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s: %s\n", l.Time.Format(time.RFC3339), l.Source, l.Text)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, map[string]any{"kind": "logs", "actor": ref, "lines": lines, "since": since}, nil
}

func (s *Server) listPools(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !c.Access.Workers {
		return nil, nil, fmt.Errorf("worker pools need cluster-wide viewer access, which %s doesn't have", orYou(c.Access.Principal))
	}
	var odd []model.Worker
	for _, w := range c.Workers {
		if w.State != "active" {
			odd = append(odd, w)
		}
	}
	var b strings.Builder
	for _, p := range c.Pools {
		fmt.Fprintf(&b, "- %s (%s): desired %d, ready %d, busy %d\n", p.Name, p.SandboxClass, p.Desired, p.Ready, p.Busy)
	}
	for _, w := range odd {
		fmt.Fprintf(&b, "- worker %s/%s is %s with %d actors\n", w.Pool, w.Name, w.State, w.Actors)
	}
	if odd == nil {
		odd = []model.Worker{}
	}
	return &mcp.CallToolResult{Content: text(b.String())}, map[string]any{"kind": "pools", "pools": c.Pools, "workers": odd}, nil
}

func (s *Server) queryMetric(ctx context.Context, _ *mcp.CallToolRequest, in MetricIn) (*mcp.CallToolResult, any, error) {
	m := backend.Metric(in.Metric)
	info, ok := metricInfo[m]
	if !ok {
		return nil, nil, fmt.Errorf("unknown metric %q; use one of resume_p95, resume_failure_rate, capacity, crashes, running_actors", in.Metric)
	}
	if in.By != "" && in.By != "template" && in.By != "pool" {
		return nil, nil, fmt.Errorf("by must be template or pool, not %q", in.By)
	}
	if s.opts.Metrics == nil {
		return nil, nil, fmt.Errorf("no Prometheus configured; start substrate-console with --prometheus-url")
	}
	if in.Window == "" {
		in.Window = "6h"
	}
	w, err := parseWindow(in.Window, 6*time.Hour)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	series, err := s.metricsFor(c).Query(ctx, backend.MetricQuery{Metric: m, By: in.By, Atespace: in.Atespace, Window: w, Step: w / 60})
	if err != nil {
		return nil, nil, err
	}
	if series == nil {
		series = []model.Series{}
	}
	res := MetricResult{Kind: "metric", Metric: in.Metric, Title: info.title, Unit: info.unit, By: in.By, Window: in.Window, Series: series}
	if in.By != "" {
		res.Title += " by " + in.By
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, last %s (%s):\n", res.Title, in.Window, info.unit)
	for _, sr := range series {
		if len(sr.Points) == 0 {
			continue
		}
		first, last := sr.Points[0].V, sr.Points[len(sr.Points)-1].V
		lo, hi := first, first
		for _, p := range sr.Points {
			lo, hi = min(lo, p.V), max(hi, p.V)
		}
		fmt.Fprintf(&b, "- %s: now %.1f, start %.1f, min %.1f, max %.1f\n", sr.Label, last, first, lo, hi)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, res, nil
}

func orYou(principal string) string {
	if principal == "" {
		return "your token"
	}
	return principal
}
