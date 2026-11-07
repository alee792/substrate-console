// Package ate is the live backend: it reads and changes a Substrate cluster
// through ateapi and the Kubernetes API, with the same credentials and
// connection path as kubectl-ate.
package ate

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/alee792/substrate-console/internal/backend"
	"github.com/alee792/substrate-console/internal/model"
)

// Config says how to reach the cluster.
type Config struct {
	Kubeconfig string
	Context    string
	// Endpoint is an ateapi address. Empty means port-forward to the api
	// Service, as kubectl-ate does.
	Endpoint  string
	TokenFile string
}

// Live is the live backend.
type Live struct {
	api   ateapi.ControlClient
	k8s   *kubernetes.Clientset
	dyn   dynamic.Interface
	close func()
	name  string

	mu      sync.Mutex
	workers map[string]string // "pool/worker" → Worker resource name
}

var _ backend.Backend = (*Live)(nil)

var workerPoolGVR = schema.GroupVersionResource{Group: "ate.dev", Version: "v1", Resource: "workerpools"}

// New connects to the cluster in the kubeconfig.
func New(ctx context.Context, cfg Config) (*Live, error) {
	rc, err := loadKubeConfig(cfg.Kubeconfig, cfg.Context)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	conn, stop, err := dial(ctx, rc, cs, cfg.Endpoint, cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	name := cfg.Context
	if name == "" {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = cfg.Kubeconfig
		if raw, err := rules.Load(); err == nil {
			name = raw.CurrentContext
		}
	}
	return &Live{api: ateapi.NewControlClient(conn), k8s: cs, dyn: dyn, close: stop, name: name, workers: map[string]string{}}, nil
}

// Close tears down the connection and port-forward.
func (l *Live) Close() { l.close() }

func t(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil || !ts.IsValid() || ts.AsTime().IsZero() {
		return nil
	}
	v := ts.AsTime()
	return &v
}

var states = map[ateapi.ActorState]model.ActorState{
	ateapi.ActorState_ACTOR_STATE_RESUMING:   model.StateResuming,
	ateapi.ActorState_ACTOR_STATE_RUNNING:    model.StateRunning,
	ateapi.ActorState_ACTOR_STATE_SUSPENDING: model.StateSuspending,
	ateapi.ActorState_ACTOR_STATE_SUSPENDED:  model.StateSuspended,
	ateapi.ActorState_ACTOR_STATE_CRASHED:    model.StateCrashed,
	ateapi.ActorState_ACTOR_STATE_DELETING:   model.StateDeleting,
	ateapi.ActorState_ACTOR_STATE_REVERTING:  model.StateReverting,
}

func lower(enumName, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(enumName, prefix))
}

func convertActor(a *ateapi.Actor, now time.Time) model.Actor {
	md, st := a.GetMetadata(), a.GetStatus()
	out := model.Actor{
		Atespace: md.GetAtespace(), Name: md.GetName(), UID: md.GetUid(),
		State: states[st.GetState()], Template: a.GetActorTemplate().GetName(),
		Node: st.GetAssignedNode(), CreatedAt: t(md.GetCreateTime()),
	}
	if out.State == "" {
		out.State = model.StateUnknown
	}
	// ateapi doesn't record when the state began; the last update to the
	// record is the closest it has, since a transition writes the record.
	out.StateSince = t(md.GetUpdateTime())
	if wa := st.GetWorkerAssignment(); wa != nil {
		w := wa.GetWorker().GetName()
		if w == "" {
			w = wa.GetWorkerPod()
		}
		out.Pool = wa.GetWorkerPool()
		out.Worker = wa.GetWorkerPool() + "/" + w
		if wa.GetNodeName() != "" {
			out.Node = wa.GetNodeName()
		}
		if rt := wa.GetSandboxRuntime(); rt != nil {
			out.SandboxClass = sandboxClass(rt.String())
		}
	}
	if c := st.GetCrash(); c != nil {
		out.Crash = &model.Crash{Message: c.GetMessage()}
		if ct := t(c.GetCrashTime()); ct != nil {
			out.Crash.Time = *ct
			out.StateSince = ct
		}
	}
	for _, s := range st.GetSnapshots() {
		sn := model.Snapshot{Generation: int(s.GetGeneration()), Fidelity: lower(s.GetFidelity().String(), "SNAPSHOT_FIDELITY_")}
		switch {
		case s.GetDurableSnapshot().GetState() == ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED:
			sn.Storage = "durable"
		case s.GetLocality() != "":
			sn.Storage = "local"
		default:
			sn.Storage = "pending"
		}
		out.Snapshots = append(out.Snapshots, sn)
	}
	sort.Slice(out.Snapshots, func(i, j int) bool { return out.Snapshots[i].Generation > out.Snapshots[j].Generation })
	if out.State == model.StateRunning {
		out.LastActive = &now
	} else {
		out.LastActive = out.StateSince
	}
	return out
}

// sandboxClass pulls a class name out of the runtime message's text form,
// which is enough for display.
func sandboxClass(s string) string {
	s = strings.ToLower(s)
	for _, c := range []string{"gvisor", "microvm"} {
		if strings.Contains(s, c) {
			return c
		}
	}
	return ""
}

// Snapshot implements backend.Backend.
func (l *Live) Snapshot(ctx context.Context) (*model.Cluster, error) {
	now := time.Now()
	out := &model.Cluster{Name: l.name, FetchedAt: now}

	var token string
	for {
		r, err := l.api.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{Options: &ateapi.ListOptions{PageSize: 500, PageToken: token}})
		if err != nil {
			return nil, fmt.Errorf("list atespaces: %w", err)
		}
		for _, a := range r.GetAtespaces() {
			out.Atespaces = append(out.Atespaces, a.GetMetadata().GetName())
		}
		if token = r.GetNextPageToken(); token == "" {
			break
		}
	}
	sort.Strings(out.Atespaces)

	for _, space := range out.Atespaces {
		token = ""
		for {
			r, err := l.api.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: space, Options: &ateapi.ListOptions{PageSize: 500, PageToken: token}})
			if err != nil {
				return nil, fmt.Errorf("list actors in %s: %w", space, err)
			}
			for _, a := range r.GetActors() {
				out.Actors = append(out.Actors, convertActor(a, now))
			}
			if token = r.GetNextPageToken(); token == "" {
				break
			}
		}
		token = ""
		for {
			r, err := l.api.ListActorTemplates(ctx, &ateapi.ListActorTemplatesRequest{Atespace: space, Options: &ateapi.ListOptions{PageSize: 500, PageToken: token}})
			if err != nil {
				return nil, fmt.Errorf("list templates in %s: %w", space, err)
			}
			for _, tm := range r.GetActorTemplates() {
				mt := model.Template{Atespace: space, Name: tm.GetMetadata().GetName(), MemoryLimit: tm.GetResources().GetLimits().GetMemoryBytes()}
				for _, c := range tm.GetContainers() {
					if mt.Image == "" {
						mt.Image = c.GetImage()
					}
					if mt.MemoryLimit == 0 {
						mt.MemoryLimit += c.GetResources().GetLimits().GetMemoryBytes()
					}
				}
				out.Templates = append(out.Templates, mt)
			}
			if token = r.GetNextPageToken(); token == "" {
				break
			}
		}
	}
	// A template's memory limit becomes each actor's limit.
	limits := map[string]int64{}
	for _, tm := range out.Templates {
		limits[tm.Atespace+"/"+tm.Name] = tm.MemoryLimit
	}
	for i := range out.Actors {
		out.Actors[i].MemoryLimit = limits[out.Actors[i].Atespace+"/"+out.Actors[i].Template]
	}

	workerNames := map[string]string{}
	token = ""
	for {
		r, err := l.api.ListWorkers(ctx, &ateapi.ListWorkersRequest{Options: &ateapi.ListOptions{PageSize: 500, PageToken: token}})
		if err != nil {
			return nil, fmt.Errorf("list workers: %w", err)
		}
		for _, w := range r.GetWorkers() {
			name := w.GetMetadata().GetName()
			mw := model.Worker{Name: name, Pool: w.GetWorkerPool(), Node: w.GetNodeName(), State: lower(w.GetStatus().GetState().String(), "WORKER_STATE_")}
			out.Workers = append(out.Workers, mw)
			workerNames[mw.Pool+"/"+name] = name
			if pod := w.GetWorkerPod(); pod != "" {
				workerNames[mw.Pool+"/"+pod] = name
			}
		}
		if token = r.GetNextPageToken(); token == "" {
			break
		}
	}
	l.mu.Lock()
	l.workers = workerNames
	l.mu.Unlock()

	busy := map[string]int{}
	assigned := map[string]int{}
	for _, a := range out.Actors {
		if a.Worker != "" {
			assigned[a.Worker]++
		}
	}
	for i := range out.Workers {
		w := &out.Workers[i]
		w.Actors = assigned[w.Pool+"/"+w.Name]
		if w.Actors > 0 {
			busy[w.Pool]++
		}
	}

	pools, err := l.dyn.Resource(workerPoolGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list WorkerPools: %w", err)
	}
	for _, p := range pools.Items {
		desired, _, _ := unstructuredInt(p.Object, "spec", "replicas")
		ready, _, _ := unstructuredInt(p.Object, "status", "readyReplicas")
		mp := model.Pool{Namespace: p.GetNamespace(), Name: p.GetName(), Desired: int(desired), Ready: int(ready), Busy: busy[p.GetName()]}
		if mp.Ready < mp.Desired {
			mp.NotReadySince = notReadySince(p.Object)
		}
		out.Pools = append(out.Pools, mp)
	}
	sort.Slice(out.Pools, func(i, j int) bool { return out.Pools[i].Name < out.Pools[j].Name })
	return out, nil
}

func unstructuredInt(obj map[string]any, path ...string) (int64, bool, error) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false, nil
		}
		cur = m[p]
	}
	switch v := cur.(type) {
	case int64:
		return v, true, nil
	case float64:
		return int64(v), true, nil
	}
	return 0, false, nil
}

// notReadySince reads the last transition time of a false Ready-like
// condition, when the pool reports one.
func notReadySince(obj map[string]any) *time.Time {
	status, _ := obj["status"].(map[string]any)
	conds, _ := status["conditions"].([]any)
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["status"] == "False" {
			if s, ok := m["lastTransitionTime"].(string); ok {
				if tt, err := time.Parse(time.RFC3339, s); err == nil {
					return &tt
				}
			}
		}
	}
	return nil
}

func ref(r model.ActorRef) *ateapi.ObjectRef {
	return &ateapi.ObjectRef{Atespace: r.Atespace, Name: r.Name}
}

// Timeline implements backend.Backend. ateapi keeps no history, so v0.1
// reports what the record holds: creation, the crash if any, and the current
// state. A full timeline needs the ate.actor.state_changed log events (see
// docs/architecture.md).
func (l *Live) Timeline(ctx context.Context, r model.ActorRef) ([]model.Event, error) {
	a, err := l.api.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref(r)})
	if err != nil {
		return nil, err
	}
	ma := convertActor(a, time.Now())
	var ev []model.Event
	if ma.StateSince != nil {
		e := model.Event{Time: *ma.StateSince, State: ma.State}
		if ma.Crash != nil {
			e.Detail = ma.Crash.Message
		}
		ev = append(ev, e)
	}
	if ma.CreatedAt != nil {
		ev = append(ev, model.Event{Time: *ma.CreatedAt, State: "created", Detail: "from " + ma.Template})
	}
	return ev, nil
}

// Logs implements backend.Backend, reading the worker pod's log as
// `kubectl ate logs` does. It only works while the actor runs.
func (l *Live) Logs(ctx context.Context, r model.ActorRef, tail int) ([]model.LogLine, error) {
	a, err := l.api.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref(r)})
	if err != nil {
		return nil, err
	}
	wa := a.GetStatus().GetWorkerAssignment()
	if a.GetStatus().GetState() != ateapi.ActorState_ACTOR_STATE_RUNNING || wa.GetWorkerPod() == "" {
		return nil, fmt.Errorf("%s is not running, and logs of past runs need a log backend", r)
	}
	n := int64(tail)
	stream, err := l.k8s.CoreV1().Pods(wa.GetWorkerNamespace()).GetLogs(wa.GetWorkerPod(), &corev1.PodLogOptions{TailLines: &n, Timestamps: true}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}
	defer stream.Close()
	var out []model.LogLine
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		ts, rest, _ := strings.Cut(line, " ")
		tt, _ := time.Parse(time.RFC3339Nano, ts)
		out = append(out, model.LogLine{Time: tt, Source: "worker", Text: rest})
	}
	return out, sc.Err()
}

// Suspend implements backend.Backend.
func (l *Live) Suspend(ctx context.Context, r model.ActorRef) error {
	_, err := l.api.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref(r)})
	return err
}

// Resume implements backend.Backend.
func (l *Live) Resume(ctx context.Context, r model.ActorRef) error {
	_, err := l.api.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref(r)})
	return err
}

// Revert implements backend.Backend.
func (l *Live) Revert(ctx context.Context, r model.ActorRef) error {
	_, err := l.api.RevertActor(ctx, &ateapi.RevertActorRequest{Actor: ref(r)})
	return err
}

// Delete implements backend.Backend.
func (l *Live) Delete(ctx context.Context, r model.ActorRef) error {
	_, err := l.api.DeleteActor(ctx, &ateapi.DeleteActorRequest{Actor: ref(r)})
	return err
}

// Drain implements backend.Backend. worker is "pool/worker" as the console
// shows it.
func (l *Live) Drain(ctx context.Context, worker string) error {
	l.mu.Lock()
	name, ok := l.workers[worker]
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("worker %s not found", worker)
	}
	_, err := l.api.DrainWorker(ctx, &ateapi.DrainWorkerRequest{Worker: &ateapi.ObjectRef{Name: name}})
	return err
}
