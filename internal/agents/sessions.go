package agents

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/alee792/canopy/api/canopy"
	"github.com/anthropics/anthropic-sdk-go"
	aopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3"
	oopt "github.com/openai/openai-go/v3/option"
)

// Session is the operator's view of a session, from the native API.
type Session struct {
	ID              string            `json:"id"`
	AgentID         string            `json:"agentId,omitempty"`
	Lab             string            `json:"lab"`
	Harness         string            `json:"harness"`
	Model           string            `json:"model"`
	Status          string            `json:"status"` // idle, in_progress, requires_action, failed
	Error           string            `json:"error,omitempty"`
	ThreadID        string            `json:"threadId,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	LastActive      time.Time         `json:"lastActive"`
	SpendCents      float64           `json:"spendCents"`
	SpendLimitCents *int64            `json:"spendLimitCents,omitempty"`
	ComputeSeconds  float64           `json:"computeSeconds"`
	Usage           Usage             `json:"usage"`
	Sandbox         Sandbox           `json:"sandbox"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// Sandbox is where the session runs.
type Sandbox struct {
	Status   string   `json:"status"` // pending, ready, suspended, failed, expired
	Error    string   `json:"error,omitempty"`
	Network  string   `json:"network"` // enabled, disabled, restricted
	Provider string   `json:"provider,omitempty"`
	Actor    string   `json:"actor,omitempty"`
	Atespace string   `json:"atespace,omitempty"`
	Template string   `json:"template,omitempty"`
	Node     string   `json:"node,omitempty"`
	Image    string   `json:"image,omitempty"`
	Size     string   `json:"size,omitempty"`
	Egress   []string `json:"egress,omitempty"`
}

// Approval is a tool call waiting for a person, as the Anthropic API
// presents it: the event to answer with user.tool_confirmation.
type Approval struct {
	EventID string `json:"eventId"`
	Tool    string `json:"tool"`
	Server  string `json:"server,omitempty"`
	// Command is the shell command, for Bash.
	Command string `json:"command,omitempty"`
	Input   string `json:"input,omitempty"`
}

// SessionDetail is a session with its transcript and what it waits on.
type SessionDetail struct {
	Session    Session     `json:"session"`
	Agent      *Agent      `json:"agent,omitempty"`
	Transcript *Transcript `json:"transcript"`
	// Approvals are what a person can answer here.
	Approvals []Approval `json:"approvals"`
	// Blocked says what the session waits on that the console can't
	// answer, such as a custom tool the app that started it implements.
	Blocked string `json:"blocked,omitempty"`
}

func sessionOf(s canopy.Session) Session {
	e := s.Environment
	out := Session{ID: s.Id, AgentID: s.AgentId, Lab: labOf(s.Surface, s.Harness), Harness: string(s.Harness), Model: s.Model,
		Status: string(s.Status), Error: s.Error, ThreadID: s.ThreadId, CreatedAt: s.CreatedAt, LastActive: s.LastActiveAt,
		SpendCents: s.SpendCents, SpendLimitCents: s.SpendLimitCents, ComputeSeconds: s.ComputeSeconds,
		Usage:    Usage{Input: s.Usage.InputTokens, Cached: s.Usage.CachedInputTokens, Output: s.Usage.OutputTokens},
		Metadata: s.Metadata,
		Sandbox: Sandbox{Status: string(e.Status), Error: e.Error, Network: string(e.Network), Provider: e.Provider, Actor: e.Actor,
			Atespace: e.Atespace, Template: e.Template, Node: e.Node, Image: e.Image, Size: e.Size, Egress: e.EgressHosts}}
	return out
}

// Session reads a session, its transcript and what it waits on.
func (c *Client) Session(ctx context.Context, id string) (*SessionDetail, error) {
	res, err := c.native.GetSessionWithResponse(ctx, id)
	if err := check(res, err); err != nil {
		return nil, err
	}
	s := *res.JSON200
	d := &SessionDetail{Session: sessionOf(s), Approvals: []Approval{}}
	if d.Transcript, err = c.transcript(ctx, id); err != nil {
		return nil, err
	}
	if s.AgentId != "" {
		if a, err := c.native.GetAgentWithResponse(ctx, s.AgentId); check(a, err) == nil {
			ag := agentOf(*a.JSON200)
			d.Agent = &ag
		}
	}
	if s.Status == canopy.SessionStatusRequiresAction {
		if err := c.waitingOn(ctx, d); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// waitingOn fills in what a requires_action session waits on.
func (c *Client) waitingOn(ctx context.Context, d *SessionDetail) error {
	if d.Session.Lab == LabAnthropic {
		approvals, custom, err := c.anthropicPending(ctx, d.Session.ID)
		if err != nil {
			return fmt.Errorf("read what the session is waiting on: %s", Explain(err))
		}
		d.Approvals = approvals
		if len(custom) > 0 {
			d.Blocked = fmt.Sprintf("It's waiting on %s, a tool the app that started it implements.", strings.Join(custom, ", "))
		}
		return nil
	}
	// The OpenAI agent shape has no approvals: Codex sessions only wait on
	// function calls, which their client answers.
	var names []string
	for _, w := range d.Transcript.Waiting {
		if w.Name != "" {
			names = append(names, w.Name)
		}
	}
	if len(names) > 0 {
		d.Blocked = fmt.Sprintf("It's waiting on %s, a function the app that started it implements.", strings.Join(names, ", "))
	} else {
		d.Blocked = "It's waiting on a function the app that started it implements."
	}
	return nil
}

// maxEventScan bounds how far back the console looks for what a session
// waits on.
const maxEventScan = 2000

// anthropicPending finds the tool uses a session's latest requires_action
// idle event names, newest first, less any already answered.
func (c *Client) anthropicPending(ctx context.Context, id string) ([]Approval, []string, error) {
	it := c.anth.Beta.Sessions.Events.ListAutoPaging(ctx, id, anthropic.BetaSessionEventListParams{
		Order: anthropic.BetaSessionEventListParamsOrderDesc, Limit: anthropic.Int(100)})
	var want map[string]bool
	answered := map[string]bool{}
	found := map[string]Approval{}
	var custom []string
	var order []string
	n := 0
	for it.Next() && n < maxEventScan {
		n++
		e := it.Current()
		if want == nil {
			switch e.Type {
			case "user.tool_confirmation":
				answered[e.ToolUseID] = true
			case "user.custom_tool_result":
				answered[e.CustomToolUseID] = true
			case "session.status_idle":
				if e.StopReason.Type != "requires_action" {
					return nil, nil, nil
				}
				want = map[string]bool{}
				for _, id := range e.StopReason.EventIDs {
					if !answered[id] {
						want[id] = true
						order = append(order, id)
					}
				}
			}
			continue
		}
		if !want[e.ID] {
			continue
		}
		delete(want, e.ID)
		switch e.Type {
		case "agent.custom_tool_use":
			custom = append(custom, e.Name)
		case "agent.tool_use", "agent.mcp_tool_use":
			a := Approval{EventID: e.ID, Tool: e.Name, Server: e.MCPServerName}
			if m, ok := e.Input.(map[string]any); ok {
				if cmd, ok := m["command"].(string); ok {
					a.Command = cmd
				}
			}
			if a.Command == "" {
				a.Input = clip(compactAny(e.Input), maxArgs)
			}
			found[e.ID] = a
		}
		if len(want) == 0 {
			break
		}
	}
	if err := it.Err(); err != nil {
		return nil, nil, err
	}
	out := []Approval{}
	for _, id := range order {
		if a, ok := found[id]; ok {
			out = append(out, a)
		}
	}
	return out, custom, nil
}

// Environment is an Anthropic environment a session can run in.
type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Network is "unrestricted" or "limited"; Hosts are the allowed hosts
	// of a limited one.
	Network         string   `json:"network"`
	Hosts           []string `json:"hosts,omitempty"`
	PackageManagers bool     `json:"packageManagers,omitempty"`
	MCPServers      bool     `json:"mcpServers,omitempty"`
}

// maxEnvironments bounds the playground's environment list.
const maxEnvironments = 200

// Environments lists the Anthropic environments, newest first.
func (c *Client) Environments(ctx context.Context) ([]Environment, error) {
	it := c.anth.Beta.Environments.ListAutoPaging(ctx, anthropic.BetaEnvironmentListParams{Limit: anthropic.Int(100)})
	out := []Environment{}
	for it.Next() && len(out) < maxEnvironments {
		e := it.Current()
		if e.ArchivedAt != "" {
			continue
		}
		n := e.Config.Networking
		out = append(out, Environment{ID: e.ID, Name: e.Name, Network: n.Type, Hosts: n.AllowedHosts,
			PackageManagers: n.AllowPackageManagers, MCPServers: n.AllowMCPServers})
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("list environments: %s", Explain(err))
	}
	return out, nil
}

// Start is a session to start from the playground.
type Start struct {
	Agent string
	// Version pins an Anthropic agent's version; 0 is the latest.
	Version int
	// Environment is the Anthropic environment.
	Environment string
	// Network and Domains are the OpenAI session's own environment:
	// network access enabled or disabled, and allowed domains, which make
	// it restricted.
	Network string
	Domains []string
	// Repository and Branch are a GitHub repository to check out
	// (Anthropic sessions).
	Repository string
	Branch     string
	Message    string
	Title      string
}

// startedBy marks sessions the console started, in their metadata.
const startedBy = "substrate-console"

// StartSession starts a session through the agent's lab API and sends the
// first message with it.
func (c *Client) StartSession(ctx context.Context, a Agent, in Start) (string, error) {
	meta := map[string]string{"started_by": startedBy}
	switch a.Lab {
	case LabAnthropic:
		p := anthropic.BetaSessionNewParams{EnvironmentID: in.Environment, Metadata: meta,
			Agent: anthropic.BetaSessionNewParamsAgentUnion{OfBetaManagedAgentsAgents: &anthropic.BetaManagedAgentsAgentParams{
				Type: "agent", ID: a.ID}},
			InitialEvents: []anthropic.BetaSessionNewParamsInitialEventUnion{{OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
				Type:    "user.message",
				Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{OfText: &anthropic.BetaManagedAgentsTextBlockParam{Type: "text", Text: in.Message}}},
			}}},
		}
		if in.Version > 0 {
			p.Agent.OfBetaManagedAgentsAgents.Version = anthropic.Int(int64(in.Version))
		}
		if in.Title != "" {
			p.Title = anthropic.String(in.Title)
		}
		if in.Repository != "" {
			repo := &anthropic.BetaManagedAgentsGitHubRepositoryResourceParams{Type: "github_repository", URL: in.Repository}
			if in.Branch != "" {
				repo.Checkout.OfBranch = &anthropic.BetaManagedAgentsBranchCheckoutParam{Type: "branch", Name: in.Branch}
			}
			p.Resources = []anthropic.BetaSessionNewParamsResourceUnion{{OfGitHubRepository: repo}}
		}
		// Not retried: a retry after a lost response would start a second
		// session.
		s, err := c.anth.Beta.Sessions.New(ctx, p, aopt.WithMaxRetries(0))
		if err != nil {
			return "", err
		}
		return s.ID, nil
	case LabOpenAI:
		net := openai.EnvironmentParamOpenAIHostedNetwork{Access: in.Network}
		if len(in.Domains) > 0 {
			net.AllowedDomains = in.Domains
		}
		s, err := c.oai.Beta.Agents.Sessions.New(ctx, openai.BetaAgentSessionNewParams{
			AgentID:     openai.String(a.ID),
			Environment: openai.EnvironmentParamUnion{OfParamOpenAIHosted: &openai.EnvironmentParamOpenAIHosted{Network: net}},
			Input:       openai.BetaAgentSessionNewParamsInputUnion{OfString: openai.String(in.Message)},
			Metadata:    meta,
		}, oopt.WithMaxRetries(0))
		if err != nil {
			return "", err
		}
		return s.ID, nil
	}
	return "", fmt.Errorf("agent %s is on an API the console doesn't drive", a.ID)
}

// lab reads which API a session belongs to.
func (c *Client) lab(ctx context.Context, id string) (string, error) {
	res, err := c.native.GetSessionWithResponse(ctx, id)
	if err := check(res, err); err != nil {
		return "", err
	}
	return labOf(res.JSON200.Surface, res.JSON200.Harness), nil
}

// Send sends a message into a session.
func (c *Client) Send(ctx context.Context, id, text string) error {
	lab, err := c.lab(ctx, id)
	if err != nil {
		return err
	}
	if lab == LabOpenAI {
		return c.oai.Beta.Agents.Sessions.Events.New(ctx, id, openai.BetaAgentSessionEventNewParams{
			Events: []openai.AgentSessionInputParamUnion{{OfParamAgentSessionInputMessage: &openai.AgentSessionInputParamAgentSessionInputMessage{
				Input: []openai.AgentSessionInputMessageParam{{Content: []openai.InputContentParamUnion{openai.InputContentParamOfParamInputText(text)}}},
			}}},
		}, oopt.WithMaxRetries(0))
	}
	_, err = c.anth.Beta.Sessions.Events.Send(ctx, id, anthropic.BetaSessionEventSendParams{
		Events: []anthropic.BetaManagedAgentsEventParamsUnion{{OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
			Type:    "user.message",
			Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{OfText: &anthropic.BetaManagedAgentsTextBlockParam{Type: "text", Text: text}}},
		}}},
	}, aopt.WithMaxRetries(0))
	return err
}

// Interrupt stops the turn running now.
func (c *Client) Interrupt(ctx context.Context, id string) error {
	lab, err := c.lab(ctx, id)
	if err != nil {
		return err
	}
	if lab == LabOpenAI {
		return c.oai.Beta.Agents.Sessions.Events.New(ctx, id, openai.BetaAgentSessionEventNewParams{
			Events: []openai.AgentSessionInputParamUnion{{OfParamAgentSessionInputCancel: &openai.AgentSessionInputParamAgentSessionInputCancel{}}},
		})
	}
	_, err = c.anth.Beta.Sessions.Events.Send(ctx, id, anthropic.BetaSessionEventSendParams{
		Events: []anthropic.BetaManagedAgentsEventParamsUnion{{OfUserInterrupt: &anthropic.BetaManagedAgentsUserInterruptEventParams{Type: "user.interrupt"}}},
	})
	return err
}

// Answer allows or denies a tool use waiting for approval. Only Anthropic
// agents ask.
func (c *Client) Answer(ctx context.Context, id, event string, allow bool, reason string) error {
	lab, err := c.lab(ctx, id)
	if err != nil {
		return err
	}
	if lab != LabAnthropic {
		return errors.New("only Anthropic agents ask for approval; this session's agent is on the OpenAI API")
	}
	p := &anthropic.BetaManagedAgentsUserToolConfirmationEventParams{Type: "user.tool_confirmation", ToolUseID: event,
		Result: anthropic.BetaManagedAgentsUserToolConfirmationEventParamsResultDeny}
	if allow {
		p.Result = anthropic.BetaManagedAgentsUserToolConfirmationEventParamsResultAllow
	} else if reason != "" {
		p.DenyMessage = anthropic.String(reason)
	}
	_, err = c.anth.Beta.Sessions.Events.Send(ctx, id, anthropic.BetaSessionEventSendParams{
		Events: []anthropic.BetaManagedAgentsEventParamsUnion{{OfUserToolConfirmation: p}},
	})
	return err
}

// Suspend snapshots the session's sandbox and stops it.
func (c *Client) Suspend(ctx context.Context, id string) error {
	res, err := c.native.SuspendSessionWithResponse(ctx, id)
	return check(res, err)
}

// Resume restores the session's sandbox.
func (c *Client) Resume(ctx context.Context, id string) error {
	res, err := c.native.ResumeSessionWithResponse(ctx, id)
	return check(res, err)
}

// End deletes the session and its sandbox, through its lab API.
func (c *Client) End(ctx context.Context, id string) error {
	lab, err := c.lab(ctx, id)
	if err != nil {
		return err
	}
	if lab == LabOpenAI {
		_, err = c.oai.Beta.Agents.Sessions.Delete(ctx, id)
	} else {
		_, err = c.anth.Beta.Sessions.Delete(ctx, id, anthropic.BetaSessionDeleteParams{})
	}
	if err == nil {
		c.forget(id)
	}
	return err
}

// GitHubRepo checks a repository URL the way the Anthropic API takes it.
func GitHubRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("%q isn't a repository URL", s)
	}
	if u.Scheme != "https" {
		return "", errors.New("use the https URL of the repository")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func compactAny(v any) string {
	if v == nil {
		return ""
	}
	b, err := jsonMarshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
