package ate

import (
	"testing"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alee792/substrate-console/internal/model"
)

func TestConvertCrashedActor(t *testing.T) {
	crash := time.Date(2026, 11, 7, 16, 2, 41, 0, time.UTC)
	a := &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: "triage", Name: "triage-0d55", Uid: "u1", UpdateTime: timestamppb.New(crash.Add(time.Second))},
		ActorTemplate: &ateapi.ObjectRef{Atespace: "triage", Name: "agent-devbox-v11"},
		Status: &ateapi.ActorStatus{
			State:        ateapi.ActorState_ACTOR_STATE_CRASHED,
			AssignedNode: "kind-worker3",
			Crash:        &ateapi.ActorCrash{Message: "oom", CrashTime: timestamppb.New(crash)},
			Snapshots: []*ateapi.Snapshot{
				{Generation: 13, Fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, DurableSnapshot: &ateapi.SnapshotStorage{State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED}},
				{Generation: 14, Fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, Locality: "kind-worker3"},
			},
		},
	}
	got := convertActor(a, crash.Add(time.Hour))
	if got.State != model.StateCrashed || got.Node != "kind-worker3" || got.Worker != "" {
		t.Errorf("state/node/worker = %s/%s/%s", got.State, got.Node, got.Worker)
	}
	if got.StateSince == nil || !got.StateSince.Equal(crash) {
		t.Errorf("stateSince = %v, want crash time", got.StateSince)
	}
	if len(got.Snapshots) != 2 || got.Snapshots[0].Generation != 14 || got.Snapshots[0].Storage != "local" || got.Snapshots[1].Storage != "durable" || got.Snapshots[0].Fidelity != "memory" {
		t.Errorf("snapshots = %+v", got.Snapshots)
	}
}

func TestConvertRunningActor(t *testing.T) {
	now := time.Now()
	a := &ateapi.Actor{
		Metadata: &ateapi.ResourceMetadata{Atespace: "triage", Name: "x"},
		Status: &ateapi.ActorStatus{
			State:            ateapi.ActorState_ACTOR_STATE_RUNNING,
			WorkerAssignment: &ateapi.WorkerAssignment{Worker: &ateapi.ObjectRef{Name: "w-07"}, WorkerPool: "gvisor-pool-a", NodeName: "n1"},
		},
	}
	got := convertActor(a, now)
	if got.Worker != "gvisor-pool-a/w-07" || got.Pool != "gvisor-pool-a" || got.Node != "n1" || got.LastActive == nil || !got.LastActive.Equal(now) {
		t.Errorf("got %+v", got)
	}
}
