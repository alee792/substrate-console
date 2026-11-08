package mcpapp_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	aopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3"
	oopt "github.com/openai/openai-go/v3/option"

	"github.com/alee792/substrate-console/internal/agents"
	"github.com/alee792/substrate-console/internal/mcpapp"
)

// TestAgentsAgainstCanopy drives the agent tools against a real Canopy:
// CANOPY_TEST_URL, with CANOPY_API_KEY, running Claude Code against
// Canopy's scripted model (local_gateway.mock), which runs "run: <cmd>"
// as a Bash call. It creates an agent through the Anthropic API whose Bash
// calls ask first, so the approval path is covered.
func TestAgentsAgainstCanopy(t *testing.T) {
	url := os.Getenv("CANOPY_TEST_URL")
	if url == "" {
		t.Skip("set CANOPY_TEST_URL (and CANOPY_API_KEY) to a Canopy with the scripted model")
	}
	key := os.Getenv("CANOPY_API_KEY")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	lab := anthropic.NewClient(aopt.WithBaseURL(url+"/anthropic"), aopt.WithAPIKey(key),
		aopt.WithHeader("anthropic-beta", string(anthropic.AnthropicBetaManagedAgents2026_04_01)))
	ag, err := lab.Beta.Agents.New(ctx, anthropic.BetaAgentNewParams{
		Name:  "console-live-test",
		Model: anthropic.BetaManagedAgentsModelConfigParams{ID: "claude-opus-5-5"},
		Tools: []anthropic.BetaAgentNewParamsToolUnion{{OfAgentToolset20260401: &anthropic.BetaManagedAgentsAgentToolset20260401Params{
			Type: "agent_toolset_20260401",
			Configs: []anthropic.BetaManagedAgentsAgentToolConfigParamsUnion{{OfBash: &anthropic.BetaManagedAgentsBashToolConfigParams{
				Type:             "bash",
				PermissionPolicy: anthropic.BetaManagedAgentsBashToolConfigParamsPermissionPolicyUnion{OfAlwaysAsk: &anthropic.BetaManagedAgentsAlwaysAskPolicyParam{Type: "always_ask"}},
			}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lab.Beta.Agents.Archive(context.Background(), ag.ID, anthropic.BetaAgentArchiveParams{}) })
	env, err := lab.Beta.Environments.New(ctx, anthropic.BetaEnvironmentNewParams{Name: "console-live-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		lab.Beta.Environments.Delete(context.Background(), env.ID, anthropic.BetaEnvironmentDeleteParams{})
	})

	cl, err := agents.New(agents.Config{URL: url, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := connectWith(t, func(o *mcpapp.Options) { o.Agents = cl })

	list, _ := call(t, cs, "list_agents", nil)
	if !strings.Contains(jsonString(list), ag.ID) {
		t.Fatalf("list_agents doesn't have %s", ag.ID)
	}
	page, _ := call(t, cs, "get_agent", map[string]any{"agent": ag.ID})
	if !strings.Contains(jsonString(page["definition"]), "always_ask") {
		t.Fatalf("definition isn't the Anthropic agent object: %s", jsonString(page["definition"]))
	}

	pg, _ := call(t, cs, "start_session", map[string]any{"agent": ag.ID, "environment": env.ID, "message": "run: echo hello-from-console", "dry_run": true})
	if pg["kind"] != "playground" || len(pg["problems"].(map[string]any)) != 0 {
		t.Fatalf("dry run: %s", jsonString(pg))
	}
	started, txt := call(t, cs, "start_session", map[string]any{"agent": ag.ID, "environment": env.ID, "message": "run: echo hello-from-console"})
	if started["kind"] != "session" {
		t.Fatalf("start: %s", txt)
	}
	id := started["session"].(map[string]any)["id"].(string)
	defer call(t, cs, "end_session", map[string]any{"session": id})

	waiting := waitFor(t, cs, id, func(p map[string]any) bool { return len(p["approvals"].([]any)) > 0 })
	ap := waiting["approvals"].([]any)[0].(map[string]any)
	if ap["command"] != "echo hello-from-console" {
		t.Fatalf("approval: %s", jsonString(ap))
	}
	call(t, cs, "answer_approval", map[string]any{"session": id, "event": ap["eventId"], "decision": "allow"})
	done := waitFor(t, cs, id, func(p map[string]any) bool {
		turns := p["transcript"].(map[string]any)["turns"].([]any)
		return len(turns) == 1 && turns[0].(map[string]any)["status"] == "completed"
	})
	turn := done["transcript"].(map[string]any)["turns"].([]any)[0].(map[string]any)
	if turn["input"] != "run: echo hello-from-console" {
		t.Errorf("input = %v", turn["input"])
	}
	var sawCmd, sawReply bool
	for _, x := range turn["items"].([]any) {
		it := x.(map[string]any)
		switch it["kind"] {
		case "command_execution":
			sawCmd = it["command"] == "echo hello-from-console" && strings.Contains(jsonString(it["output"]), "hello-from-console") && it["decision"] == "allow"
		case "assistant_message":
			sawReply = strings.Contains(jsonString(it["text"]), "hello-from-console")
		}
	}
	if !sawCmd || !sawReply {
		t.Fatalf("transcript: %s", jsonString(turn))
	}

	// A second message goes through the same API, and Stop interrupts it.
	call(t, cs, "send_message", map[string]any{"session": id, "text": "run: timeout 60 tail -f /dev/null"})
	waiting = waitFor(t, cs, id, func(p map[string]any) bool { return len(p["approvals"].([]any)) > 0 })
	call(t, cs, "answer_approval", map[string]any{"session": id, "event": waiting["approvals"].([]any)[0].(map[string]any)["eventId"], "decision": "allow"})
	running := waitFor(t, cs, id, func(p map[string]any) bool { return p["session"].(map[string]any)["status"] == "in_progress" && len(p["approvals"].([]any)) == 0 })
	if !hasAction(running, "interrupt_session") {
		t.Errorf("no Stop while working: %s", jsonString(running["actions"]))
	}
	call(t, cs, "interrupt_session", map[string]any{"session": id})
	stopped := waitFor(t, cs, id, func(p map[string]any) bool {
		turns := p["transcript"].(map[string]any)["turns"].([]any)
		return len(turns) == 2 && turns[1].(map[string]any)["status"] == "cancelled"
	})
	if !hasAction(stopped, "suspend_session") {
		t.Errorf("no Suspend when idle: %s", jsonString(stopped["actions"]))
	}
	call(t, cs, "suspend_session", map[string]any{"session": id})
	waitFor(t, cs, id, func(p map[string]any) bool { return p["session"].(map[string]any)["sandbox"].(map[string]any)["status"] == "suspended" })
}

func hasAction(p map[string]any, tool string) bool {
	for _, a := range p["actions"].([]any) {
		if a.(map[string]any)["tool"] == tool {
			return true
		}
	}
	return false
}

// TestOpenAIAgentAgainstCanopy is the same for an OpenAI agent, which runs
// on Codex and never asks: CANOPY_TEST_CODEX=1 says the Canopy has Codex.
func TestOpenAIAgentAgainstCanopy(t *testing.T) {
	url := os.Getenv("CANOPY_TEST_URL")
	if url == "" || os.Getenv("CANOPY_TEST_CODEX") == "" {
		t.Skip("set CANOPY_TEST_URL, CANOPY_API_KEY and CANOPY_TEST_CODEX=1 for a Canopy that runs Codex with the scripted model")
	}
	key := os.Getenv("CANOPY_API_KEY")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	lab := openai.NewClient(oopt.WithBaseURL(url+"/openai/v1"), oopt.WithAPIKey(key))
	ag, err := lab.Beta.Agents.New(ctx, openai.BetaAgentNewParams{Name: openai.String("console-live-test-codex"), Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lab.Beta.Agents.Delete(context.Background(), ag.ID) })

	cl, err := agents.New(agents.Config{URL: url, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := connectWith(t, func(o *mcpapp.Options) { o.Agents = cl })
	page, _ := call(t, cs, "get_agent", map[string]any{"agent": ag.ID})
	if page["agent"].(map[string]any)["lab"] != "openai" || !strings.Contains(jsonString(page["definition"]), "console-live-test-codex") {
		t.Fatalf("get_agent: %s", jsonString(page))
	}
	pg, _ := call(t, cs, "start_session", map[string]any{"agent": ag.ID, "dry_run": true})
	if pg["network"] != "disabled" {
		t.Fatalf("playground: %s", jsonString(pg))
	}
	started, txt := call(t, cs, "start_session", map[string]any{"agent": ag.ID, "network": "disabled", "message": "run: echo hello-from-codex"})
	if started["kind"] != "session" {
		t.Fatalf("start: %s", txt)
	}
	id := started["session"].(map[string]any)["id"].(string)
	defer call(t, cs, "end_session", map[string]any{"session": id})
	done := waitFor(t, cs, id, func(p map[string]any) bool {
		turns := p["transcript"].(map[string]any)["turns"].([]any)
		return len(turns) == 1 && turns[0].(map[string]any)["status"] == "completed"
	})
	turn := done["transcript"].(map[string]any)["turns"].([]any)[0].(map[string]any)
	var ran bool
	for _, x := range turn["items"].([]any) {
		it := x.(map[string]any)
		if it["kind"] == "command_execution" && strings.Contains(jsonString(it["output"]), "hello-from-codex") {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("no command ran: %s", jsonString(turn))
	}
}

func waitFor(t *testing.T, cs *mcp.ClientSession, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		p, _ := call(t, cs, "get_session", map[string]any{"session": id})
		if ok(p) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out; last: %s", jsonString(p))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The agent tools are there only with a Canopy, and read-only leaves out
// everything that starts or changes a session.
func TestAgentToolsNeedCanopy(t *testing.T) {
	names := func(cs *mcp.ClientSession) map[string]bool {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, tl := range res.Tools {
			out[tl.Name] = true
		}
		return out
	}
	cl, err := agents.New(agents.Config{URL: "http://127.0.0.1:1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	without, _ := connectWith(t, func(o *mcpapp.Options) {})
	if names(without)["list_agents"] {
		t.Error("agent tools without a Canopy")
	}
	full, _ := connectWith(t, func(o *mcpapp.Options) { o.Agents = cl })
	ro, _ := connectWith(t, func(o *mcpapp.Options) { o.Agents = cl; o.ReadOnly = true })
	all, reads := names(full), names(ro)
	for _, n := range []string{"list_agents", "get_agent", "get_session", "start_session", "send_message", "answer_approval", "interrupt_session", "suspend_session", "resume_session", "end_session"} {
		if !all[n] {
			t.Errorf("missing %s", n)
		}
		if read := strings.HasPrefix(n, "list_") || strings.HasPrefix(n, "get_"); reads[n] != read {
			t.Errorf("read-only: %s registered = %v", n, reads[n])
		}
	}
	// The status result names the Canopy, which is how the view knows to
	// show Agents.
	st, _ := call(t, full, "status", nil)
	if st["canopy"] != "127.0.0.1:1" {
		t.Errorf("status canopy = %v", st["canopy"])
	}
	// A Canopy that isn't there is an error for the person, not a crash.
	res, err := full.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{}})
	if err != nil || !res.IsError {
		t.Fatalf("list_agents against nothing: %v %+v", err, res)
	}
}
