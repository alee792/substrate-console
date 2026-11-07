// Package demo serves a fixed example cluster. It backs `--demo`, the
// screenshots in the README, and the tests. Writes change its in-memory
// state, so a demo session behaves like a small real cluster.
package demo

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
)

const gib = int64(1) << 30

// Cluster is the demo backend. It implements backend.Backend and
// backend.Metrics.
type Cluster struct {
	now func() time.Time

	mu        sync.Mutex
	start     time.Time
	actors    []model.Actor
	workers   []model.Worker
	pools     []model.Pool
	templates []model.Template
}

var (
	_ backend.Backend = (*Cluster)(nil)
	_ backend.Metrics = (*Cluster)(nil)
)

// New returns the example cluster with times relative to now(). Pass nil to
// use the wall clock.
func New(now func() time.Time) *Cluster {
	if now == nil {
		now = time.Now
	}
	c := &Cluster{now: now, start: now()}
	c.seed()
	return c
}

func ago(t time.Time, d time.Duration) *time.Time {
	v := t.Add(-d)
	return &v
}

func (c *Cluster) seed() {
	t := c.start
	r := rand.New(rand.NewSource(7))
	hex := func() string { return fmt.Sprintf("%04x", r.Intn(0xffff)) }

	c.templates = []model.Template{
		{Atespace: "triage", Name: "agent-devbox-v11", Image: "ghcr.io/example/agent-devbox:11", MemoryLimit: 2 * gib},
		{Atespace: "triage", Name: "agent-devbox-v12", Image: "ghcr.io/example/agent-devbox:12", MemoryLimit: 4 * gib},
		{Atespace: "ioc-enrich", Name: "mcp-enrich-v3", Image: "ghcr.io/example/mcp-enrich:3", MemoryLimit: 1 * gib},
		{Atespace: "code-review", Name: "codex-review", Image: "ghcr.io/example/codex-review:5", MemoryLimit: 4 * gib},
	}
	c.pools = []model.Pool{
		{Namespace: "ate-workers", Name: "gvisor-pool-a", SandboxClass: "gvisor", Desired: 40, Ready: 40},
		{Namespace: "ate-workers", Name: "microvm-pool-b", SandboxClass: "microvm", Desired: 8, Ready: 8},
	}
	for _, p := range c.pools {
		for i := 1; i <= p.Desired; i++ {
			c.workers = append(c.workers, model.Worker{
				Name: fmt.Sprintf("w-%02d", i), Pool: p.Name, Node: fmt.Sprintf("kind-worker%d", (i-1)%4+1),
				State: "active", LastSeen: ago(t, time.Duration(r.Intn(4))*time.Second),
			})
		}
	}

	add := func(a model.Actor) {
		if a.CreatedAt == nil {
			a.CreatedAt = ago(t, time.Duration(2+r.Intn(400))*time.Hour)
		}
		if a.UID == "" {
			a.UID = fmt.Sprintf("%08x-demo", r.Uint32())
		}
		c.actors = append(c.actors, a)
	}
	suspended := func(atespace, name, tmpl string, idle time.Duration, size int64) model.Actor {
		return model.Actor{
			Atespace: atespace, Name: name, State: model.StateSuspended, Template: tmpl,
			StateSince: ago(t, idle), LastActive: ago(t, idle),
			Snapshots:   []model.Snapshot{{Generation: 3 + r.Intn(20), Fidelity: "memory", Storage: "durable", SizeBytes: size}},
			ResumeP95Ms: 380 + float64(r.Intn(90)),
		}
	}

	// triage: the atespace with the problems.
	crash := func(name string, d time.Duration, worker string) model.Actor {
		return model.Actor{
			Atespace: "triage", Name: name, State: model.StateCrashed, Template: "agent-devbox-v11",
			Worker: "gvisor-pool-a/" + worker, Pool: "gvisor-pool-a", Node: "kind-worker3", SandboxClass: "gvisor",
			StateSince: ago(t, d), LastActive: ago(t, d), MemoryLimit: 2 * gib,
			Crash: &model.Crash{
				Message: fmt.Sprintf("container \"agent\" exceeded memory limit 2Gi (working set 2.04Gi) and was killed by the sandbox on gvisor-pool-a/%s", worker),
				Time:    t.Add(-d),
			},
			Snapshots: []model.Snapshot{{Generation: 14, Fidelity: "memory", Storage: "durable", SizeBytes: 1300 << 20}},
		}
	}
	add(crash("triage-0d55", 6*time.Minute, "w-03"))
	add(crash("triage-77b2", 31*time.Minute, "w-03"))
	add(crash("triage-a1f9", 48*time.Minute, "w-03"))

	gv := 0 // next gvisor worker index for running actors
	run := func(atespace, name, tmpl string, active time.Duration, mem int64) model.Actor {
		w := c.workers[gv%38] // 38 busy workers in gvisor-pool-a
		gv++
		return model.Actor{
			Atespace: atespace, Name: name, State: model.StateRunning, Template: tmpl,
			Worker: w.Pool + "/" + w.Name, Pool: w.Pool, Node: w.Node, SandboxClass: "gvisor",
			StateSince: ago(t, active+time.Duration(r.Intn(1800))*time.Second), LastActive: ago(t, active),
			MemoryBytes: mem, ResumeP95Ms: 370 + float64(r.Intn(60)),
		}
	}
	add(run("triage", "triage-7f3a", "agent-devbox-v12", 0, 1100<<20))
	add(run("triage", "triage-c41e", "agent-devbox-v12", 14*time.Second, 840<<20))
	for i := 0; i < 30; i++ {
		add(run("triage", "triage-"+hex(), "agent-devbox-v12", time.Duration(r.Intn(300))*time.Second, int64(500+r.Intn(1500))<<20))
	}
	add(model.Actor{
		Atespace: "triage", Name: "triage-91c0", State: model.StateResuming, Template: "agent-devbox-v12",
		Worker: "gvisor-pool-a/w-12", Pool: "gvisor-pool-a", SandboxClass: "gvisor",
		StateSince: ago(t, 2*time.Second), LastActive: ago(t, 2*time.Second), ResumeP95Ms: 410,
	})
	add(suspended("triage", "triage-2a90", "agent-devbox-v12", 41*time.Minute, 1300<<20))
	add(suspended("triage", "triage-5e18", "agent-devbox-v12", 3*time.Hour, 1200<<20))
	for i := 0; i < 148; i++ {
		tmpl := "agent-devbox-v12"
		if i%5 == 0 {
			tmpl = "agent-devbox-v11"
		}
		add(suspended("triage", "triage-"+hex(), tmpl, time.Duration(10+r.Intn(60*24*9))*time.Minute, int64(900+r.Intn(600))<<20))
	}

	// ioc-enrich: quiet, except one actor stuck resuming.
	add(model.Actor{
		Atespace: "ioc-enrich", Name: "ioc-enrich-02", State: model.StateResuming, Template: "mcp-enrich-v3",
		Worker: "gvisor-pool-a/w-39", Pool: "gvisor-pool-a", SandboxClass: "gvisor",
		StateSince: ago(t, 9*time.Minute), LastActive: ago(t, 41*time.Minute), ResumeP95Ms: 250,
		Snapshots: []model.Snapshot{{Generation: 31, Fidelity: "memory", Storage: "durable", SizeBytes: 300 << 20}},
	})
	for i := 0; i < 6; i++ {
		add(run("ioc-enrich", fmt.Sprintf("ioc-enrich-%02d", 10+i), "mcp-enrich-v3", time.Duration(r.Intn(120))*time.Second, int64(200+r.Intn(300))<<20))
	}
	for i := 0; i < 143; i++ {
		add(suspended("ioc-enrich", fmt.Sprintf("ioc-enrich-%02d", 20+i), "mcp-enrich-v3", time.Duration(5+r.Intn(60*24*3))*time.Minute, int64(250+r.Intn(100))<<20))
	}

	// code-review: runs on microVMs.
	for i := 0; i < 2; i++ {
		w := c.workers[40+i]
		add(model.Actor{
			Atespace: "code-review", Name: fmt.Sprintf("codex-review-%d", i+1), State: model.StateRunning, Template: "codex-review",
			Worker: w.Pool + "/" + w.Name, Pool: w.Pool, Node: w.Node, SandboxClass: "microvm",
			StateSince: ago(t, 20*time.Minute), LastActive: ago(t, 30*time.Second), MemoryBytes: 2 * gib, ResumeP95Ms: 610,
		})
	}
	for i := 0; i < 74; i++ {
		add(suspended("code-review", fmt.Sprintf("codex-review-%d", i+3), "codex-review", time.Duration(30+r.Intn(60*24*14))*time.Minute, int64(1800+r.Intn(800))<<20))
	}
	// 38 running gvisor actors fill w-01..w-38, and ioc-enrich-02 holds w-39,
	// so gvisor-pool-a shows 39 of 40 workers busy.
}

func (c *Cluster) recount() {
	busy := map[string]int{}
	for i := range c.workers {
		c.workers[i].Actors = 0
	}
	for _, a := range c.actors {
		if a.Worker == "" || a.State == model.StateCrashed || a.State == model.StateSuspended {
			continue
		}
		for i := range c.workers {
			if c.workers[i].Pool+"/"+c.workers[i].Name == a.Worker {
				c.workers[i].Actors++
			}
		}
	}
	for _, w := range c.workers {
		if w.Actors > 0 {
			busy[w.Pool]++
		}
	}
	for i := range c.pools {
		c.pools[i].Busy = busy[c.pools[i].Name]
	}
}

// Snapshot implements backend.Backend.
func (c *Cluster) Snapshot(context.Context) (*model.Cluster, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recount()
	out := &model.Cluster{
		Name: "kind-sec-tools", Version: "v1.0.0", FetchedAt: c.now(),
		Atespaces: []string{"code-review", "ioc-enrich", "triage"},
		Actors:    append([]model.Actor(nil), c.actors...),
		Workers:   append([]model.Worker(nil), c.workers...),
		Pools:     append([]model.Pool(nil), c.pools...),
		Templates: append([]model.Template(nil), c.templates...),
	}
	// Shift times so the demo stays "live": everything moves with the clock.
	shift := c.shift()
	if shift > 0 {
		for i := range out.Actors {
			a := &out.Actors[i]
			for _, p := range []**time.Time{&a.StateSince, &a.LastActive, &a.CreatedAt} {
				if *p != nil {
					v := (*p).Add(shift)
					*p = &v
				}
			}
			if a.Crash != nil {
				cr := *a.Crash
				cr.Time = cr.Time.Add(shift)
				a.Crash = &cr
			}
		}
	}
	return out, nil
}

// shift is how far the clock has moved since the demo was built. Stored
// times are relative to c.start, so everything read out moves by shift.
func (c *Cluster) shift() time.Duration {
	if d := c.now().Sub(c.start); d > 0 {
		return d
	}
	return 0
}

func (c *Cluster) find(ref model.ActorRef) (int, error) {
	for i, a := range c.actors {
		if a.Atespace == ref.Atespace && a.Name == ref.Name {
			return i, nil
		}
	}
	return -1, fmt.Errorf("actor %s not found", ref)
}

// Timeline implements backend.Backend.
func (c *Cluster) Timeline(_ context.Context, ref model.ActorRef) ([]model.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, err := c.find(ref)
	if err != nil {
		return nil, err
	}
	a := c.actors[i]
	t := c.start
	if a.StateSince != nil {
		t = *a.StateSince
	}
	ev := []model.Event{{Time: t, State: a.State}}
	switch a.State {
	case model.StateCrashed:
		ev[0].Detail = "OOM on " + strings.TrimPrefix(a.Worker, a.Pool+"/")
		ev = append(ev,
			model.Event{Time: t.Add(-4*time.Minute - 31*time.Second), State: model.StateRunning, Detail: "resumed from local snapshot in 402 ms"},
			model.Event{Time: t.Add(-4*time.Minute - 32*time.Second), State: model.StateResuming, Detail: "inbound request, parked 0.4s"},
			model.Event{Time: t.Add(-41*time.Minute - 4*time.Second), State: model.StateSuspended, Detail: "gen 14, memory, 1.3 GiB"},
			model.Event{Time: t.Add(-58*time.Minute - 39*time.Second), State: model.StateRunning},
		)
	case model.StateResuming:
		ev[0].Detail = "from durable snapshot"
		ev = append(ev, model.Event{Time: t.Add(-30 * time.Minute), State: model.StateSuspended, Detail: "idle"})
	case model.StateSuspended:
		ev[0].Detail = "idle"
		ev = append(ev, model.Event{Time: t.Add(-25 * time.Minute), State: model.StateRunning})
	case model.StateRunning:
		ev = append(ev, model.Event{Time: t.Add(-400 * time.Millisecond), State: model.StateResuming})
	}
	if a.CreatedAt != nil {
		ev = append(ev, model.Event{Time: *a.CreatedAt, State: "created", Detail: "from " + a.Template})
	}
	for i := range ev {
		ev[i].Time = ev[i].Time.Add(c.shift())
	}
	return ev, nil
}

// Logs implements backend.Backend.
func (c *Cluster) Logs(_ context.Context, ref model.ActorRef, tail int) ([]model.LogLine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, err := c.find(ref)
	if err != nil {
		return nil, err
	}
	a := c.actors[i]
	t := c.start
	if a.StateSince != nil {
		t = *a.StateSince
	}
	var lines []model.LogLine
	if a.State == model.StateCrashed {
		lines = []model.LogLine{
			{Time: t.Add(-3 * time.Second), Source: "agent", Text: "bash: npm install (412 packages)"},
			{Time: t.Add(-2 * time.Second), Source: "agent", Text: "node --max-old-space-size=4096 build.js"},
			{Time: t.Add(-1 * time.Second), Source: "agent", Text: "[build] bundling 1,904 modules"},
			{Time: t, Source: "sandbox", Text: "oom-kill: agent rss=2.04Gi limit=2.00Gi", Error: true},
			{Time: t, Source: "atelet", Text: "ReportActorCrashed actor=" + a.Name, Error: true},
		}
	} else {
		lines = []model.LogLine{
			{Time: t.Add(-2 * time.Second), Source: "agent", Text: "session resumed"},
			{Time: t.Add(-1 * time.Second), Source: "agent", Text: "waiting for input"},
		}
	}
	for i := range lines {
		lines[i].Time = lines[i].Time.Add(c.shift())
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

func (c *Cluster) mutate(ref model.ActorRef, f func(a *model.Actor) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, err := c.find(ref)
	if err != nil {
		return err
	}
	return f(&c.actors[i])
}

func (c *Cluster) since() *time.Time {
	// Stored times are relative to c.start; Snapshot shifts them forward.
	v := c.start.Add(-time.Second)
	return &v
}

// Suspend implements backend.Backend.
func (c *Cluster) Suspend(_ context.Context, ref model.ActorRef) error {
	return c.mutate(ref, func(a *model.Actor) error {
		if a.State != model.StateRunning {
			return fmt.Errorf("actor %s is %s; only a running actor can be suspended", ref, a.State)
		}
		a.State, a.Worker, a.Node, a.StateSince = model.StateSuspended, "", "", c.since()
		return nil
	})
}

// Resume implements backend.Backend.
func (c *Cluster) Resume(_ context.Context, ref model.ActorRef) error {
	return c.mutate(ref, func(a *model.Actor) error {
		if a.State != model.StateSuspended {
			return fmt.Errorf("actor %s is %s; only a suspended actor can be resumed", ref, a.State)
		}
		a.State, a.StateSince, a.LastActive = model.StateRunning, c.since(), c.since()
		a.Worker, a.Pool = "gvisor-pool-a/w-40", "gvisor-pool-a"
		return nil
	})
}

// Revert implements backend.Backend.
func (c *Cluster) Revert(_ context.Context, ref model.ActorRef) error {
	return c.mutate(ref, func(a *model.Actor) error {
		if a.State != model.StateCrashed && !a.State.Transitional() {
			return fmt.Errorf("actor %s is %s; revert applies to crashed or stuck actors", ref, a.State)
		}
		if len(a.Snapshots) == 0 {
			return fmt.Errorf("actor %s has no snapshot to revert to", ref)
		}
		a.State, a.Crash, a.Worker, a.Node, a.StateSince = model.StateSuspended, nil, "", "", c.since()
		return nil
	})
}

// Delete implements backend.Backend.
func (c *Cluster) Delete(_ context.Context, ref model.ActorRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, err := c.find(ref)
	if err != nil {
		return err
	}
	c.actors = append(c.actors[:i], c.actors[i+1:]...)
	return nil
}

// Drain implements backend.Backend.
func (c *Cluster) Drain(_ context.Context, worker string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.workers {
		w := &c.workers[i]
		if w.Pool+"/"+w.Name != worker {
			continue
		}
		w.State = "draining"
		for j := range c.actors {
			if c.actors[j].Worker == worker && c.actors[j].State == model.StateRunning {
				c.actors[j].State, c.actors[j].Worker, c.actors[j].StateSince = model.StateSuspended, "", c.since()
			}
		}
		return nil
	}
	return fmt.Errorf("worker %s not found", worker)
}

// Query implements backend.Metrics with smooth synthetic series.
func (c *Cluster) Query(_ context.Context, q backend.MetricQuery) ([]model.Series, error) {
	if q.Window <= 0 {
		q.Window = 6 * time.Hour
	}
	if q.Step <= 0 {
		q.Step = q.Window / 48
	}
	end := c.now().Add(-q.Offset)
	n := int(q.Window/q.Step) + 1
	gen := func(label string, f func(x float64) float64) model.Series {
		s := model.Series{Label: label}
		for i := 0; i < n; i++ {
			x := float64(i) / float64(n-1) // 0 → 1 across the window
			s.Points = append(s.Points, model.Point{T: end.Add(-q.Window + time.Duration(i)*q.Step), V: math.Round(f(x)*10) / 10})
		}
		return s
	}
	wobble := func(x, amp, freq float64) float64 { return amp * math.Sin(x*freq) }
	baseline := q.Offset > 0

	switch q.Metric {
	case backend.MetricResumeP95:
		if q.By == "template" {
			var out []model.Series
			for _, t := range c.templates {
				if q.Atespace != "" && t.Atespace != q.Atespace {
					continue
				}
				switch t.Name {
				case "agent-devbox-v11":
					out = append(out, gen(t.Name, func(x float64) float64 {
						if baseline {
							return 440 + wobble(x, 15, 9)
						}
						return 440 + 450*math.Pow(x, 2.2) + wobble(x, 12, 11)
					}))
				case "agent-devbox-v12":
					out = append(out, gen(t.Name, func(x float64) float64 { return 395 + wobble(x, 10, 13) }))
				case "mcp-enrich-v3":
					out = append(out, gen(t.Name, func(x float64) float64 { return 250 + wobble(x, 8, 7) }))
				case "codex-review":
					out = append(out, gen(t.Name, func(x float64) float64 { return 610 + wobble(x, 20, 5) }))
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
			return out, nil
		}
		return []model.Series{gen("resume p95 (ms)", func(x float64) float64 {
			if baseline {
				return 350 + wobble(x, 12, 9)
			}
			return 350 + 60*x + wobble(x, 10, 9)
		})}, nil
	case backend.MetricResumeFailures:
		return []model.Series{gen("resume failures (%)", func(x float64) float64 {
			if baseline || x < 0.9 {
				return 0
			}
			return 4.1 * (x - 0.9) * 10
		})}, nil
	case backend.MetricCapacity:
		if q.By == "pool" {
			return []model.Series{
				gen("gvisor-pool-a", func(x float64) float64 { return 70 + 25*x + wobble(x, 2, 8) }),
				gen("microvm-pool-b", func(x float64) float64 { return 25 + wobble(x, 6, 5) }),
			}, nil
		}
		return []model.Series{gen("capacity (% busy)", func(x float64) float64 {
			if baseline {
				return 73 + wobble(x, 3, 6)
			}
			return 72 + 23*math.Pow(x, 1.5) + wobble(x, 1.5, 8)
		})}, nil
	case backend.MetricCrashes:
		return []model.Series{gen("crashes", func(x float64) float64 {
			if baseline || x < 0.93 {
				return 0
			}
			if x < 0.97 {
				return 1
			}
			return 3
		})}, nil
	case backend.MetricRunning:
		return []model.Series{gen("running actors", func(x float64) float64 { return 30 + 8*x + wobble(x, 3, 10) })}, nil
	}
	return nil, fmt.Errorf("unknown metric %q", q.Metric)
}
