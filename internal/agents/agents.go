// Package agents reads and drives agents on Canopy
// (github.com/alee792/canopy), the managed-agents control plane that runs
// on Substrate.
//
// The console adds no agent format of its own. An agent is an Anthropic or
// OpenAI agent: its definition is that API's own agent object, and a
// session is started, sent messages and answered through that API with the
// official SDK, pointed at Canopy. Canopy's native API (/canopy/v1, through
// its generated Go client) is used for what the lab APIs can't show: every
// agent and version across both surfaces, where a version came from, the
// session's sandbox on Substrate, and the session log the transcript is
// built from.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alee792/canopy/api/canopy"
	"github.com/anthropics/anthropic-sdk-go"
	aopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3"
	oopt "github.com/openai/openai-go/v3/option"
)

// Config says where Canopy is.
type Config struct {
	// URL is Canopy's base URL, e.g. http://localhost:8000. The native API
	// is under /canopy/v1, the lab APIs under /anthropic and /openai/v1.
	URL string
	// APIKey is a Canopy API key. Everyone using the console acts as it.
	APIKey string
	// HTTPClient is optional.
	HTTPClient *http.Client
}

// Client talks to one Canopy.
type Client struct {
	native *canopy.ClientWithResponses
	anth   anthropic.Client
	oai    openai.Client
	host   string

	mu   sync.Mutex
	logs map[string]*sessionLog
}

// Lab names the API an agent was made through, and so the SDK that drives
// its sessions.
const (
	LabAnthropic = "anthropic"
	LabOpenAI    = "openai"
)

// New returns a client. It doesn't call Canopy.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		return nil, errors.New("canopy: no URL")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("canopy: %q is not an http(s) URL", cfg.URL)
	}
	if cfg.APIKey == "" {
		return nil, errors.New("canopy: no API key")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	native, err := canopy.NewClientWithResponses(base+"/canopy/v1", canopy.WithHTTPClient(hc), canopy.WithAPIKey(cfg.APIKey))
	if err != nil {
		return nil, err
	}
	c := &Client{
		native: native,
		// The same calls an application makes: the official SDKs with only
		// the base URL changed.
		anth: anthropic.NewClient(aopt.WithBaseURL(base+"/anthropic"), aopt.WithAPIKey(cfg.APIKey), aopt.WithHTTPClient(hc),
			aopt.WithHeader("anthropic-beta", string(anthropic.AnthropicBetaManagedAgents2026_04_01))),
		oai:  openai.NewClient(oopt.WithBaseURL(base+"/openai/v1"), oopt.WithAPIKey(cfg.APIKey), oopt.WithHTTPClient(hc)),
		host: strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://"),
		logs: map[string]*sessionLog{},
	}
	return c, nil
}

// Host is Canopy's address, for display.
func (c *Client) Host() string { return c.host }

// Agent is an agent as every surface shares it.
type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Lab         string    `json:"lab"`     // anthropic or openai
	Harness     string    `json:"harness"` // claude-code or codex
	Model       string    `json:"model"`
	Version     int       `json:"version"`
	Source      *Source   `json:"source,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Sessions counts recent sessions; Active those running or waiting
	// now. LastActive is the latest of them.
	Sessions   int        `json:"sessions"`
	Active     int        `json:"active"`
	LastActive *time.Time `json:"lastActive,omitempty"`
}

// Source is where a git-defined agent comes from.
type Source struct {
	URL    string `json:"url"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Version is one version of an agent.
type Version struct {
	Version    int       `json:"version"`
	Origin     string    `json:"origin"` // git (a push) or api (an edit through the lab API)
	Source     *Source   `json:"source,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	Contents   *Contents `json:"contents,omitempty"`
	Bundle     string    `json:"bundle,omitempty"`
	BundleSize int64     `json:"bundleSize,omitempty"`
}

// Contents is what a version's bundle installs beside the definition.
type Contents struct {
	Instructions bool     `json:"instructions"`
	Skills       []string `json:"skills"`
	MCPServers   []string `json:"mcpServers"`
	Subagents    []string `json:"subagents"`
}

// SessionRow is a session in a list.
type SessionRow struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentId,omitempty"`
	Status     string    `json:"status"`
	EnvStatus  string    `json:"envStatus"`
	Harness    string    `json:"harness"`
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"createdAt"`
	LastActive time.Time `json:"lastActive"`
	SpendCents float64   `json:"spendCents"`
	// Preview is the first message, when the console has read the log.
	Preview string `json:"preview,omitempty"`
}

func labOf(s canopy.Surface, h canopy.Harness) string {
	switch s {
	case canopy.SurfaceAnthropic:
		return LabAnthropic
	case canopy.SurfaceOpenai:
		return LabOpenAI
	}
	if h == canopy.HarnessCodex {
		return LabOpenAI
	}
	return LabAnthropic
}

func agentOf(a canopy.Agent) Agent {
	out := Agent{ID: a.Id, Name: a.Name, Lab: labOf(a.Surface, a.Harness), Harness: string(a.Harness), Model: a.Model,
		Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if out.Name == "" {
		out.Name = a.Id
	}
	if a.Source != nil {
		out.Source = &Source{URL: a.Source.Url, Ref: a.Source.Ref, Path: a.Source.Path}
	}
	return out
}

func sessionRow(s canopy.Session) SessionRow {
	return SessionRow{ID: s.Id, AgentID: s.AgentId, Status: string(s.Status), EnvStatus: string(s.Environment.Status),
		Harness: string(s.Harness), Model: s.Model, CreatedAt: s.CreatedAt, LastActive: s.LastActiveAt, SpendCents: s.SpendCents}
}

// active is a session doing something or waiting on someone.
func active(s canopy.Session) bool {
	return s.Status == canopy.SessionStatusInProgress || s.Status == canopy.SessionStatusRequiresAction
}

// maxAgents bounds how many agents a list reads.
const maxAgents = 500

// Agents lists every agent, newest first, with its recent sessions counted.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	var after *string
	limit := 100
	for len(out) < maxAgents {
		res, err := c.native.ListAgentsWithResponse(ctx, &canopy.ListAgentsParams{Limit: &limit, After: after})
		if err := check(res, err); err != nil {
			return nil, err
		}
		for _, a := range res.JSON200.Data {
			out = append(out, agentOf(a))
		}
		if !res.JSON200.HasMore || res.JSON200.LastId == "" {
			break
		}
		after = &res.JSON200.LastId
	}
	recent, err := c.sessions(ctx, nil, 100)
	if err != nil {
		return nil, err
	}
	idx := map[string]*Agent{}
	for i := range out {
		idx[out[i].ID] = &out[i]
	}
	for _, s := range recent {
		a := idx[s.AgentId]
		if a == nil {
			continue
		}
		a.Sessions++
		if active(s) {
			a.Active++
		}
		if t := s.LastActiveAt; a.LastActive == nil || t.After(*a.LastActive) {
			a.LastActive = &t
		}
	}
	return out, nil
}

func (c *Client) sessions(ctx context.Context, agent *string, limit int) ([]canopy.Session, error) {
	res, err := c.native.ListSessionsWithResponse(ctx, &canopy.ListSessionsParams{AgentId: agent, Limit: &limit})
	if err := check(res, err); err != nil {
		return nil, err
	}
	return res.JSON200.Data, nil
}

// AgentDetail is one agent at one version.
type AgentDetail struct {
	Agent    Agent        `json:"agent"`
	Versions []Version    `json:"versions"`
	Shown    int          `json:"shown"` // the version Definition is for
	Sessions []SessionRow `json:"sessions"`
	// Definition is the agent object as its lab API returns it.
	Definition json.RawMessage `json:"definition,omitempty"`
	// DefinitionNote says why Definition is missing or isn't for Shown.
	DefinitionNote string `json:"definitionNote,omitempty"`
}

// maxVersions bounds the versions an agent page lists.
const maxVersions = 100

// Agent reads one agent with its versions, recent sessions, and its
// definition at version (0 for the latest).
func (c *Client) Agent(ctx context.Context, id string, version int) (*AgentDetail, error) {
	res, err := c.native.GetAgentWithResponse(ctx, id)
	if err := check(res, err); err != nil {
		return nil, err
	}
	d := &AgentDetail{Agent: agentOf(*res.JSON200), Versions: []Version{}, Sessions: []SessionRow{}}
	if version <= 0 || version > d.Agent.Version {
		version = d.Agent.Version
	}
	d.Shown = version

	limit := maxVersions
	vs, err := c.native.ListAgentVersionsWithResponse(ctx, id, &canopy.ListAgentVersionsParams{Limit: &limit})
	if err := check(vs, err); err != nil {
		return nil, err
	}
	for _, v := range vs.JSON200.Data {
		d.Versions = append(d.Versions, versionOf(v))
	}
	sort.Slice(d.Versions, func(i, j int) bool { return d.Versions[i].Version > d.Versions[j].Version })
	for _, v := range d.Versions {
		if v.Version == d.Agent.Version && v.Source != nil && d.Agent.Source != nil {
			d.Agent.Source.Commit = v.Source.Commit
		}
	}

	recent, err := c.sessions(ctx, &id, 20)
	if err != nil {
		return nil, err
	}
	for _, s := range recent {
		r := sessionRow(s)
		r.Preview = c.preview(s.Id)
		d.Sessions = append(d.Sessions, r)
		d.Agent.Sessions++
		if active(s) {
			d.Agent.Active++
		}
	}

	def, desc, note, err := c.definition(ctx, d.Agent, version)
	if err != nil {
		d.DefinitionNote = "The " + labName(d.Agent.Lab) + " didn't return the definition: " + Explain(err)
	} else {
		d.Definition, d.Agent.Description, d.DefinitionNote = def, desc, note
	}
	return d, nil
}

func versionOf(v canopy.AgentVersion) Version {
	out := Version{Version: v.Version, Origin: string(v.Origin), CreatedAt: v.CreatedAt}
	if v.Source != nil {
		out.Source = &Source{URL: v.Source.Url, Ref: v.Source.Ref, Path: v.Source.Path, Commit: v.Source.Commit}
	}
	if v.Contents != nil {
		out.Contents = &Contents{Instructions: v.Contents.Instructions, Skills: nonNil(v.Contents.Skills),
			MCPServers: nonNil(v.Contents.McpServers), Subagents: nonNil(v.Contents.Subagents)}
	}
	if v.Bundle != nil {
		out.Bundle, out.BundleSize = v.Bundle.Digest, v.Bundle.Size
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func labName(lab string) string {
	if lab == LabOpenAI {
		return "OpenAI API"
	}
	return "Anthropic API"
}

// definition reads the agent object from its lab API, pretty-printed.
func (c *Client) definition(ctx context.Context, a Agent, version int) (json.RawMessage, string, string, error) {
	var raw, note, desc string
	switch a.Lab {
	case LabAnthropic:
		got, err := c.anth.Beta.Agents.Get(ctx, a.ID, anthropic.BetaAgentGetParams{Version: anthropic.Int(int64(version))})
		if err != nil {
			return nil, "", "", err
		}
		raw, desc = got.RawJSON(), got.Description
	case LabOpenAI:
		// The OpenAI API serves only an agent's latest definition.
		got, err := c.oai.Beta.Agents.Get(ctx, a.ID)
		if err != nil {
			return nil, "", "", err
		}
		raw = got.RawJSON()
		if version != a.Version {
			note = fmt.Sprintf("The OpenAI API serves only the latest definition, so this is version %d.", a.Version)
		}
	}
	// Indent, not re-marshal, so the fields stay in the API's order.
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(raw), "", "  "); err != nil {
		return nil, "", "", err
	}
	return b.Bytes(), desc, note, nil
}

// check turns a generated client's result into an error.
func check(res interface{ StatusCode() int }, err error) error {
	if err != nil {
		return err
	}
	if v := reflect.ValueOf(res); v.Kind() == reflect.Pointer && v.IsNil() {
		return errors.New("canopy: no response")
	}
	code := res.StatusCode()
	if code >= 200 && code < 300 {
		return nil
	}
	// Every generated response carries the raw body as Body.
	var body []byte
	if v := reflect.ValueOf(res); v.Kind() == reflect.Pointer && !v.IsNil() {
		if f := v.Elem().FieldByName("Body"); f.IsValid() {
			body, _ = f.Interface().([]byte)
		}
	}
	return apiError(code, body)
}

// Error is an error response from Canopy, on any of its APIs.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// NotFound reports whether err is a 404 from Canopy.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func apiError(status int, body []byte) error {
	var e canopy.Error
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return &Error{Status: status, Message: e.Error.Message}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &Error{Status: status, Message: fmt.Sprintf("Canopy answered %d: %s", status, msg)}
}

// Explain is err as a sentence a person can act on: the API's own message
// for an error response, without the SDK's request details.
func Explain(err error) string {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ae.RawJSON()), &body) == nil && body.Error.Message != "" {
			return body.Error.Message
		}
		return fmt.Sprintf("Canopy answered %d", ae.StatusCode)
	}
	var oe *openai.Error
	if errors.As(err, &oe) {
		if oe.Message != "" {
			return oe.Message
		}
		return fmt.Sprintf("Canopy answered %d", oe.StatusCode)
	}
	return err.Error()
}
