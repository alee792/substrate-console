package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alee792/substrate-console/internal/model"
)

// ActorsIn names the actors a write tool acts on.
type ActorsIn struct {
	Actors []model.ActorRef `json:"actors" jsonschema:"the actors to act on, each with atespace and name"`
	DryRun bool             `json:"dry_run,omitempty" jsonschema:"report what would change without changing anything"`
}

// DrainIn names a worker.
type DrainIn struct {
	Worker string `json:"worker" jsonschema:"pool/worker, e.g. gvisor-pool-a/w-03"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"report what would change without changing anything"`
}

// Target is one actor in a write result.
type Target struct {
	model.ActorRef
	State  model.ActorState `json:"state"`
	OK     bool             `json:"ok"`
	Reason string           `json:"reason,omitempty"`
}

// ActionResult is every write tool's result.
type ActionResult struct {
	Kind    string   `json:"kind"` // "action"
	Verb    string   `json:"verb"`
	Tool    string   `json:"tool"`
	DryRun  bool     `json:"dryRun"`
	Summary string   `json:"summary"`
	Targets []Target `json:"targets"`
	// Note is a sentence about consequences the view shows above Confirm.
	Note string `json:"note,omitempty"`
	// Args repeats the call so a view can confirm a dry run by calling the
	// tool again with dry_run=false.
	Args map[string]any `json:"args"`
}

type verb struct {
	tool, name, past string
	desc             string
	destructive      bool
	idempotent       bool
	// eligible says whether the verb applies to an actor in this state.
	eligible func(model.Actor) (bool, string)
	do       func(context.Context, model.ActorRef) error
}

func (s *Server) verbs() []verb {
	b := s.opts.Backend
	return []verb{
		{
			tool: "suspend_actors", name: "suspend", past: "suspended",
			desc:       "Suspend running actors. Their state is snapshotted and they stop using a worker. Use dry_run first.",
			idempotent: true, do: b.Suspend,
			eligible: func(a model.Actor) (bool, string) {
				return a.State == model.StateRunning, "only running actors can be suspended"
			},
		},
		{
			tool: "resume_actors", name: "resume", past: "resumed",
			desc:       "Resume suspended actors from their latest snapshot. Use dry_run first.",
			idempotent: true, do: b.Resume,
			eligible: func(a model.Actor) (bool, string) {
				return a.State == model.StateSuspended, "only suspended actors can be resumed"
			},
		},
		{
			tool: "revert_actors", name: "revert", past: "reverted",
			desc:        "Revert crashed or stuck actors to their latest snapshot, which leaves them suspended. Work since that snapshot is lost. Use dry_run first.",
			destructive: true, do: b.Revert,
			eligible: func(a model.Actor) (bool, string) {
				if a.State != model.StateCrashed && !a.State.Transitional() {
					return false, "only crashed or stuck actors can be reverted"
				}
				if len(a.Snapshots) == 0 {
					return false, "no snapshot to revert to"
				}
				return true, ""
			},
		},
		{
			tool: "delete_actors", name: "delete", past: "deleted",
			desc:        "Delete actors and their snapshots. This cannot be undone. Use dry_run first.",
			destructive: true, do: b.Delete,
			eligible: func(a model.Actor) (bool, string) {
				return a.State != model.StateDeleting, "already deleting"
			},
		},
	}
}

func (s *Server) addWriteTools(srv *mcp.Server) {
	for _, v := range s.verbs() {
		v := v
		mcp.AddTool(srv, &mcp.Tool{
			Name: v.tool, Description: v.desc,
			Annotations: write(strings.ToUpper(v.name[:1])+v.name[1:]+" actors", v.destructive, v.idempotent),
			Meta:        toolMeta(strings.ToUpper(v.name[:1])+v.name[1:]+"ing actors…", "Done"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in ActorsIn) (*mcp.CallToolResult, any, error) {
			return s.runVerb(ctx, v, in)
		})
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "drain_worker",
		Description: "Drain a worker: stop placing new actors on it. Actors already there keep running until they suspend or move. Use dry_run first.",
		Annotations: write("Drain worker", true, true),
		Meta:        toolMeta("Draining worker…", "Drained worker"),
	}, s.drain)
}

func (s *Server) runVerb(ctx context.Context, v verb, in ActorsIn) (*mcp.CallToolResult, any, error) {
	if len(in.Actors) == 0 {
		return nil, nil, fmt.Errorf("name at least one actor")
	}
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	res := ActionResult{Kind: "action", Verb: v.name, Tool: v.tool, DryRun: in.DryRun,
		Args: map[string]any{"actors": in.Actors}}
	for _, ref := range in.Actors {
		t := Target{ActorRef: ref}
		a, ok := findActor(c, ref)
		if !ok {
			t.Reason = "not found"
			res.Targets = append(res.Targets, t)
			continue
		}
		t.State = a.State
		ok, why := v.eligible(a)
		if !ok {
			t.Reason = why
			res.Targets = append(res.Targets, t)
			continue
		}
		if in.DryRun {
			t.OK = true
		} else if err := v.do(ctx, ref); err != nil {
			t.Reason = explain(err, "can't "+v.name+" actors in "+ref.Atespace)
		} else {
			t.OK = true
		}
		res.Targets = append(res.Targets, t)
	}
	if !in.DryRun {
		s.invalidate()
	}
	res.Summary = summarize(v.name, v.past, res)
	return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
}

func (s *Server) drain(ctx context.Context, _ *mcp.CallToolRequest, in DrainIn) (*mcp.CallToolResult, any, error) {
	c, err := s.snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	found := false
	for _, w := range c.Workers {
		if w.Pool+"/"+w.Name == in.Worker {
			found = true
		}
	}
	if !found && !c.Access.Workers {
		return nil, nil, fmt.Errorf("draining workers needs cluster-wide owner access, which %s doesn't have", orYou(c.Access.Principal))
	}
	if !found {
		return nil, nil, fmt.Errorf("worker %q not found; use pool/worker as list_worker_pools shows it", in.Worker)
	}
	res := ActionResult{Kind: "action", Verb: "drain", Tool: "drain_worker", DryRun: in.DryRun,
		Args: map[string]any{"worker": in.Worker}}
	for _, a := range c.Actors {
		if a.Worker == in.Worker && a.State == model.StateRunning {
			res.Targets = append(res.Targets, Target{ActorRef: a.Ref(), State: a.State, OK: true})
		}
	}
	if !in.DryRun {
		if err := s.opts.Backend.Drain(ctx, in.Worker); err != nil {
			return nil, nil, errors.New(explain(err, "can't drain workers; that needs cluster owner"))
		}
		s.invalidate()
	}
	if in.DryRun {
		res.Summary = fmt.Sprintf("Drain %s? Nothing new will be placed on it.", in.Worker)
		if n := len(res.Targets); n > 0 {
			res.Note = fmt.Sprintf("The %d %s on it keep running.", n, map[bool]string{true: "actor", false: "actors"}[n == 1])
		}
	} else {
		res.Summary = fmt.Sprintf("Drained %s. Nothing new will be placed on it.", in.Worker)
	}
	return &mcp.CallToolResult{Content: text(actionText(res))}, res, nil
}

func summarize(name, past string, r ActionResult) string {
	ok, skip := 0, 0
	for _, t := range r.Targets {
		if t.OK {
			ok++
		} else {
			skip++
		}
	}
	noun := func(n int) string {
		if n == 1 {
			return "1 actor"
		}
		return fmt.Sprintf("%d actors", n)
	}
	var s string
	if r.DryRun {
		s = fmt.Sprintf("%s %s?", strings.ToUpper(name[:1])+name[1:], noun(ok))
		if skip > 0 {
			s += fmt.Sprintf(" %d skipped.", skip)
		}
		return s
	}
	s = fmt.Sprintf("%s %s.", strings.ToUpper(past[:1])+past[1:], noun(ok))
	if skip > 0 {
		s += fmt.Sprintf(" %d skipped.", skip)
	}
	return s
}

func actionText(r ActionResult) string {
	var b strings.Builder
	b.WriteString(r.Summary)
	for _, t := range r.Targets {
		mark := "ok"
		if !t.OK {
			mark = "skipped: " + t.Reason
		}
		fmt.Fprintf(&b, "\n- %s (%s) %s", t.ActorRef, t.State, mark)
	}
	if r.DryRun {
		b.WriteString("\nCall again with dry_run=false to apply.")
	}
	return b.String()
}

// explain words an ateapi error for the person who hit it. A denial says
// what they can't do, since ateapi's message names relations, not tasks.
func explain(err error, deniedMsg string) string {
	switch status.Code(err) {
	case codes.PermissionDenied:
		return "you " + deniedMsg
	case codes.Unauthenticated:
		return "ateapi didn't accept your token; it may have expired"
	}
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}
