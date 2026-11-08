package mcpapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	substratefake "github.com/alee792/substrate-fake"

	"github.com/alee792/substrate-console/internal/backend/ate"
	"github.com/alee792/substrate-console/internal/mcpapp"
	"github.com/alee792/substrate-console/internal/metrics"
	"github.com/alee792/substrate-console/internal/shell"
)

// served is the console in forward mode over HTTP, against the fake demo
// cluster with its access policies.
type served struct {
	fake *substratefake.Cluster
	url  string
}

func serveForward(t *testing.T) *served {
	t.Helper()
	ctx := context.Background()
	fc, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fc.Close)
	live, err := ate.New(ctx, ate.Config{Kubeconfig: fc.Kubeconfig, Context: fc.Context, Endpoint: fc.APIEndpoint,
		Forward: true, Atespaces: []string{"triage", "ioc-enrich"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	// The default cache, so a test catches one caller's view served to another.
	app := mcpapp.New(mcpapp.Options{Backend: live, Metrics: metrics.NewPrometheus(fc.PrometheusURL), ViewHTML: []byte("<!doctype html>")})
	hs := httptest.NewServer(shell.Handler(app.MCP(), nil, shell.Auth{Forward: true}))
	t.Cleanup(hs.Close)
	return &served{fake: fc, url: hs.URL + "/mcp"}
}

type bearerRT string

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func (s *served) as(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: s.url, HTTPClient: &http.Client{Transport: bearerRT(token)}, DisableStandaloneSSE: true}, nil)
	if err == nil {
		t.Cleanup(func() { cs.Close() })
	}
	return cs, err
}

func (s *served) token(principal string) string {
	return s.fake.World.IssueToken(principal, substratefake.TokenOptions{})
}

// running finds a running actor in an atespace.
func (s *served) running(t *testing.T, atespace string) map[string]any {
	t.Helper()
	r, err := s.fake.Client().ListActors(context.Background(), &ateapi.ListActorsRequest{Atespace: atespace})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range r.GetActors() {
		if a.GetStatus().GetState() == ateapi.ActorState_ACTOR_STATE_RUNNING {
			return map[string]any{"atespace": atespace, "name": a.GetMetadata().GetName()}
		}
	}
	t.Fatalf("no running actor in %s", atespace)
	return nil
}

func tryCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var txt string
	if len(res.Content) > 0 {
		txt = res.Content[0].(*mcp.TextContent).Text
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, txt, res.IsError
}

func atespacesOf(st map[string]any) []string {
	var out []string
	list, _ := st["atespaces"].([]any)
	for _, a := range list {
		out = append(out, a.(map[string]any)["name"].(string))
	}
	return out
}

func TestForwardActsAsEachCaller(t *testing.T) {
	s := serveForward(t)

	if _, err := s.as(t, ""); err == nil {
		t.Fatal("connected without a token")
	}

	owner, err := s.as(t, s.token(substratefake.ClientPrincipal))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := call(t, owner, "status", nil)
	acc := st["access"].(map[string]any)
	if acc["principal"] != substratefake.ClientPrincipal || acc["shared"] != true || acc["workers"] != true || acc["allAtespaces"] != true {
		t.Errorf("owner access %v", acc)
	}
	if len(st["pools"].([]any)) != 2 || len(atespacesOf(st)) != 3 {
		t.Errorf("owner sees %d pools, atespaces %v", len(st["pools"].([]any)), atespacesOf(st))
	}

	// The triage editor sees triage and nothing cluster-wide, right after
	// the owner, so a shared cache would show.
	before := len(s.fake.World.Calls())
	dev, err := s.as(t, s.token(substratefake.DemoTriageDev))
	if err != nil {
		t.Fatal(err)
	}
	st, txt := call(t, dev, "status", nil)
	if got := atespacesOf(st); !slices.Equal(got, []string{"triage"}) {
		t.Errorf("editor sees atespaces %v", got)
	}
	if st["pools"] != nil {
		t.Errorf("editor sees pools %v", st["pools"])
	}
	for _, v := range st["vitals"].([]any) {
		if v.(map[string]any)["key"] == "capacity" {
			t.Error("editor sees cluster capacity")
		}
	}
	if !strings.Contains(txt, "need cluster-wide viewer access") {
		t.Errorf("status doesn't say what's hidden:\n%s", txt)
	}
	if _, txt, isErr := tryCall(t, dev, "list_worker_pools", nil); !isErr || !strings.Contains(txt, "cluster-wide viewer") {
		t.Errorf("editor list_worker_pools: %s", txt)
	}
	if _, txt, isErr := tryCall(t, dev, "query_metric", map[string]any{"metric": "resume_p95", "by": "pool"}); !isErr {
		t.Errorf("editor got pool metrics: %s", txt)
	}
	if res, txt, isErr := tryCall(t, dev, "query_metric", map[string]any{"metric": "resume_p95", "by": "template"}); isErr || len(res["series"].([]any)) == 0 {
		t.Errorf("editor's own template metrics: %s", txt)
	} else {
		for _, se := range res["series"].([]any) {
			if l := se.(map[string]any)["label"].(string); !strings.HasPrefix(l, "agent-devbox") {
				t.Errorf("editor sees another atespace's template %q", l)
			}
		}
	}
	// Logs need the worker assignment, which only global viewers can list;
	// the editor gets none and is told why, not the worker's other lines.
	if res, txt := call(t, dev, "get_actor", s.running(t, "triage")); res["logs"] != nil && len(res["logs"].([]any)) > 0 || !strings.Contains(txt, "Logs need cluster-wide viewer access") {
		t.Errorf("editor get_actor logs %v:\n%s", res["logs"], txt)
	}
	// Writes in their atespace work; elsewhere ateapi refuses them.
	res, _ := call(t, dev, "suspend_actors", map[string]any{"actors": []any{s.running(t, "triage")}})
	if tg := res["targets"].([]any)[0].(map[string]any); tg["ok"] != true {
		t.Errorf("editor suspend in triage: %v", tg)
	}
	res, _ = call(t, dev, "suspend_actors", map[string]any{"actors": []any{s.running(t, "ioc-enrich")}})
	if tg := res["targets"].([]any)[0].(map[string]any); tg["ok"] == true || tg["reason"] != "not found" {
		t.Errorf("editor suspend in ioc-enrich: %v", tg)
	}
	// Every call the editor's session made reached ateapi as the editor.
	// (Calls with no principal are this test's own, in process.)
	for _, c := range s.fake.World.Calls()[before:] {
		if c.Principal != "" && c.Principal != substratefake.DemoTriageDev {
			t.Errorf("%s ran as %q", c, c.Principal)
		}
	}

	// The global viewer sees everything and changes nothing.
	viewer, err := s.as(t, s.token(substratefake.DemoOnCall))
	if err != nil {
		t.Fatal(err)
	}
	st, _ = call(t, viewer, "status", nil)
	if len(st["pools"].([]any)) != 2 || len(atespacesOf(st)) != 3 {
		t.Errorf("viewer sees %v pools, atespaces %v", st["pools"], atespacesOf(st))
	}
	res, _ = call(t, viewer, "suspend_actors", map[string]any{"actors": []any{s.running(t, "ioc-enrich")}})
	if tg := res["targets"].([]any)[0].(map[string]any); tg["ok"] == true || tg["reason"] != "you can't suspend actors in ioc-enrich" {
		t.Errorf("viewer suspend: %v", tg)
	}
	if _, txt, isErr := tryCall(t, viewer, "drain_worker", map[string]any{"worker": "gvisor-pool-a/gvisor-pool-a-1"}); !isErr || !strings.Contains(txt, "can't drain") {
		t.Errorf("viewer drain: %s", txt)
	}

	// Someone with no grants sees an empty cluster that says why.
	nobody, err := s.as(t, s.token("system:serviceaccount:ate-users:nobody"))
	if err != nil {
		t.Fatal(err)
	}
	if st, txt := call(t, nobody, "status", nil); len(atespacesOf(st)) != 0 || !strings.Contains(txt, "can't list atespaces") {
		t.Errorf("nobody: %v\n%s", atespacesOf(st), txt)
	}
}

// A problem the person resolved stays resolved when their token rotates:
// history is kept per principal, snapshots per token.
func TestResolvedSurvivesTokenRotation(t *testing.T) {
	s := serveForward(t)
	dev, err := s.as(t, s.token(substratefake.DemoTriageDev))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := call(t, dev, "status", nil)
	var revert map[string]any
	for _, it := range st["items"].([]any) {
		for _, a := range it.(map[string]any)["actions"].([]any) {
			if a := a.(map[string]any); a["tool"] == "revert_actors" {
				revert = a["args"].(map[string]any)
			}
		}
	}
	if revert == nil {
		t.Fatal("no crash to revert in triage")
	}
	if res, _ := call(t, dev, "revert_actors", revert); !strings.HasPrefix(res["summary"].(string), "Reverted") {
		t.Fatalf("revert: %v", res["summary"])
	}
	// A new token for the same person.
	rotated, err := s.as(t, s.fake.World.IssueToken(substratefake.DemoTriageDev, substratefake.TokenOptions{TTL: 2 * time.Hour}))
	if err != nil {
		t.Fatal(err)
	}
	st, _ = call(t, rotated, "status", nil)
	resolved := 0
	for _, it := range st["items"].([]any) {
		if it.(map[string]any)["severity"] == "resolved" {
			resolved++
		}
	}
	if resolved == 0 {
		t.Error("the reverted crash isn't shown as resolved after the token rotated")
	}
	// Someone else doesn't see the editor's history.
	viewer, err := s.as(t, s.token(substratefake.DemoOnCall))
	if err != nil {
		t.Fatal(err)
	}
	st, _ = call(t, viewer, "status", nil)
	for _, it := range st["items"].([]any) {
		if it.(map[string]any)["severity"] == "resolved" {
			t.Errorf("viewer sees the editor's resolved item %v", it.(map[string]any)["title"])
		}
	}
}
