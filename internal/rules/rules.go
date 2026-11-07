// Package rules turns a cluster snapshot into the console's first two tiers:
// the attention list ("is anything broken?") and the vitals ("is it
// healthy?"). The rules follow the failure scenarios in upstream's
// docs/guides/slos-and-alerts.md, plus one of our own for actors stuck in a
// transitional state.
package rules

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
)

// Severity orders attention items.
type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Resolved Severity = "resolved"
)

func (s Severity) rank() int {
	switch s {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}

// Action is a suggested next step: a tool call the view or the model can make.
type Action struct {
	Label   string         `json:"label"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Primary bool           `json:"primary,omitempty"`
	// Destructive marks actions the view must confirm with a dry run first.
	Destructive bool `json:"destructive,omitempty"`
}

// Item is one entry in the attention list.
type Item struct {
	ID       string           `json:"id"`
	Severity Severity         `json:"severity"`
	Rule     string           `json:"rule"` // upstream scenario, e.g. "C.4"
	Title    string           `json:"title"`
	Facts    []string         `json:"facts,omitempty"`
	Actors   []model.ActorRef `json:"actors,omitempty"`
	Since    *time.Time       `json:"since,omitempty"`
	Actions  []Action         `json:"actions,omitempty"`

	atespaces map[string]bool
}

// Vital is one of the four health numbers.
type Vital struct {
	Key      string        `json:"key"`
	Label    string        `json:"label"`
	Value    string        `json:"value"`
	Unit     string        `json:"unit,omitempty"`
	Delta    string        `json:"delta,omitempty"`
	Tone     string        `json:"tone,omitempty"` // "", "warn", "crit"
	Note     string        `json:"note,omitempty"`
	Series   []model.Point `json:"series,omitempty"`
	Baseline []model.Point `json:"baseline,omitempty"`
	// Drill is the tool call that lists the objects behind the number.
	Drill *Action `json:"drill,omitempty"`
}

// AtespaceSummary is one row of the scope switcher.
type AtespaceSummary struct {
	Name     string `json:"name"`
	Actors   int    `json:"actors"`
	Problems int    `json:"problems"`
}

// Status is the result of the status tool.
type Status struct {
	Kind        string            `json:"kind"` // "status"
	Cluster     string            `json:"cluster"`
	Version     string            `json:"version,omitempty"`
	Scope       string            `json:"scope"` // "" for the whole cluster, else an atespace
	Headline    string            `json:"headline"`
	Summary     string            `json:"summary"`
	Items       []Item            `json:"items"`
	Vitals      []Vital           `json:"vitals"`
	Pools       []model.Pool      `json:"pools,omitempty"`
	Atespaces   []AtespaceSummary `json:"atespaces"`
	Counts      map[string]int    `json:"counts"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Notes       []string          `json:"notes,omitempty"`
}

// Thresholds for the stuck-transition rule. Resumes normally take well under
// a second, so two minutes is generous.
var stuckAfter = map[model.ActorState]time.Duration{
	model.StateResuming:   2 * time.Minute,
	model.StateSuspending: 5 * time.Minute,
	model.StateReverting:  5 * time.Minute,
	model.StateDeleting:   10 * time.Minute,
}

const capacityWarn = 0.95

// Engine evaluates rules. It remembers items between calls so that an item
// that clears shows as resolved for a day.
type Engine struct {
	mu   sync.Mutex
	seen map[string]seenItem
}

type seenItem struct {
	item     Item
	lastSeen time.Time
}

// NewEngine returns an engine with no history.
func NewEngine() *Engine { return &Engine{seen: map[string]seenItem{}} }

// Evaluate builds the status for scope ("" for the whole cluster). metrics
// may be nil.
func (e *Engine) Evaluate(ctx context.Context, c *model.Cluster, scope string, metrics backend.Metrics) Status {
	now := c.FetchedAt
	if now.IsZero() {
		now = time.Now()
	}
	all := append(e.crashes(c, now), e.stuck(c, now)...)
	all = append(all, e.workers(c)...)
	all = append(all, e.pools(c, now)...)
	all = append(all, e.capacity(ctx, c, metrics)...)

	e.mu.Lock()
	current := map[string]bool{}
	for _, it := range all {
		current[it.ID] = true
		e.seen[it.ID] = seenItem{item: it, lastSeen: now}
	}
	for id, s := range e.seen {
		if current[id] {
			continue
		}
		if now.Sub(s.lastSeen) > 24*time.Hour {
			delete(e.seen, id)
			continue
		}
		r := s.item
		r.Severity = Resolved
		r.Facts = []string{"Resolved " + Ago(now, s.lastSeen)}
		r.Actions = nil
		all = append(all, r)
	}
	e.mu.Unlock()

	var items []Item
	for _, it := range all {
		if scope == "" || it.atespaces[scope] {
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Severity.rank() != items[j].Severity.rank() {
			return items[i].Severity.rank() < items[j].Severity.rank()
		}
		return items[i].ID < items[j].ID
	})

	st := Status{
		Kind: "status", Cluster: c.Name, Version: c.Version, Scope: scope,
		Items: items, GeneratedAt: now, Counts: map[string]int{},
	}
	if items == nil {
		st.Items = []Item{}
	}
	open := 0
	for _, it := range items {
		if it.Severity != Resolved {
			open++
		}
	}
	switch open {
	case 0:
		st.Headline = "All clear"
	case 1:
		st.Headline = "1 issue"
	default:
		st.Headline = fmt.Sprintf("%d issues", open)
	}

	perAtespace := map[string]*AtespaceSummary{}
	for _, name := range c.Atespaces {
		perAtespace[name] = &AtespaceSummary{Name: name}
	}
	total := 0
	for _, a := range c.Actors {
		s := perAtespace[a.Atespace]
		if s == nil {
			s = &AtespaceSummary{Name: a.Atespace}
			perAtespace[a.Atespace] = s
		}
		s.Actors++
		if scope == "" || a.Atespace == scope {
			st.Counts[string(a.State)]++
			total++
		}
	}
	for _, it := range all {
		if it.Severity == Resolved {
			continue
		}
		for name := range it.atespaces {
			if s := perAtespace[name]; s != nil {
				s.Problems++
			}
		}
	}
	for _, s := range perAtespace {
		st.Atespaces = append(st.Atespaces, *s)
	}
	sort.Slice(st.Atespaces, func(i, j int) bool { return st.Atespaces[i].Name < st.Atespaces[j].Name })

	if scope == "" {
		st.Pools = c.Pools
		st.Summary = fmt.Sprintf("%s · %s · %s", plural(total, "actor"), plural(len(c.Atespaces), "atespace"), plural(len(c.Pools), "pool"))
	} else {
		st.Summary = fmt.Sprintf("%s · %d running", plural(total, "actor"), st.Counts["running"])
	}
	st.Vitals = e.vitals(ctx, c, scope, metrics, st.Counts, total)
	if metrics == nil {
		st.Notes = append(st.Notes, "Resume latency and history need Prometheus. Start with --prometheus-url.")
	}
	return st
}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func short(worker, pool string) string { return strings.TrimPrefix(worker, pool+"/") }

// CrashReason turns a crash message into a few words.
func CrashReason(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "memory") || strings.Contains(m, "oom"):
		return "Out of memory"
	case strings.Contains(m, "exit"):
		return "Process exited"
	case strings.Contains(m, "worker") && (strings.Contains(m, "lost") || strings.Contains(m, "unreachable")):
		return "Worker lost"
	case msg == "":
		return "Unknown cause"
	}
	if len(msg) > 60 {
		return msg[:57] + "…"
	}
	return msg
}

// C.4: actors failing. Grouped by worker so one bad node is one item.
func (e *Engine) crashes(c *model.Cluster, now time.Time) []Item {
	groups := map[string][]model.Actor{}
	for _, a := range c.Actors {
		if a.State == model.StateCrashed {
			// ateapi clears the worker assignment on a crash, so the live
			// backend often only has the node. Group by the most specific
			// place we know.
			key := a.Worker
			if key == "" && a.Node != "" {
				key = "node:" + a.Node
			}
			if key == "" {
				key = "template:" + a.Atespace + "/" + a.Template
			}
			groups[key] = append(groups[key], a)
		}
	}
	var out []Item
	for key, as := range groups {
		sort.Slice(as, func(i, j int) bool { return crashTime(as[i]).After(crashTime(as[j])) })
		latest := crashTime(as[0])
		it := Item{ID: "crash:" + key, Severity: Critical, Rule: "C.4", Since: &latest, atespaces: map[string]bool{}}
		reasons, templates, spaces := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, a := range as {
			it.Actors = append(it.Actors, a.Ref())
			it.atespaces[a.Atespace] = true
			spaces[a.Atespace] = true
			templates[a.Template] = true
			msg := ""
			if a.Crash != nil {
				msg = a.Crash.Message
			}
			reasons[CrashReason(msg)] = true
		}
		where := ""
		switch {
		case as[0].Worker != "":
			where = " on " + short(as[0].Worker, as[0].Pool)
		case as[0].Node != "":
			where = " on node " + as[0].Node
		}
		if len(as) == 1 {
			it.Title = fmt.Sprintf("%s crashed", as[0].Name)
		} else {
			it.Title = fmt.Sprintf("%d crashes%s", len(as), where)
		}
		if len(reasons) == 1 {
			for r := range reasons {
				it.Facts = append(it.Facts, r)
			}
		}
		if len(templates) == 1 {
			it.Facts = append(it.Facts, as[0].Template)
		}
		if len(as) == 1 && where != "" {
			it.Facts = append(it.Facts, strings.TrimPrefix(where, " on "))
		}
		it.Facts = append(it.Facts, Ago(now, latest))
		if len(as) == 1 {
			it.Actions = []Action{
				{Label: "Open", Tool: "get_actor", Args: map[string]any{"atespace": as[0].Atespace, "name": as[0].Name}},
				{Label: "Revert", Tool: "revert_actors", Args: refsArgs(it.Actors), Primary: true, Destructive: true},
			}
		} else {
			it.Actions = []Action{
				{Label: "Show", Tool: "list_actors", Args: map[string]any{"state": "crashed", "worker": as[0].Worker, "atespace": onlyOne(spaces)}},
				{Label: fmt.Sprintf("Revert all %d", len(as)), Tool: "revert_actors", Args: refsArgs(it.Actors), Primary: true, Destructive: true},
			}
		}
		out = append(out, it)
	}
	return out
}

func crashTime(a model.Actor) time.Time {
	if a.Crash != nil {
		return a.Crash.Time
	}
	if a.StateSince != nil {
		return *a.StateSince
	}
	return time.Time{}
}

func refsArgs(refs []model.ActorRef) map[string]any {
	list := make([]map[string]string, 0, len(refs))
	for _, r := range refs {
		list = append(list, map[string]string{"atespace": r.Atespace, "name": r.Name})
	}
	return map[string]any{"actors": list}
}

func onlyOne(m map[string]bool) string {
	if len(m) == 1 {
		return keys(m)[0]
	}
	return ""
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Our own rule: an actor that stays in a transitional state. This is the
// stuck-delete / stuck-resume case operators hit in practice.
func (e *Engine) stuck(c *model.Cluster, now time.Time) []Item {
	var out []Item
	for _, a := range c.Actors {
		limit, ok := stuckAfter[a.State]
		if !ok || a.StateSince == nil || now.Sub(*a.StateSince) < limit {
			continue
		}
		since := *a.StateSince
		it := Item{
			ID: "stuck:" + a.Atespace + "/" + a.Name, Severity: Warning, Rule: "stuck",
			Title:  fmt.Sprintf("%s stuck %s", a.Name, a.State),
			Actors: []model.ActorRef{a.Ref()}, Since: &since, atespaces: set(a.Atespace),
		}
		it.Facts = append(it.Facts, "for "+Duration(now.Sub(since)))
		if a.State == model.StateResuming {
			it.Facts = append(it.Facts, "normally under 1 s")
		}
		it.Facts = append(it.Facts, a.Template)
		it.Actions = []Action{
			{Label: "Open", Tool: "get_actor", Args: map[string]any{"atespace": a.Atespace, "name": a.Name}},
			{Label: "Revert", Tool: "revert_actors", Args: refsArgs(it.Actors), Primary: true, Destructive: true},
		}
		out = append(out, it)
	}
	return out
}

// B.2: workers that stopped reporting while actors sit on them.
func (e *Engine) workers(c *model.Cluster) []Item {
	var out []Item
	for _, w := range c.Workers {
		if w.State != "unavailable" || w.Actors == 0 {
			continue
		}
		it := Item{
			ID: "worker:" + w.Pool + "/" + w.Name, Severity: Critical, Rule: "B.2",
			Title:     fmt.Sprintf("%s unreachable", w.Name),
			Facts:     []string{plural(w.Actors, "actor") + " on it", w.Pool},
			atespaces: map[string]bool{},
			Actions:   []Action{{Label: "Drain", Tool: "drain_worker", Args: map[string]any{"worker": w.Pool + "/" + w.Name}, Primary: true, Destructive: true}},
		}
		for _, a := range c.Actors {
			if a.Worker == w.Pool+"/"+w.Name {
				it.atespaces[a.Atespace] = true
				it.Actors = append(it.Actors, a.Ref())
			}
		}
		out = append(out, it)
	}
	return out
}

// B.1: worker pools that don't reach their desired size.
func (e *Engine) pools(c *model.Cluster, now time.Time) []Item {
	var out []Item
	for _, p := range c.Pools {
		if p.Ready >= p.Desired {
			continue
		}
		it := Item{
			ID: "pool:" + p.Namespace + "/" + p.Name, Severity: Warning, Rule: "B.1",
			Title:     fmt.Sprintf("%s short %s", p.Name, plural(p.Desired-p.Ready, "worker")),
			Facts:     []string{fmt.Sprintf("%d of %d ready", p.Ready, p.Desired)},
			atespaces: map[string]bool{},
			Actions:   []Action{{Label: "Pools", Tool: "list_worker_pools", Args: map[string]any{}}},
		}
		if p.NotReadySince != nil {
			it.Since = p.NotReadySince
			it.Facts = append(it.Facts, "since "+Ago(now, *p.NotReadySince))
		}
		out = append(out, it)
	}
	return out
}

// A.1: resumes failing for lack of capacity. With Prometheus we report the
// failure rate; without it we warn when a pool is nearly full.
func (e *Engine) capacity(ctx context.Context, c *model.Cluster, metrics backend.Metrics) []Item {
	var failRate float64 = -1
	if metrics != nil {
		if s, err := metrics.Query(ctx, backend.MetricQuery{Metric: backend.MetricResumeFailures, Window: 15 * time.Minute, Step: time.Minute}); err == nil && len(s) > 0 && len(s[0].Points) > 0 {
			failRate = s[0].Points[len(s[0].Points)-1].V
		}
	}
	var out []Item
	for _, p := range c.Pools {
		if p.Ready == 0 || float64(p.Busy)/float64(p.Ready) < capacityWarn {
			continue
		}
		it := Item{
			ID: "capacity:" + p.Namespace + "/" + p.Name, Severity: Warning, Rule: "A.1",
			Title:     fmt.Sprintf("%s nearly full", p.Name),
			Facts:     []string{fmt.Sprintf("%d of %d workers busy", p.Busy, p.Ready)},
			atespaces: map[string]bool{},
			Actions:   []Action{{Label: "Pools", Tool: "list_worker_pools", Args: map[string]any{}}},
		}
		if failRate > 1 {
			it.Title = fmt.Sprintf("%s full, resumes failing", p.Name)
			it.Facts = []string{fmt.Sprintf("%.1f%% failed in 15 min", failRate), fmt.Sprintf("%d of %d busy", p.Busy, p.Ready)}
		}
		for _, a := range c.Actors {
			if a.Pool == p.Name {
				it.atespaces[a.Atespace] = true
			}
		}
		out = append(out, it)
	}
	return out
}

func (e *Engine) vitals(ctx context.Context, c *model.Cluster, scope string, metrics backend.Metrics, counts map[string]int, total int) []Vital {
	series := func(m backend.Metric, offset time.Duration) []model.Point {
		if metrics == nil {
			return nil
		}
		s, err := metrics.Query(ctx, backend.MetricQuery{Metric: m, Atespace: scope, Window: 24 * time.Hour, Step: 30 * time.Minute, Offset: offset})
		if err != nil || len(s) == 0 {
			return nil
		}
		return s[0].Points
	}
	week := 7 * 24 * time.Hour

	busy, ready := 0, 0
	for _, p := range c.Pools {
		busy += p.Busy
		ready += p.Ready
	}
	capV := Vital{Key: "capacity", Label: "Capacity", Value: "–", Unit: "%",
		Drill: &Action{Label: "Open pools", Tool: "list_worker_pools", Args: map[string]any{}}}
	if ready > 0 {
		pct := 100 * float64(busy) / float64(ready)
		capV.Value = fmt.Sprintf("%.0f", pct)
		capV.Note = fmt.Sprintf("%d of %d workers", busy, ready)
		// A full pool matters even when the cluster total looks fine, because
		// a template can only use its own pool.
		for _, p := range c.Pools {
			if p.Ready > 0 && float64(p.Busy)/float64(p.Ready) >= capacityWarn {
				capV.Tone = "warn"
				capV.Note = fmt.Sprintf("%s at %d of %d", p.Name, p.Busy, p.Ready)
			}
		}
	}
	if scope != "" {
		capV.Note += ", shared"
	}
	capV.Series, capV.Baseline = series(backend.MetricCapacity, 0), series(backend.MetricCapacity, week)
	capV.Delta = delta(capV.Series, capV.Baseline, " pts")

	res := Vital{Key: "resume_p95", Label: "Resume p95", Value: "–", Unit: "ms",
		Drill: &Action{Label: "By template", Tool: "query_metric", Args: map[string]any{"metric": "resume_p95", "by": "template", "window": "6h"}}}
	res.Series, res.Baseline = series(backend.MetricResumeP95, 0), series(backend.MetricResumeP95, week)
	if n := len(res.Series); n > 0 {
		res.Value = fmt.Sprintf("%.0f", res.Series[n-1].V)
		res.Delta = delta(res.Series, res.Baseline, " ms")
	} else {
		res.Note = "needs Prometheus"
	}

	dens := Vital{Key: "density", Label: "Suspended", Value: "–", Unit: "%",
		Drill: &Action{Label: "Suspended actors", Tool: "list_actors", Args: map[string]any{"state": "suspended", "atespace": scope}}}
	if total > 0 {
		dens.Value = fmt.Sprintf("%.0f", 100*float64(counts["suspended"])/float64(total))
		dens.Note = fmt.Sprintf("%d running", counts["running"])
	}
	if s := series(backend.MetricRunning, 0); s != nil && total > 0 {
		for _, p := range s {
			dens.Series = append(dens.Series, model.Point{T: p.T, V: math.Round(1000*(1-p.V/float64(total))) / 10})
		}
	}

	cr := Vital{Key: "crashes", Label: "Crashed", Value: fmt.Sprintf("%d", counts["crashed"]),
		Drill: &Action{Label: "Crashed actors", Tool: "list_actors", Args: map[string]any{"state": "crashed", "atespace": scope}}}
	if counts["crashed"] > 0 {
		cr.Tone = "crit"
	}
	cr.Series, cr.Baseline = series(backend.MetricCrashes, 0), series(backend.MetricCrashes, week)
	workers := map[string]bool{}
	for _, a := range c.Actors {
		if a.State == model.StateCrashed && (scope == "" || a.Atespace == scope) {
			workers[short(a.Worker, a.Pool)] = true
		}
	}
	if len(workers) == 1 && counts["crashed"] > 1 {
		for w := range workers {
			cr.Note = "All on " + w
		}
	}
	return []Vital{capV, res, dens, cr}
}

func delta(cur, base []model.Point, unit string) string {
	if len(cur) == 0 || len(base) == 0 {
		return ""
	}
	d := cur[len(cur)-1].V - base[len(base)-1].V
	if math.Abs(d) < 0.5 {
		return "flat"
	}
	return fmt.Sprintf("%+.0f%s", d, unit)
}

// Ago renders a past time relative to now, e.g. "6m ago".
func Ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Duration renders a duration in the largest sensible unit.
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}
