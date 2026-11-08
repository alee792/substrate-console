package mcpapp

// Agents are Canopy's: Anthropic or OpenAI agents that run on Substrate.
// The console shows them and drives sessions through the lab APIs, as an
// application would. It has no agent format of its own: a definition is
// the lab API's agent object, changed in git (agent.anthropic.yaml or
// agent.openai.yaml, then canopy push) or through that API.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alee792/substrate-console/internal/agents"
	"github.com/alee792/substrate-console/internal/rules"
)

const agentInstructions = `Agents run on Canopy, on this cluster. An agent is an Anthropic or OpenAI agent: list_agents and get_agent show each one, its versions (a push from git or an edit through the API) and its definition as that API returns it. The console doesn't edit definitions: they change in git (agent.anthropic.yaml or agent.openai.yaml, then canopy push) or through the lab API.
To try an agent, call start_session with dry_run=true and a message; the user reviews the playground (version, environment, repository) and starts it. get_session shows the transcript and what the session waits on. send_message, interrupt_session, suspend_session and resume_session act at once; end_session takes dry_run. Approvals are the user's alone: they answer them on the session card, and you can't. Tell the user what a session waits on and leave the decision to them.`

// AgentList is list_agents' result.
type AgentList struct {
	Kind   string         `json:"kind"` // "agents"
	Canopy string         `json:"canopy"`
	Agents []agents.Agent `json:"agents"`
}

// AgentPage is get_agent's result.
type AgentPage struct {
	Kind string `json:"kind"` // "agent"
	agents.AgentDetail
	Actions []rules.Action `json:"actions"`
}

// SessionPage is get_session's result, and what the session tools return.
type SessionPage struct {
	Kind string `json:"kind"` // "session"
	agents.SessionDetail
	Actions []rules.Action `json:"actions"`
	// Done says what the call just did.
	Done string `json:"done,omitempty"`
}

// Playground is start_session's dry run: the session as it would start,
// and the choices the form offers.
type Playground struct {
	Kind         string               `json:"kind"` // "playground"
	Agent        agents.Agent         `json:"agent"`
	Versions     []int                `json:"versions"`
	Environments []agents.Environment `json:"environments"`
	StartIn
	Problems map[string]string `json:"problems"`
}

// AgentIn names an agent.
type AgentIn struct {
	Agent   string `json:"agent" jsonschema:"the agent's id"`
	Version int    `json:"version,omitempty" jsonschema:"show the definition at this version; omit for the latest"`
}

// SessionIn names a session.
type SessionIn struct {
	Session string `json:"session" jsonschema:"the session's id"`
}

// StartIn is a session to start.
type StartIn struct {
	Agent       string   `json:"agent" jsonschema:"the agent's id"`
	Version     int      `json:"version,omitempty" jsonschema:"the agent version to run (Anthropic agents); omit for the latest"`
	Environment string   `json:"environment,omitempty" jsonschema:"the Anthropic environment's id; it decides what the session can reach"`
	Network     string   `json:"network,omitempty" jsonschema:"OpenAI agents: enabled (every host over HTTP and TLS) or disabled (only what the platform needs); allowed_domains make it restricted"`
	Domains     []string `json:"allowed_domains,omitempty" jsonschema:"OpenAI agents: hosts the session may reach besides the platform's"`
	Repository  string   `json:"repository,omitempty" jsonschema:"Anthropic agents: a GitHub repository to check out, as its https URL"`
	Branch      string   `json:"branch,omitempty" jsonschema:"the branch to check out; omit for the default branch"`
	Message     string   `json:"message,omitempty" jsonschema:"the first message; required unless dry_run"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"fill in and check the session without starting it; the view shows it as the playground form"`
}

// SendIn sends a message.
type SendIn struct {
	Session string `json:"session" jsonschema:"the session's id"`
	Text    string `json:"text" jsonschema:"the message"`
}

// AnswerIn answers an approval.
type AnswerIn struct {
	Session  string `json:"session" jsonschema:"the session's id"`
	Event    string `json:"event" jsonschema:"the tool use event to answer, from get_session's approvals"`
	Decision string `json:"decision" jsonschema:"allow or deny"`
	Reason   string `json:"reason,omitempty" jsonschema:"for deny: what to tell the agent"`
}

// EndIn ends a session.
type EndIn struct {
	Session string `json:"session" jsonschema:"the session's id"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"report what would happen without doing it"`
}

func (s *Server) addAgentTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_agents",
		Description: "Agents on Canopy: Anthropic and OpenAI agents that run on this cluster, with their harness, model, latest version, where they're defined (a git repository, or the API) and recent sessions.",
		Annotations: readOnly("List agents"),
		Meta:        toolMeta("Listing agents…", "Listed agents"),
	}, s.listAgents)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_agent",
		Description: "One agent: its versions with the commit each push came from, its definition as its lab API returns it, what its bundle installs (instructions, skills, MCP servers, subagents), and recent sessions.",
		Annotations: readOnly("Get agent"),
		Meta:        toolMeta("Reading agent…", "Read agent"),
	}, s.getAgent)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_session",
		Description: "One agent session: the transcript (messages, commands, tool calls, file changes), what it's waiting on, its spend, and the sandbox it runs in on Substrate.",
		Annotations: readOnly("Get session"),
		Meta:        toolMeta("Reading session…", "Read session"),
	}, s.getSession)
	if s.opts.ReadOnly {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "start_session",
		Description: "Start a session with an agent and send the first message, through the agent's own API. " +
			"With dry_run=true, fill in and check it and show it to the user as the playground form. Always dry-run first. " +
			"The environment decides what the session can reach; the agent can't widen it.",
		Annotations: write("Start session", false, false),
		Meta:        toolMeta("Preparing the session…", "Ready"),
	}, s.startSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a message into a session. It queues behind a running turn.",
		Annotations: write("Send message", false, false),
		Meta:        toolMeta("Sending…", "Sent"),
	}, s.sendMessage)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "answer_approval",
		Description: "Allow or deny a tool call a session is waiting on. The session card's buttons call it; the model never does.",
		Annotations: write("Answer approval", false, false),
		// App-only, so a model steered by what an agent read (get_session
		// shows it the transcript) can't approve that agent's own tool calls.
		Meta: appOnlyMeta(),
	}, s.answerApproval)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "interrupt_session",
		Description: "Stop the turn a session is running. The session stays open.",
		Annotations: write("Interrupt session", false, true),
		Meta:        toolMeta("Interrupting…", "Interrupted"),
	}, s.interruptSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "suspend_session",
		Description: "Snapshot a session's sandbox and stop it. The next message resumes it where it was.",
		Annotations: write("Suspend session", false, true),
		Meta:        toolMeta("Suspending…", "Suspended"),
	}, s.suspendSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "resume_session",
		Description: "Restore a suspended session's sandbox now, instead of on its next message.",
		Annotations: write("Resume session", false, true),
		Meta:        toolMeta("Resuming…", "Resumed"),
	}, s.resumeSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "end_session",
		Description: "End a session: delete it and its sandbox. Use dry_run first.",
		Annotations: write("End session", true, true),
		Meta:        toolMeta("Ending session…", "Ended session"),
	}, s.endSession)
}

// appOnlyMeta hides a tool from the model; only the view can call it.
func appOnlyMeta() mcp.Meta {
	return mcp.Meta{
		"ui":                      map[string]any{"visibility": []string{"app"}},
		"openai/visibility":       "private",
		"openai/widgetAccessible": true,
	}
}

func (s *Server) listAgents(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	list, err := s.opts.Agents.Agents(ctx)
	if err != nil {
		return nil, nil, canopyErr("list agents", err)
	}
	if list == nil {
		list = []agents.Agent{}
	}
	res := AgentList{Kind: "agents", Canopy: s.opts.Agents.Host(), Agents: list}
	var b strings.Builder
	fmt.Fprintf(&b, "%d agents on %s.", len(list), res.Canopy)
	for _, a := range list {
		fmt.Fprintf(&b, "\n- %s (%s): %s, %s, v%d, %s", a.Name, a.ID, a.Harness, a.Model, a.Version, where(a.Source))
		if a.Active > 0 {
			fmt.Fprintf(&b, ", %d active sessions", a.Active)
		}
	}
	return &mcp.CallToolResult{Content: text(b.String())}, res, nil
}

func where(src *agents.Source) string {
	if src == nil {
		return "defined through the API"
	}
	w := "from " + src.URL
	if src.Path != "" {
		w += " " + src.Path
	}
	if src.Ref != "" {
		w += " @ " + src.Ref
	}
	return w
}

func (s *Server) getAgent(ctx context.Context, _ *mcp.CallToolRequest, in AgentIn) (*mcp.CallToolResult, any, error) {
	if in.Agent == "" {
		return nil, nil, errors.New("agent is required")
	}
	d, err := s.opts.Agents.Agent(ctx, in.Agent, in.Version)
	if err != nil {
		return nil, nil, canopyErr("read agent "+in.Agent, err)
	}
	res := AgentPage{Kind: "agent", AgentDetail: *d, Actions: []rules.Action{}}
	if !s.opts.ReadOnly {
		res.Actions = append(res.Actions, rules.Action{Label: "Try it", Tool: "start_session", Primary: true,
			Args: map[string]any{"agent": d.Agent.ID}})
	}
	a := d.Agent
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) runs on %s with %s, through the %s. Latest is v%d, %s.", a.Name, a.ID, a.Harness, a.Model, labTitle(a.Lab), a.Version, where(a.Source))
	for _, v := range d.Versions {
		fmt.Fprintf(&b, "\n- v%d %s", v.Version, versionLine(v))
	}
	if len(d.Definition) > 0 {
		fmt.Fprintf(&b, "\nDefinition at v%d:\n%s", d.Shown, d.Definition)
	}
	if d.DefinitionNote != "" {
		b.WriteString("\n" + d.DefinitionNote)
	}
	return &mcp.CallToolResult{Content: text(b.String())}, res, nil
}

func labTitle(lab string) string {
	if lab == agents.LabOpenAI {
		return "OpenAI API"
	}
	return "Anthropic API"
}

func versionLine(v agents.Version) string {
	if v.Origin == "api" || v.Source == nil {
		return "edited through the API, " + v.CreatedAt.Format(time.RFC3339)
	}
	c := v.Source.Commit
	if len(c) > 7 {
		c = c[:7]
	}
	return fmt.Sprintf("pushed from %s on %s, %s", c, v.Source.Ref, v.CreatedAt.Format(time.RFC3339))
}

func (s *Server) getSession(ctx context.Context, _ *mcp.CallToolRequest, in SessionIn) (*mcp.CallToolResult, any, error) {
	return s.sessionResult(ctx, in.Session, "")
}

// sessionResult reads the session for the view, after done.
func (s *Server) sessionResult(ctx context.Context, id, done string) (*mcp.CallToolResult, any, error) {
	if id == "" {
		return nil, nil, errors.New("session is required")
	}
	d, err := s.opts.Agents.Session(ctx, id)
	if err != nil {
		return nil, nil, canopyErr("read session "+id, err)
	}
	res := SessionPage{Kind: "session", SessionDetail: *d, Actions: s.sessionActions(d), Done: done}
	return &mcp.CallToolResult{Content: text(sessionText(res))}, res, nil
}

func (s *Server) sessionActions(d *agents.SessionDetail) []rules.Action {
	out := []rules.Action{}
	sb := d.Session.Sandbox
	if sb.Provider == "substrate" && sb.Actor != "" && sb.Atespace != "" {
		out = append(out, rules.Action{Label: "Open actor", Tool: "get_actor", Args: map[string]any{"atespace": sb.Atespace, "name": sb.Actor}})
	}
	if s.opts.ReadOnly {
		return out
	}
	id := map[string]any{"session": d.Session.ID}
	switch {
	case d.Session.Status == "in_progress":
		out = append(out, rules.Action{Label: "Stop", Tool: "interrupt_session", Args: id})
	case sb.Status == "suspended":
		out = append(out, rules.Action{Label: "Resume", Tool: "resume_session", Args: id})
	case sb.Status == "ready" && d.Session.Status == "idle":
		out = append(out, rules.Action{Label: "Suspend", Tool: "suspend_session", Args: id})
	}
	out = append(out, rules.Action{Label: "End", Tool: "end_session", Args: id, Destructive: true})
	return out
}

func sessionText(p SessionPage) string {
	var b strings.Builder
	ss := p.Session
	name := ss.AgentID
	if p.Agent != nil {
		name = p.Agent.Name
	}
	if p.Done != "" {
		b.WriteString(p.Done + " ")
	}
	fmt.Fprintf(&b, "Session %s with %s: %s, sandbox %s, %s spent.", ss.ID, name, statusWords(ss.Status), ss.Sandbox.Status, cents(ss.SpendCents))
	if ss.Error != "" {
		b.WriteString(" Error: " + ss.Error)
	}
	t := p.Transcript
	from := 0
	if len(t.Turns) > 3 {
		from = len(t.Turns) - 3
		fmt.Fprintf(&b, "\n(%d earlier turns)", from+t.Earlier)
	}
	for _, turn := range t.Turns[from:] {
		fmt.Fprintf(&b, "\n> %s\n", oneLine(turn.Input, 300))
		for _, it := range turn.Items {
			switch it.Kind {
			case "assistant_message":
				fmt.Fprintf(&b, "  %s\n", oneLine(it.Text, 600))
			case "command_execution":
				fmt.Fprintf(&b, "  $ %s", oneLine(it.Command, 200))
				if it.ExitCode != nil && *it.ExitCode != 0 {
					fmt.Fprintf(&b, " (exit %d)", *it.ExitCode)
				}
				b.WriteString("\n")
			case "file_change":
				for _, c := range it.Changes {
					fmt.Fprintf(&b, "  %s %s\n", c.Kind, c.Path)
				}
			case "tool_call", "mcp_call", "function_call", "web_search":
				fmt.Fprintf(&b, "  %s %s\n", toolName(it), oneLine(it.Args, 160))
			}
		}
		if turn.Status != "completed" {
			fmt.Fprintf(&b, "  [%s%s]\n", turn.Status, suffix(": ", turn.Error))
		}
	}
	for _, a := range p.Approvals {
		what := a.Command
		if what == "" {
			what = a.Tool + " " + a.Input
		}
		fmt.Fprintf(&b, "\nWaiting for the user to approve, on the session card: %s", oneLine(what, 300))
	}
	if p.Blocked != "" {
		b.WriteString("\n" + p.Blocked)
	}
	return strings.TrimRight(b.String(), "\n")
}

func toolName(it *agents.Item) string {
	if it.Server != "" {
		return it.Server + "." + it.Name
	}
	if it.Name != "" {
		return it.Name
	}
	return it.Kind
}

func statusWords(st string) string {
	switch st {
	case "in_progress":
		return "working"
	case "requires_action":
		return "waiting"
	}
	return st
}

func cents(c float64) string {
	if c < 100 {
		return fmt.Sprintf("%.1f¢", c)
	}
	return fmt.Sprintf("$%.2f", c/100)
}

func suffix(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return strings.ToValidUTF8(s[:n-1], "") + "…"
	}
	return s
}

func (s *Server) startSession(ctx context.Context, _ *mcp.CallToolRequest, in StartIn) (*mcp.CallToolResult, any, error) {
	if in.Agent == "" {
		return nil, nil, errors.New("agent is required")
	}
	d, err := s.opts.Agents.Agent(ctx, in.Agent, 0)
	if err != nil {
		return nil, nil, canopyErr("read agent "+in.Agent, err)
	}
	pg := Playground{Kind: "playground", Agent: d.Agent, Versions: []int{}, Environments: []agents.Environment{}, StartIn: in, Problems: map[string]string{}}
	pg.DryRun = true
	for _, v := range d.Versions {
		pg.Versions = append(pg.Versions, v.Version)
	}
	a := d.Agent
	if a.Lab == agents.LabAnthropic {
		if pg.Environments, err = s.opts.Agents.Environments(ctx); err != nil {
			return nil, nil, canopyErr("list environments", err)
		}
		pg.Network, pg.Domains = "", nil
		if pg.Environment == "" && len(pg.Environments) > 0 {
			pg.Environment = pg.Environments[0].ID
		}
		if pg.Repository == "" && in.DryRun && a.Source != nil && strings.HasPrefix(a.Source.URL, "https://github.com/") {
			// An agent kept in a repository usually works on it.
			pg.Repository, pg.Branch = strings.TrimSuffix(a.Source.URL, ".git"), a.Source.Ref
		}
	} else {
		pg.Version, pg.Environment, pg.Repository, pg.Branch = 0, "", "", ""
		if pg.Network == "" {
			pg.Network = "disabled"
		}
	}
	p := pg.Problems
	if strings.TrimSpace(pg.Message) == "" && !in.DryRun {
		p["message"] = "Say what the agent should do."
	}
	if a.Lab == agents.LabAnthropic {
		if pg.Environment == "" {
			p["environment"] = "There are no environments yet. Create one with the Anthropic API (client.beta.environments.create) and it shows up here."
		} else if !hasEnv(pg.Environments, pg.Environment) {
			p["environment"] = fmt.Sprintf("No environment %s.", pg.Environment)
		}
		if pg.Version != 0 && !hasInt(pg.Versions, pg.Version) {
			p["version"] = fmt.Sprintf("%s has no version %d.", a.Name, pg.Version)
		}
		if repo, err := agents.GitHubRepo(pg.Repository); err != nil {
			p["repository"] = capFirst(err.Error()) + "."
		} else {
			pg.Repository = repo
		}
	} else if pg.Network != "enabled" && pg.Network != "disabled" {
		p["network"] = "Network is enabled or disabled."
	}
	if in.DryRun || len(p) > 0 {
		return &mcp.CallToolResult{Content: text(playgroundText(pg))}, pg, nil
	}

	id, err := s.opts.Agents.StartSession(ctx, a, agents.Start{Agent: a.ID, Version: pg.Version, Environment: pg.Environment,
		Network: pg.Network, Domains: pg.Domains, Repository: pg.Repository, Branch: pg.Branch,
		Message: strings.TrimSpace(pg.Message), Title: oneLine(pg.Message, 80)})
	if err != nil {
		// Canopy's refusal, such as an agent needing hosts the environment
		// doesn't allow, goes back on the form.
		field := "start"
		if a.Lab == agents.LabOpenAI {
			field = "network"
		} else if strings.Contains(strings.ToLower(agents.Explain(err)), "environment") {
			field = "environment"
		}
		p[field] = agents.Explain(err)
		return &mcp.CallToolResult{Content: text(playgroundText(pg))}, pg, nil
	}
	return s.sessionResult(ctx, id, fmt.Sprintf("Started a session with %s.", a.Name))
}

func playgroundText(pg Playground) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Playground for %s (%s, %s).", pg.Agent.Name, pg.Agent.Harness, pg.Agent.Model)
	if pg.Agent.Lab == agents.LabAnthropic {
		v := "latest"
		if pg.Version > 0 {
			v = fmt.Sprintf("v%d", pg.Version)
		}
		fmt.Fprintf(&b, " Version %s, environment %s.", v, orDash(pg.Environment))
		if pg.Repository != "" {
			fmt.Fprintf(&b, " Repository %s%s.", pg.Repository, suffix(" @ ", pg.Branch))
		}
	} else {
		fmt.Fprintf(&b, " Network %s.", pg.Network)
	}
	for k, v := range pg.Problems {
		fmt.Fprintf(&b, "\nProblem with %s: %s", k, v)
	}
	b.WriteString("\nThe user starts it from the form; or call start_session again with dry_run=false.")
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func hasEnv(list []agents.Environment, id string) bool {
	for _, e := range list {
		if e.ID == id {
			return true
		}
	}
	return false
}

func hasInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) sendMessage(ctx context.Context, _ *mcp.CallToolRequest, in SendIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, nil, errors.New("text is required")
	}
	if err := s.opts.Agents.Send(ctx, in.Session, strings.TrimSpace(in.Text)); err != nil {
		return nil, nil, canopyErr("send", err)
	}
	return s.sessionResult(ctx, in.Session, "Sent.")
}

func (s *Server) answerApproval(ctx context.Context, _ *mcp.CallToolRequest, in AnswerIn) (*mcp.CallToolResult, any, error) {
	if in.Decision != "allow" && in.Decision != "deny" {
		return nil, nil, errors.New("decision is allow or deny")
	}
	if in.Event == "" {
		return nil, nil, errors.New("event is required")
	}
	if err := s.opts.Agents.Answer(ctx, in.Session, in.Event, in.Decision == "allow", in.Reason); err != nil {
		return nil, nil, canopyErr("answer", err)
	}
	done := "Allowed."
	if in.Decision == "deny" {
		done = "Denied."
	}
	return s.sessionResult(ctx, in.Session, done)
}

func (s *Server) interruptSession(ctx context.Context, _ *mcp.CallToolRequest, in SessionIn) (*mcp.CallToolResult, any, error) {
	if err := s.opts.Agents.Interrupt(ctx, in.Session); err != nil {
		return nil, nil, canopyErr("interrupt", err)
	}
	return s.sessionResult(ctx, in.Session, "Stopped the turn.")
}

func (s *Server) suspendSession(ctx context.Context, _ *mcp.CallToolRequest, in SessionIn) (*mcp.CallToolResult, any, error) {
	if err := s.opts.Agents.Suspend(ctx, in.Session); err != nil {
		return nil, nil, canopyErr("suspend", err)
	}
	return s.sessionResult(ctx, in.Session, "Suspended.")
}

func (s *Server) resumeSession(ctx context.Context, _ *mcp.CallToolRequest, in SessionIn) (*mcp.CallToolResult, any, error) {
	if err := s.opts.Agents.Resume(ctx, in.Session); err != nil {
		return nil, nil, canopyErr("resume", err)
	}
	return s.sessionResult(ctx, in.Session, "Resumed.")
}

func (s *Server) endSession(ctx context.Context, _ *mcp.CallToolRequest, in EndIn) (*mcp.CallToolResult, any, error) {
	if in.Session == "" {
		return nil, nil, errors.New("session is required")
	}
	res := ActionResult{Kind: "action", Verb: "end session", Tool: "end_session", DryRun: in.DryRun,
		Args: map[string]any{"session": in.Session}, Targets: []Target{},
		Note: "Its sandbox and log are deleted, and it can't be resumed."}
	if in.DryRun {
		res.Summary = "End this session?"
		return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
	}
	if err := s.opts.Agents.End(ctx, in.Session); err != nil {
		return nil, nil, canopyErr("end session", err)
	}
	res.Summary = "Ended the session."
	res.Note = ""
	return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
}

// canopyErr words a Canopy error for the person who hit it.
func canopyErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: Canopy didn't answer in time", what)
	}
	if agents.NotFound(err) {
		return fmt.Errorf("%s: not found on Canopy", what)
	}
	return fmt.Errorf("%s: %s", what, agents.Explain(err))
}
