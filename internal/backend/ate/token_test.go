package ate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	substratefake "github.com/alee792/substrate-fake"

	"github.com/alee792/substrate-console/internal/backend"
)

// A token command is run once and its token kept until shortly before it
// expires; then it runs again.
func TestTokenCommand(t *testing.T) {
	ctx := context.Background()
	fc, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	dir := t.TempDir()
	tokFile, runs := filepath.Join(dir, "token"), filepath.Join(dir, "runs")
	// Expires within the minute of slack, so every call refreshes it.
	short := fc.World.IssueToken(substratefake.DemoOnCall, substratefake.TokenOptions{TTL: 30 * time.Second})
	if err := os.WriteFile(tokFile, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := "echo run >> " + runs + "; cat " + tokFile
	l, err := New(ctx, Config{Kubeconfig: fc.Kubeconfig, Context: fc.Context, Endpoint: fc.APIEndpoint, TokenCommand: cmd})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for range 2 {
		c, err := l.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c.Access.Principal != substratefake.DemoOnCall || c.Access.Shared || !c.Access.Workers {
			t.Fatalf("access %+v", c.Access)
		}
	}
	b, _ := os.ReadFile(runs)
	if n := strings.Count(string(b), "run"); n < 2 {
		t.Errorf("command ran %d times for a token about to expire", n)
	}

	// A longer-lived token is kept.
	long := fc.World.IssueToken(substratefake.DemoOnCall, substratefake.TokenOptions{TTL: time.Hour})
	_ = os.WriteFile(tokFile, []byte(long), 0o600)
	_ = os.Remove(runs)
	ct := &cachedToken{fetch: func(ctx context.Context) (string, error) { return runTokenCommand(ctx, cmd) }}
	for range 3 {
		if _, err := ct.get(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") != 1 {
		t.Errorf("command ran %d times for a fresh token", strings.Count(string(b), "run"))
	}
}

// In forward mode a call with no caller fails instead of falling back to
// the console's identity.
func TestForwardNeverFallsBack(t *testing.T) {
	ctx := context.Background()
	fc, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	l, err := New(ctx, Config{Kubeconfig: fc.Kubeconfig, Context: fc.Context, Endpoint: fc.APIEndpoint, Forward: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Snapshot(ctx); err == nil || !strings.Contains(err.Error(), "no caller token") {
		t.Fatalf("snapshot with no caller: %v", err)
	}
	ctx = backend.WithCaller(ctx, backend.Caller{Token: fc.World.IssueToken(substratefake.DemoOnCall, substratefake.TokenOptions{})})
	if c, err := l.Snapshot(ctx); err != nil || c.Access.Principal != substratefake.DemoOnCall {
		t.Fatalf("snapshot as caller: %v %+v", err, c)
	}
}

// With a ServiceAccount, the console gets tokens for the person's own
// account from Kubernetes and reads the cluster as them.
func TestServiceAccount(t *testing.T) {
	ctx := context.Background()
	fc, err := substratefake.Start(ctx, substratefake.Options{Scenario: substratefake.Demo})
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	l, err := New(ctx, Config{Kubeconfig: fc.Kubeconfig, Context: fc.Context, Endpoint: fc.APIEndpoint,
		ServiceAccount: "ate-users/triage-dev", Atespaces: []string{"triage", "ioc-enrich"}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := l.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Access.Principal != substratefake.DemoTriageDev || c.Access.Shared || c.Access.AllAtespaces {
		t.Errorf("access %+v", c.Access)
	}
	if len(c.Atespaces) != 1 || c.Atespaces[0] != "triage" {
		t.Errorf("atespaces %+v", c.Atespaces)
	}
}

// Kubernetes RBAC decides who may act as a ServiceAccount; a refusal says
// which permission is missing.
func TestServiceAccountForbidden(t *testing.T) {
	cs := k8sfake.NewClientset()
	cs.PrependReactor("create", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, "alice", errors.New("RBAC: access denied"))
	})
	_, err := ownToken(context.Background(), cs, Config{ServiceAccount: "ate-users/alice"})
	if err == nil || !strings.Contains(err.Error(), "needs create on serviceaccounts/token") {
		t.Errorf("err = %v", err)
	}
}
