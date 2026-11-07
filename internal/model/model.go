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
	Desired      int    `json:"desired"`
	Ready        int    `json:"ready"`
	// Busy is the number of ready workers hosting an actor.
	Busy int `json:"busy"`
	// NotReadySince is when Ready first fell below Desired, when known.
	NotReadySince *time.Time `json:"notReadySince,omitempty"`
}

// Template is one ActorTemplate.
type Template struct {
	Atespace    string `json:"atespace"`
	Name        string `json:"name"`
	Image       string `json:"image,omitempty"`
	MemoryLimit int64  `json:"memoryLimitBytes,omitempty"`
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
