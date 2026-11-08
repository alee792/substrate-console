package mcpapp

// Templates are where people author what runs on Substrate: apps create
// actors through the API, but a person writes the template. ActorTemplates
// are immutable, so "editing" one is creating the next version.

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
	"github.com/alee792/substrate-console/internal/rules"
)

// TemplateRow is a template with what the console knows about its use.
type TemplateRow struct {
	model.Template
	Family       string   `json:"family"`
	Version      string   `json:"version,omitempty"`
	SandboxClass string   `json:"sandboxClass,omitempty"`
	Pools        []string `json:"pools"`
	Actors       int      `json:"actors"`
	Running      int      `json:"running"`
	Suspended    int      `json:"suspended"`
	Crashed      int      `json:"crashed"`
	ResumeP95Ms  float64  `json:"resumeP95Ms,omitempty"`
}

// TemplateList is list_templates' result.
type TemplateList struct {
	Kind      string        `json:"kind"` // "templates"
	Scope     string        `json:"scope"`
	Templates []TemplateRow `json:"templates"`
}

// TemplateDetail is get_template's result.
type TemplateDetail struct {
	Kind     string         `json:"kind"` // "template"
	Template TemplateRow    `json:"template"`
	Versions []TemplateRow  `json:"versions"`
	Actors   []ActorRow     `json:"actors"`
	Latency  *MetricResult  `json:"latency,omitempty"`
	Hint     string         `json:"hint,omitempty"`
	Actions  []rules.Action `json:"actions"`
}

// TemplateIn names one template.
type TemplateIn struct {
	Atespace string `json:"atespace" jsonschema:"the template's atespace"`
	Name     string `json:"name" jsonschema:"the template's name"`
}

// CreateTemplateIn is a template to create, or a draft to check.
type CreateTemplateIn struct {
	model.TemplateSpec
	DryRun bool `json:"dry_run,omitempty" jsonschema:"check and fill in the spec without creating anything; the view shows it as an editable draft"`
}

// Draft is create_template's dry-run result: the spec as it would be
// created, what is wrong with it, and the choices the form offers.
type Draft struct {
	Kind      string             `json:"kind"` // "draft"
	Spec      model.TemplateSpec `json:"spec"`
	Problems  map[string]string  `json:"problems"`
	Summary   string             `json:"summary"`
	Atespaces []string           `json:"atespaces"`
	Pools     []PoolOption       `json:"pools"`
	// Carried lists what a new version keeps from its source unchanged.
	Carried []string `json:"carried,omitempty"`
}

// PoolOption is a worker pool the template can be pinned to.
type PoolOption struct {
	Name         string `json:"name"`
	SandboxClass string `json:"sandboxClass,omitempty"`
	// Pinnable is false when the pool has no labels to select it by.
	Pinnable bool `json:"pinnable"`
}

// CreateActorIn creates one actor from a template.
type CreateActorIn struct {
	Atespace string `json:"atespace" jsonschema:"the template's atespace"`
	Template string `json:"template" jsonschema:"the template to create the actor from"`
	Name     string `json:"name,omitempty" jsonschema:"the actor's name; generated from the template family when omitted"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"report what would change without changing anything"`
}

// DeleteTemplateIn deletes one template.
type DeleteTemplateIn struct {
	Atespace string `json:"atespace" jsonschema:"the template's atespace"`
	Name     string `json:"name" jsonschema:"the template's name"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"report what would change without changing anything"`
}

var versionRE = regexp.MustCompile(`^(.*)-v(\d+)$`)

func family(name string) (string, string) {
	if m := versionRE.FindStringSubmatch(name); m != nil {
		return m[1], "v" + m[2]
	}
	return name, ""
}

// nextName suggests the next version's name: agent-devbox-v12 → -v13,
// codex-review → codex-review-v2, skipping names already taken.
func nextName(name string, taken func(string) bool) string {
	fam, ver := family(name)
	n := 1
	if ver != "" {
		n, _ = strconv.Atoi(ver[1:])
	}
	for {
		n++
		cand := fmt.Sprintf("%s-v%d", fam, n)
		if !taken(cand) {
			return cand
		}
	}
}

func (s *Server) addTemplateTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_templates",
		Description: "Actor templates: what each runs, its size and sandbox, its golden snapshot, how many actors use it, how many crashed, and resume p95.",
		Annotations: readOnly("List templates"),
		Meta:        toolMeta("Listing templates…", "Listed templates"),
	}, s.listTemplates)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_template",
		Description: "One template in detail: spec, golden snapshot, its actors, and how it compares with the other versions of the same template (resume latency, crashes).",
		Annotations: readOnly("Get template"),
		Meta:        toolMeta("Reading template…", "Read template"),
	}, s.getTemplate)
}

func (s *Server) addTemplateWrites(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_template",
		Description: "Create an actor template, or with dry_run=true check a draft and show it to the user as an editable form. " +
			"Templates are immutable: to change one, set `from` to the existing template and give a new name (the next version). " +
			"Omitted fields are filled from `from`. Always call with dry_run=true first and let the user review.",
		Annotations: write("Create template", false, false),
		Meta:        toolMeta("Drafting template…", "Drafted template"),
	}, s.createTemplate)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_actor",
		Description: "Create a suspended actor from a template. It starts from the template's golden snapshot when it has one. Use dry_run first.",
		Annotations: write("Create actor", false, false),
		Meta:        toolMeta("Creating actor…", "Created actor"),
	}, s.createActor)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_template",
		Description: "Delete a template with its golden snapshot. Actors created from it are listed in the dry run. Use dry_run first.",
		Annotations: write("Delete template", true, true),
		Meta:        toolMeta("Deleting template…", "Deleted template"),
	}, s.deleteTemplate)
}

// rows builds a TemplateRow for every template, with latest resume p95
// when metrics are available.
func (s *Server) rows(ctx context.Context, c *model.Cluster) []TemplateRow {
	p95 := map[string]float64{}
	if mt := s.metricsFor(c); mt != nil {
		if series, err := mt.Query(ctx, backend.MetricQuery{Metric: backend.MetricResumeP95, By: "template", Window: time.Hour, Step: 5 * time.Minute}); err == nil {
			for _, se := range series {
				if n := len(se.Points); n > 0 {
					p95[se.Label] = se.Points[n-1].V
				}
			}
		}
	}
	out := make([]TemplateRow, 0, len(c.Templates))
	for _, t := range c.Templates {
		r := TemplateRow{Template: t, Pools: []string{}, ResumeP95Ms: p95[t.Name]}
		r.Family, r.Version = family(t.Name)
		classes := map[string]bool{}
		for _, p := range c.Pools {
			if matches(t.WorkerSelector, p.Labels) {
				r.Pools = append(r.Pools, p.Name)
				classes[p.SandboxClass] = true
			}
		}
		if len(classes) == 1 {
			for k := range classes {
				r.SandboxClass = k
			}
		}
		for _, a := range c.Actors {
			if a.Atespace != t.Atespace || a.Template != t.Name {
				continue
			}
			r.Actors++
			switch a.State {
			case model.StateRunning:
				r.Running++
			case model.StateSuspended:
				r.Suspended++
			case model.StateCrashed:
				r.Crashed++
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Atespace != out[j].Atespace {
			return out[i].Atespace < out[j].Atespace
		}
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return versionNum(out[i].Version) > versionNum(out[j].Version)
	})
	return out
}

func versionNum(v string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "v"))
	return n
}

func matches(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (s *Server) listTemplates(ctx context.Context, _ *mcp.CallToolRequest, in ScopeIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := TemplateList{Kind: "templates", Scope: in.Atespace, Templates: []TemplateRow{}}
	var b strings.Builder
	for _, r := range s.rows(ctx, c) {
		if in.Atespace != "" && r.Atespace != in.Atespace {
			continue
		}
		out.Templates = append(out.Templates, r)
		fmt.Fprintf(&b, "- %s/%s: %s, %s, %d actors (%d running, %d crashed), golden %s", r.Atespace, r.Name, r.Image, size(r.Template), r.Actors, r.Running, r.Crashed, r.Golden.State)
		if r.ResumeP95Ms > 0 {
			fmt.Fprintf(&b, ", resume p95 %.0f ms", r.ResumeP95Ms)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		b.WriteString("No templates.")
	}
	return &mcp.CallToolResult{Content: text(b.String())}, out, nil
}

func size(t model.Template) string {
	cpu := fmt.Sprintf("%g CPU", float64(t.CPUMillis)/1000)
	if t.MemoryLimit == 0 {
		return cpu
	}
	return cpu + ", " + bytes(t.MemoryLimit)
}

func (s *Server) getTemplate(ctx context.Context, _ *mcp.CallToolRequest, in TemplateIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	all := s.rows(ctx, c)
	var d TemplateDetail
	found := false
	for _, r := range all {
		if r.Atespace == in.Atespace && r.Name == in.Name {
			d.Template, found = r, true
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("template %s/%s not found", in.Atespace, in.Name)
	}
	d.Kind = "template"
	d.Versions = []TemplateRow{}
	for _, r := range all {
		if r.Atespace == in.Atespace && r.Family == d.Template.Family {
			d.Versions = append(d.Versions, r)
		}
	}
	d.Actors = []ActorRow{}
	for _, a := range c.Actors {
		if a.Atespace == in.Atespace && a.Template == in.Name {
			ny, why := needsYou(a, c.FetchedAt)
			d.Actors = append(d.Actors, ActorRow{Actor: a, NeedsYou: ny, Reason: why})
		}
	}
	sort.SliceStable(d.Actors, func(i, j int) bool {
		if ri, rj := stateRank(d.Actors[i]), stateRank(d.Actors[j]); ri != rj {
			return ri < rj
		}
		return lastActive(d.Actors[i]).After(lastActive(d.Actors[j]))
	})
	if len(d.Actors) > 12 {
		d.Actors = d.Actors[:12]
	}
	if mt := s.metricsFor(c); mt != nil {
		series, err := mt.Query(ctx, backend.MetricQuery{Metric: backend.MetricResumeP95, By: "template", Atespace: in.Atespace, Window: 24 * time.Hour, Step: 30 * time.Minute})
		if err == nil {
			m := &MetricResult{Kind: "metric", Metric: string(backend.MetricResumeP95), Title: "Resume p95", Unit: "ms", By: "template", Window: "24h"}
			for _, se := range series {
				if f, _ := family(se.Label); f == d.Template.Family {
					m.Series = append(m.Series, se)
				}
			}
			if len(m.Series) > 0 {
				d.Latency = m
			}
		}
	}
	d.Hint = versionHint(d.Template, d.Versions)
	ref := map[string]any{"atespace": in.Atespace, "name": in.Name}
	if !s.opts.ReadOnly {
		d.Actions = []rules.Action{
			{Label: "Delete", Tool: "delete_template", Args: ref, Destructive: true},
			{Label: "New version", Tool: "create_template", Args: map[string]any{"atespace": in.Atespace, "from": in.Name}, Primary: d.Template.Golden.State == "failed"},
		}
		if d.Template.Golden.State != "failed" {
			d.Actions = append(d.Actions, rules.Action{Label: "New actor", Tool: "create_actor", Args: map[string]any{"atespace": in.Atespace, "template": in.Name}, Primary: true})
		}
	} else {
		d.Actions = []rules.Action{}
	}

	t := d.Template
	var b strings.Builder
	fmt.Fprintf(&b, "Template %s/%s: %s, %s", t.Atespace, t.Name, t.Image, size(t.Template))
	if t.SandboxClass != "" {
		fmt.Fprintf(&b, ", %s", t.SandboxClass)
	}
	fmt.Fprintf(&b, ". Golden snapshot %s", t.Golden.State)
	if t.Golden.Error != "" {
		fmt.Fprintf(&b, " (%s)", t.Golden.Error)
	}
	fmt.Fprintf(&b, ". %d actors: %d running, %d suspended, %d crashed.", t.Actors, t.Running, t.Suspended, t.Crashed)
	for _, v := range d.Versions {
		if v.Name != t.Name {
			fmt.Fprintf(&b, "\nOther version %s: %s, %d actors, %d crashed", v.Name, size(v.Template), v.Actors, v.Crashed)
			if v.ResumeP95Ms > 0 {
				fmt.Fprintf(&b, ", resume p95 %.0f ms", v.ResumeP95Ms)
			}
		}
	}
	if d.Hint != "" {
		b.WriteString("\n" + d.Hint)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, d, nil
}

func lastActive(r ActorRow) time.Time {
	if r.LastActive != nil {
		return *r.LastActive
	}
	return time.Time{}
}

// versionHint compares a template with the newest other version.
func versionHint(t TemplateRow, versions []TemplateRow) string {
	for _, v := range versions {
		if v.Name == t.Name {
			continue
		}
		var parts []string
		if t.Crashed > 0 && v.Crashed == 0 && v.Actors >= 5 {
			p := fmt.Sprintf("%d crashes here, none on %s (%d actors)", t.Crashed, v.Name, v.Actors)
			if t.MemoryLimit > 0 && v.MemoryLimit > t.MemoryLimit {
				p += fmt.Sprintf(", which has %s of memory instead of %s", bytes(v.MemoryLimit), bytes(t.MemoryLimit))
			}
			parts = append(parts, p+".")
		}
		if t.ResumeP95Ms > 0 && v.ResumeP95Ms > 0 && t.ResumeP95Ms > 1.3*v.ResumeP95Ms {
			parts = append(parts, fmt.Sprintf("Resumes are %.1f× slower than on %s.", t.ResumeP95Ms/v.ResumeP95Ms, v.Name))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (s *Server) createTemplate(ctx context.Context, _ *mcp.CallToolRequest, in CreateTemplateIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	spec := in.TemplateSpec
	d := Draft{Kind: "draft", Problems: map[string]string{}, Atespaces: c.Atespaces, Pools: []PoolOption{}}
	for _, p := range c.Pools {
		d.Pools = append(d.Pools, PoolOption{Name: p.Name, SandboxClass: p.SandboxClass, Pinnable: len(p.Labels) > 0})
	}
	if spec.Atespace == "" && len(c.Atespaces) == 1 {
		spec.Atespace = c.Atespaces[0]
	}
	taken := func(name string) bool { return findTemplate(c, spec.Atespace, name) != nil }

	// A new version starts as a copy of its source.
	if spec.From != "" {
		src := findTemplate(c, spec.Atespace, spec.From)
		if src == nil {
			d.Problems["from"] = fmt.Sprintf("No template %s in %s.", spec.From, spec.Atespace)
		} else {
			if spec.Name == "" {
				spec.Name = nextName(src.Name, taken)
			}
			if spec.Image == "" {
				spec.Image = src.Image
			}
			if len(src.Containers) > 0 {
				if spec.Command == nil {
					spec.Command = src.Containers[0].Command
				}
				if spec.Args == nil {
					spec.Args = src.Containers[0].Args
				}
				if len(src.Containers[0].EnvNames) > 0 && spec.Env == nil {
					d.Carried = append(d.Carried, "environment ("+strings.Join(src.Containers[0].EnvNames, ", ")+")")
				}
				if src.Containers[0].Probe != "" {
					d.Carried = append(d.Carried, "wakeup probe "+src.Containers[0].Probe)
				}
				if len(src.Containers) > 1 {
					d.Carried = append(d.Carried, fmt.Sprintf("%d more containers", len(src.Containers)-1))
				}
			}
			if spec.CPUMillis == 0 {
				spec.CPUMillis = src.CPUMillis
			}
			if spec.MemoryMiB == 0 {
				spec.MemoryMiB = src.MemoryLimit >> 20
			}
			if spec.Pool == "" {
				for _, p := range c.Pools {
					if len(src.WorkerSelector) > 0 && matches(src.WorkerSelector, p.Labels) {
						spec.Pool = p.Name
						break
					}
				}
			}
			if spec.Fidelity == "" {
				spec.Fidelity = src.Fidelity
			}
			if spec.Golden == nil {
				spec.Golden = ptr(src.Golden.State != "off")
			}
		}
	} else if in.DryRun && spec.Image == "" && spec.CPUMillis == 0 && spec.MemoryMiB == 0 {
		// A blank form: sensible starting values.
		spec.CPUMillis, spec.MemoryMiB, spec.Fidelity = 1000, 2048, "memory"
	}
	if spec.Golden == nil {
		spec.Golden = ptr(true)
	}

	switch {
	case spec.Atespace == "":
		d.Problems["atespace"] = "Pick an atespace."
	case !contains(c.Atespaces, spec.Atespace):
		d.Problems["atespace"] = fmt.Sprintf("No atespace %s.", spec.Atespace)
	}
	switch {
	case spec.Name == "":
		d.Problems["name"] = "Name it."
	case len(spec.Name) > 63 || !nameRE.MatchString(spec.Name):
		d.Problems["name"] = "Lowercase letters, digits and dashes, up to 63."
	case taken(spec.Name):
		d.Problems["name"] = "Taken. Templates can't change; use a new version name."
	}
	if strings.TrimSpace(spec.Image) == "" {
		d.Problems["image"] = "An image is required."
	}
	if spec.CPUMillis <= 0 {
		d.Problems["cpuMillicores"] = "Set a CPU limit."
	}
	if spec.MemoryMiB <= 0 {
		d.Problems["memoryMiB"] = "Set a memory limit."
	}
	var selector map[string]string
	if spec.Pool != "" {
		ok := false
		for _, p := range c.Pools {
			if p.Name == spec.Pool {
				ok, selector = true, p.Labels
				if len(p.Labels) == 0 {
					d.Problems["pool"] = "This pool has no labels to select it by."
				}
			}
		}
		if !ok {
			d.Problems["pool"] = fmt.Sprintf("No pool %s.", spec.Pool)
		}
	}
	switch spec.Fidelity {
	case "", "memory", "rootfs", "volumes":
	default:
		d.Problems["fidelity"] = "One of memory, rootfs or volumes."
	}
	d.Spec = spec
	if spec.From != "" {
		d.Summary = fmt.Sprintf("New version of %s", spec.From)
	} else {
		d.Summary = "New template"
	}

	if in.DryRun || len(d.Problems) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "Draft %s/%s: %s, %g CPU, %d MiB", spec.Atespace, spec.Name, spec.Image, float64(spec.CPUMillis)/1000, spec.MemoryMiB)
		if spec.Pool != "" {
			fmt.Fprintf(&b, ", pool %s", spec.Pool)
		}
		fmt.Fprintf(&b, ", golden snapshot %v.", *spec.Golden)
		if len(d.Problems) > 0 {
			keys := make([]string, 0, len(d.Problems))
			for k := range d.Problems {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString(" Problems:")
			for _, k := range keys {
				fmt.Fprintf(&b, " %s: %s", k, d.Problems[k])
			}
		} else {
			b.WriteString(" Ready to create; the user reviews it in the view.")
		}
		return &mcp.CallToolResult{Content: text(b.String())}, d, nil
	}

	if err := s.opts.Backend.CreateTemplate(ctx, spec, selector); err != nil {
		return nil, nil, fmt.Errorf("create template: %s", explain(err, "can't create templates in "+spec.Atespace))
	}
	s.invalidate()
	return s.getTemplate(ctx, nil, TemplateIn{Atespace: spec.Atespace, Name: spec.Name})
}

func (s *Server) createActor(ctx context.Context, _ *mcp.CallToolRequest, in CreateActorIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	t := findTemplate(c, in.Atespace, in.Template)
	if t == nil {
		return nil, nil, fmt.Errorf("template %s/%s not found", in.Atespace, in.Template)
	}
	if in.Name == "" {
		fam, _ := family(in.Template)
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for {
			in.Name = fmt.Sprintf("%s-%04x", fam, r.Intn(0xffff))
			if _, ok := findActor(c, model.ActorRef{Atespace: in.Atespace, Name: in.Name}); !ok {
				break
			}
		}
	}
	ref := model.ActorRef{Atespace: in.Atespace, Name: in.Name}
	res := ActionResult{Kind: "action", Verb: "create", Tool: "create_actor", DryRun: in.DryRun,
		Args: map[string]any{"atespace": in.Atespace, "template": in.Template, "name": in.Name}}
	tg := Target{ActorRef: ref, State: model.StateSuspended, OK: true}
	switch {
	case !nameRE.MatchString(in.Name):
		tg.OK, tg.Reason = false, "invalid name"
	case t.Golden.State == "failed":
		tg.OK, tg.Reason = false, "golden snapshot failed"
	}
	if _, exists := findActor(c, ref); exists {
		tg.OK, tg.Reason = false, "already exists"
	}
	res.Targets = []Target{tg}
	if in.DryRun {
		res.Summary = fmt.Sprintf("Create %s from %s?", in.Name, in.Template)
		switch {
		case !tg.OK:
		case t.Golden.State == "ready":
			res.Note = "It starts suspended, from the golden snapshot."
		case t.Golden.State == "building":
			res.Note = "It starts suspended. The golden snapshot is still building."
		default:
			res.Note = "It starts suspended and cold-boots on first resume."
		}
		return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
	}
	if !tg.OK {
		return nil, nil, fmt.Errorf("can't create %s: %s", ref, tg.Reason)
	}
	if err := s.opts.Backend.CreateActor(ctx, ref, in.Template); err != nil {
		return nil, nil, fmt.Errorf("create actor: %s", explain(err, "can't create actors in "+ref.Atespace))
	}
	s.invalidate()
	res.Summary = fmt.Sprintf("Created %s.", in.Name)
	return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
}

func (s *Server) deleteTemplate(ctx context.Context, _ *mcp.CallToolRequest, in DeleteTemplateIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	if findTemplate(c, in.Atespace, in.Name) == nil {
		return nil, nil, fmt.Errorf("template %s/%s not found", in.Atespace, in.Name)
	}
	res := ActionResult{Kind: "action", Verb: "delete template", Tool: "delete_template", DryRun: in.DryRun,
		Args: map[string]any{"atespace": in.Atespace, "name": in.Name}, Targets: []Target{}}
	using := 0
	for _, a := range c.Actors {
		if a.Atespace == in.Atespace && a.Template == in.Name {
			using++
		}
	}
	res.Note = "Its golden snapshot is deleted with it."
	if using > 0 {
		res.Note = fmt.Sprintf("%d actors were created from it. Their own snapshots stay, but none can be created from it again.", using)
	}
	if in.DryRun {
		res.Summary = fmt.Sprintf("Delete %s?", in.Name)
		return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
	}
	if err := s.opts.Backend.DeleteTemplate(ctx, in.Atespace, in.Name); err != nil {
		return nil, nil, fmt.Errorf("delete template: %s", explain(err, "can't delete templates in "+in.Atespace))
	}
	s.invalidate()
	res.Summary = fmt.Sprintf("Deleted %s.", in.Name)
	return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
}
