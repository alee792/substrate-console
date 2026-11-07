// Package model holds the console's view of a Substrate cluster. The types
// are what tools return as structuredContent, so their JSON shape is part of
// the console's public contract with views and agents.
package model

import "time"

// ActorState is an actor's lifecycle state, in lower case without the
// ACTOR_STATE_ prefix the ateapi enum uses.
type ActorState string

const (
	StateResuming   ActorState = "resuming"
	StateRunning    ActorState = "running"
	StateSuspending ActorState = "suspending"
	StateSuspended  ActorState = "suspended"
	StateCrashed    ActorState = "crashed"
	StateDeleting   ActorState = "deleting"
	StateReverting  ActorState = "reverting"
	StateUnknown    ActorState = "unknown"
)

// Transitional reports whether the state is one an actor should leave on
// its own within seconds.
func (s ActorState) Transitional() bool {
	switch s {
	case StateResuming, StateSuspending, StateDeleting, StateReverting:
		return true
	}
	return false
}

// ActorRef names an actor.
type ActorRef struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
}

func (r ActorRef) String() string { return r.Atespace + "/" + r.Name }

// Actor is one actor as the console shows it.
type Actor struct {
	Atespace string     `json:"atespace"`
	Name     string     `json:"name"`
	UID      string     `json:"uid,omitempty"`
	State    ActorState `json:"state"`
	// StateSince is when the actor entered State, when known. ateapi does not
	// record it, so the live backend uses the first time the console saw the
	// state, or the crash time.
	StateSince *time.Time `json:"stateSince,omitempty"`
	Template   string     `json:"template"`
	// Worker is "pool/worker" while assigned, or the last worker for a
	// crashed actor when known.
	Worker       string     `json:"worker,omitempty"`
	Pool         string     `json:"pool,omitempty"`
	Node         string     `json:"node,omitempty"`
	SandboxClass string     `json:"sandboxClass,omitempty"`
	Crash        *Crash     `json:"crash,omitempty"`
	Snapshots    []Snapshot `json:"snapshots,omitempty"`
	LastActive   *time.Time `json:"lastActive,omitempty"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
	MemoryBytes  int64      `json:"memoryBytes,omitempty"`
	MemoryLimit  int64      `json:"memoryLimitBytes,omitempty"`
	ResumeP95Ms  float64    `json:"resumeP95Ms,omitempty"`
}

// Ref returns the actor's reference.
func (a Actor) Ref() ActorRef { return ActorRef{Atespace: a.Atespace, Name: a.Name} }

// Crash says why an actor entered CRASHED.
type Crash struct {
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// Snapshot is one snapshot generation an actor holds.
type Snapshot struct {
	Generation int    `json:"generation"`
	Fidelity   string `json:"fidelity"` // memory, rootfs, volumes
	Storage    string `json:"storage"`  // local, durable, pending
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

// Worker is one ateworker.
type Worker struct {
	Name     string     `json:"name"`
	Pool     string     `json:"pool"`
	Node     string     `json:"node,omitempty"`
	State    string     `json:"state"` // active, draining, unavailable
	Actors   int        `json:"actors"`
	LastSeen *time.Time `json:"lastSeen,omitempty"`
}

// Pool is one WorkerPool.
type Pool struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	SandboxClass string `json:"sandboxClass,omitempty"`
	// Labels are what a template's worker selector matches.
	Labels  map[string]string `json:"labels,omitempty"`
	Desired int               `json:"desired"`
	Ready   int               `json:"ready"`
	// Busy is the number of ready workers hosting an actor.
	Busy int `json:"busy"`
	// NotReadySince is when Ready first fell below Desired, when known.
	NotReadySince *time.Time `json:"notReadySince,omitempty"`
}

// Template is one ActorTemplate. Templates are immutable: a change is a
// new template, which by convention gets a version suffix (agent-devbox-v12).
type Template struct {
	Atespace   string      `json:"atespace"`
	Name       string      `json:"name"`
	UID        string      `json:"uid,omitempty"`
	CreatedAt  *time.Time  `json:"createdAt,omitempty"`
	Containers []Container `json:"containers,omitempty"`
	// Image is the first container's image, for one-line summaries.
	Image       string `json:"image,omitempty"`
	CPUMillis   int64  `json:"cpuMillicores,omitempty"`
	MemoryLimit int64  `json:"memoryLimitBytes,omitempty"`
	// WorkerSelector is matched against worker pool labels.
	WorkerSelector map[string]string `json:"workerSelector,omitempty"`
	// Fidelity is the snapshot fidelity every suspend captures.
	Fidelity string `json:"fidelity,omitempty"` // memory, rootfs, volumes, or "" for the default
	Golden   Golden `json:"golden"`
}

// Container is one container in a template. Env values are never read.
type Container struct {
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	Command  []string `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	EnvNames []string `json:"envNames,omitempty"`
	Probe    string   `json:"probe,omitempty"` // wakeup probe, e.g. ":8080/readyz"
}

// Golden is the template's golden snapshot: a booted, warmed-up actor that
// new actors start from instead of a cold boot.
type Golden struct {
	State string     `json:"state"` // ready, building, failed, off
	Tag   string     `json:"tag,omitempty"`
	Time  *time.Time `json:"time,omitempty"`
	Error string     `json:"error,omitempty"`
}

// TemplateSpec is what a person fills in to create a template. It covers
// one container, which is what most agents need; "new version" of a
// multi-container template keeps the others as they are.
type TemplateSpec struct {
	Atespace  string            `json:"atespace" jsonschema:"atespace to create the template in"`
	Name      string            `json:"name,omitempty" jsonschema:"template name; lowercase, digits, dashes. With from, defaults to the next version"`
	Image     string            `json:"image,omitempty" jsonschema:"container image, ideally pinned by digest"`
	Command   []string          `json:"command,omitempty" jsonschema:"container entrypoint"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty" jsonschema:"environment variables; with from, omit to keep the source's"`
	CPUMillis int64             `json:"cpuMillicores,omitempty" jsonschema:"CPU limit in millicores (1000 = one core)"`
	MemoryMiB int64             `json:"memoryMiB,omitempty" jsonschema:"memory limit in MiB"`
	// Pool pins the template to one worker pool (and so its sandbox class)
	// through that pool's labels. Empty means any pool.
	Pool     string `json:"pool,omitempty" jsonschema:"worker pool to pin to (sets the sandbox class); omit for any pool"`
	Fidelity string `json:"fidelity,omitempty" jsonschema:"snapshot fidelity: memory, rootfs or volumes"`
	// Golden builds a golden snapshot for new actors to start from. Nil
	// means the default: on, or the source's setting for a new version.
	Golden *bool `json:"golden,omitempty" jsonschema:"build a golden snapshot new actors start from; default on"`
	// From is the template this one is a new version of, if any.
	From string `json:"from,omitempty" jsonschema:"existing template this is a new version of"`
}

// Cluster is everything the console reads in one poll.
type Cluster struct {
	Name      string     `json:"name"`
	Version   string     `json:"version,omitempty"`
	FetchedAt time.Time  `json:"fetchedAt"`
	Atespaces []string   `json:"atespaces"`
	Actors    []Actor    `json:"actors"`
	Workers   []Worker   `json:"workers"`
	Pools     []Pool     `json:"pools"`
	Templates []Template `json:"templates"`
}

// Event is one entry on an actor's timeline.
type Event struct {
	Time   time.Time  `json:"time"`
	State  ActorState `json:"state"`
	Detail string     `json:"detail,omitempty"`
}

// LogLine is one line of actor or component output.
type LogLine struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Text   string    `json:"text"`
	Error  bool      `json:"error,omitempty"`
}

// Point is one sample of a series.
type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// Series is a named time series.
type Series struct {
	Label  string  `json:"label"`
	Points []Point `json:"points"`
}
