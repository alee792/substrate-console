// Package backend defines what the console needs from a Substrate cluster.
// The live implementation talks to ateapi and the Kubernetes API; the demo
// implementation serves a fixed example cluster for development and tests.
package backend

import (
	"context"
	"errors"
	"time"

	"github.com/alee792/substrate-console/internal/model"
)

// ErrUnsupported is returned when a backend cannot answer a request, for
// example a metric when no Prometheus is configured.
var ErrUnsupported = errors.New("not supported by this backend")

// Backend reads and changes a Substrate cluster.
type Backend interface {
	// Snapshot returns the whole cluster as of now.
	Snapshot(ctx context.Context) (*model.Cluster, error)
	// Timeline returns what the backend knows of an actor's history, newest
	// first.
	Timeline(ctx context.Context, ref model.ActorRef) ([]model.Event, error)
	// Logs returns up to tail recent log lines for an actor, oldest first.
	Logs(ctx context.Context, ref model.ActorRef, tail int) ([]model.LogLine, error)

	Suspend(ctx context.Context, ref model.ActorRef) error
	Resume(ctx context.Context, ref model.ActorRef) error
	Revert(ctx context.Context, ref model.ActorRef) error
	Delete(ctx context.Context, ref model.ActorRef) error
	Drain(ctx context.Context, worker string) error
}

// Metric names the console knows how to query. They map to upstream
// instruments; see internal/metrics.
type Metric string

const (
	MetricResumeP95      Metric = "resume_p95"
	MetricResumeFailures Metric = "resume_failure_rate"
	MetricCapacity       Metric = "capacity"
	MetricCrashes        Metric = "crashes"
	MetricRunning        Metric = "running_actors"
)

// MetricQuery asks for a metric over a window, optionally split by a label.
type MetricQuery struct {
	Metric   Metric
	By       string // "", "template", "pool"
	Atespace string // limits template-labelled metrics to one atespace
	Window   time.Duration
	Step     time.Duration
	// Offset shifts the window back, e.g. one week for a baseline.
	Offset time.Duration
}

// Metrics answers metric queries. A nil Metrics means none are available.
type Metrics interface {
	Query(ctx context.Context, q MetricQuery) ([]model.Series, error)
}
