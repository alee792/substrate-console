package agents

// The transcript is folded from the session's native log: canonical,
// harness-neutral records, the same for Claude Code and Codex. The console
// keeps each session's fold and reads only what was logged since, so a
// view polling a running session costs one short read.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/alee792/canopy/api/canopy"
)

// Turn is one exchange: what was asked and what the agent did.
type Turn struct {
	ID      string     `json:"id"`
	Status  string     `json:"status"` // queued, running, completed, failed, cancelled
	Input   string     `json:"input,omitempty"`
	Started *time.Time `json:"started,omitempty"`
	Ended   *time.Time `json:"ended,omitempty"`
	Items   []*Item    `json:"items"`
	Error   string     `json:"error,omitempty"`
	Usage   *Usage     `json:"usage,omitempty"`
	// Dropped counts items left out of a long turn, oldest first.
	Dropped int `json:"dropped,omitempty"`
}

// Item is one thing the agent did in a turn.
type Item struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"` // canopy.ItemKind
	Status   string   `json:"status"`
	Text     string   `json:"text,omitempty"`
	Command  string   `json:"command,omitempty"`
	Output   string   `json:"output,omitempty"`
	ExitCode *int     `json:"exitCode,omitempty"`
	Name     string   `json:"name,omitempty"`
	Server   string   `json:"server,omitempty"`
	Args     string   `json:"args,omitempty"`
	Changes  []Change `json:"changes,omitempty"`
	Error    string   `json:"error,omitempty"`
	Query    string   `json:"query,omitempty"`
	// Decision is how an approval for this item was answered.
	Decision string `json:"decision,omitempty"`
}

// Change is one file an item changed.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // add, delete, update
}

// Usage is a turn's token usage.
type Usage struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
}

// Waiting is something the harness is blocked on, from the native log.
type Waiting struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // approval or function_call
	ItemID  string `json:"itemId,omitempty"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	Subject string `json:"subject,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Args    string `json:"args,omitempty"`
}

// Limits on what a transcript holds, so a long session stays a small
// result. The log itself keeps everything.
const (
	maxTurns  = 40
	maxItems  = 120
	maxText   = 16 << 10
	maxOutput = 6 << 10
	maxArgs   = 2 << 10
	maxLogs   = 200
	logIdle   = 30 * time.Minute
)

// sessionLog is one session's fold, read up to cursor.
type sessionLog struct {
	ch       chan struct{} // a one-slot lock that a waiter can give up on
	cursor   int64
	turns    []*Turn
	byID     map[string]*Turn
	items    map[string]*Item
	queued   []string // messages sent that no turn has picked up yet
	waiting  map[string]Waiting
	order    []string // waiting ids in arrival order
	dropped  int      // turns left out from the start
	lastUsed time.Time
}

func newSessionLog() *sessionLog {
	l := &sessionLog{ch: make(chan struct{}, 1), byID: map[string]*Turn{}, items: map[string]*Item{}, waiting: map[string]Waiting{}}
	return l
}

func (l *sessionLog) lock(ctx context.Context) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *sessionLog) unlock() { <-l.ch }

// logFor returns the session's fold, sweeping folds nobody has read for a
// while.
func (c *Client) logFor(id string) *sessionLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if l := c.logs[id]; l != nil {
		l.lastUsed = now
		return l
	}
	if len(c.logs) >= maxLogs {
		var oldest string
		var at time.Time
		for k, l := range c.logs {
			if now.Sub(l.lastUsed) > logIdle {
				delete(c.logs, k)
				continue
			}
			if oldest == "" || l.lastUsed.Before(at) {
				oldest, at = k, l.lastUsed
			}
		}
		if len(c.logs) >= maxLogs {
			delete(c.logs, oldest)
		}
	}
	l := newSessionLog()
	l.lastUsed = now
	c.logs[id] = l
	return l
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.logs, id)
	c.mu.Unlock()
}

// preview is the first message of a session the console has read.
func (c *Client) preview(id string) string {
	c.mu.Lock()
	l := c.logs[id]
	c.mu.Unlock()
	if l == nil {
		return ""
	}
	// Don't wait on a read in progress; the preview is a nicety.
	select {
	case l.ch <- struct{}{}:
	default:
		return ""
	}
	defer l.unlock()
	if l.dropped == 0 && len(l.turns) > 0 {
		return clip(l.turns[0].Input, 140)
	}
	return ""
}

// Transcript is a session's turns so far.
type Transcript struct {
	Turns   []Turn    `json:"turns"`
	Waiting []Waiting `json:"waiting"`
	// Earlier counts turns left out from the start.
	Earlier int   `json:"earlier,omitempty"`
	Cursor  int64 `json:"cursor"`
}

// transcript reads what was logged since the last read and returns the
// whole fold.
func (c *Client) transcript(ctx context.Context, id string) (*Transcript, error) {
	l := c.logFor(id)
	if err := l.lock(ctx); err != nil {
		return nil, err
	}
	defer l.unlock()
	follow := false
	after := l.cursor
	st, err := c.native.StreamEvents(ctx, id, &canopy.StreamSessionEventsParams{After: &after, Follow: &follow})
	if err != nil {
		var ae *canopy.APIError
		if errors.As(err, &ae) {
			return nil, &Error{Status: ae.Status, Message: ae.Message}
		}
		return nil, err
	}
	defer st.Close()
	for {
		m, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Keep what was folded: the cursor only moved past records
			// that were applied.
			if ctx.Err() != nil {
				return nil, err
			}
			break
		}
		l.apply(m)
	}
	return l.snapshot(), nil
}

// apply folds one log message.
func (l *sessionLog) apply(m canopy.StreamMessage) {
	if m.Seq <= l.cursor {
		return
	}
	l.cursor = m.Seq
	r := m.Record
	switch m.Type {
	case canopy.RecordTypeUserInput:
		if r.Input.Type == canopy.InputTypeMessage {
			l.queued = append(l.queued, partsText(r.Input.Content))
		}
	case canopy.RecordTypeTurnQueued:
		l.turn(r.TurnId, m.Ts)
	case canopy.RecordTypeApproval:
		if it := l.items[r.ItemId]; it != nil {
			it.Decision = r.Decision
		}
		for id, w := range l.waiting {
			if w.Kind == "approval" && w.ItemID != "" && w.ItemID == r.ItemId {
				l.resolve(id)
			}
		}
	case canopy.RecordTypeEvent:
		l.event(r, m.Ts)
	}
}

func (l *sessionLog) turn(id string, ts time.Time) *Turn {
	if id == "" {
		return nil
	}
	if t := l.byID[id]; t != nil {
		return t
	}
	t := &Turn{ID: id, Status: "queued", Items: []*Item{}}
	if len(l.queued) > 0 {
		t.Input, l.queued = l.queued[0], l.queued[1:]
	}
	l.byID[id] = t
	l.turns = append(l.turns, t)
	if len(l.turns) > maxTurns {
		old := l.turns[0]
		for _, it := range old.Items {
			delete(l.items, it.ID)
		}
		delete(l.byID, old.ID)
		l.turns = l.turns[1:]
		l.dropped++
	}
	return t
}

func (l *sessionLog) event(r canopy.Record, ts time.Time) {
	ev := r.Event
	t := l.turn(r.TurnId, ts)
	switch ev.Type {
	case canopy.EventTypeTurnStarted:
		if t != nil {
			t.Status, t.Started = "running", &ts
		}
	case canopy.EventTypeTurnCompleted, canopy.EventTypeTurnFailed, canopy.EventTypeTurnCancelled:
		if t == nil {
			return
		}
		t.Status = map[canopy.EventType]string{canopy.EventTypeTurnCompleted: "completed", canopy.EventTypeTurnFailed: "failed", canopy.EventTypeTurnCancelled: "cancelled"}[ev.Type]
		t.Ended = &ts
		if ev.Error.Message != "" {
			t.Error = ev.Error.Message
		}
		if ev.Usage != nil {
			t.Usage = usageOf(*ev.Usage)
		}
		for _, it := range t.Items {
			if it.Status == "in_progress" {
				it.Status = "incomplete"
			}
		}
	case canopy.EventTypeUsage:
		if t != nil && ev.Usage != nil {
			t.Usage = usageOf(*ev.Usage)
		}
	case canopy.EventTypeItemStarted, canopy.EventTypeItemCompleted:
		if ev.Item.Kind == canopy.ItemKindUserMessage {
			// The turn's input. Use it when the client's message wasn't
			// in the log (a turn the harness started itself).
			if t != nil && t.Input == "" {
				t.Input = ev.Item.Text
			}
			return
		}
		l.item(t, r.ItemId, ev.Item)
	case canopy.EventTypeItemDelta:
		it := l.items[r.ItemId]
		if it == nil {
			it = l.item(t, r.ItemId, canopy.Item{Id: ev.Delta.ItemId, Kind: canopy.ItemKindAssistantMessage, Status: canopy.ItemStatusInProgress})
		}
		if it == nil {
			return
		}
		switch ev.Delta.Field {
		case canopy.DeltaFieldText, canopy.DeltaFieldReasoningSummary, canopy.DeltaFieldReasoningText:
			it.Text = clipTail(it.Text+ev.Delta.Text, maxText)
		case canopy.DeltaFieldCommandOutput:
			it.Output = clipTail(it.Output+ev.Delta.Text, maxOutput)
		}
	case canopy.EventTypeActionRequired:
		a := ev.Action
		if a.Id == "" {
			return
		}
		if _, ok := l.waiting[a.Id]; !ok {
			l.order = append(l.order, a.Id)
		}
		l.waiting[a.Id] = Waiting{ID: a.Id, Kind: string(a.Kind), ItemID: r.ItemId, Name: a.Name, Command: a.Command,
			Subject: a.Subject, Reason: a.Reason, Args: clip(compact(a.Arguments), maxArgs)}
	case canopy.EventTypeActionResolved:
		l.resolve(ev.Action.Id)
	case canopy.EventTypeError:
		if t != nil && ev.Error.Message != "" && !ev.Error.WillRetry {
			t.Error = ev.Error.Message
		}
	}
}

func (l *sessionLog) resolve(id string) {
	if _, ok := l.waiting[id]; !ok {
		return
	}
	delete(l.waiting, id)
	for i, x := range l.order {
		if x == id {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
}

// item creates or updates the item with Canopy's id.
func (l *sessionLog) item(t *Turn, id string, src canopy.Item) *Item {
	if id == "" {
		id = src.Id
	}
	it := l.items[id]
	if it == nil {
		if t == nil {
			return nil
		}
		it = &Item{ID: id}
		l.items[id] = it
		t.Items = append(t.Items, it)
		if len(t.Items) > maxItems {
			delete(l.items, t.Items[0].ID)
			t.Items = t.Items[1:]
			t.Dropped++
		}
	}
	it.Kind, it.Status = string(src.Kind), string(src.Status)
	// A completed item carries the whole text; a started one may carry
	// none, and deltas fill it in between.
	if src.Text != "" {
		it.Text = clipTail(src.Text, maxText)
	}
	if len(src.Summary) > 0 && it.Text == "" {
		it.Text = clipTail(strings.Join(src.Summary, "\n\n"), maxText)
	}
	set := func(dst *string, v string, n int) {
		if v != "" {
			*dst = clipTail(v, n)
		}
	}
	set(&it.Command, src.Command, maxArgs)
	set(&it.Output, src.Output, maxOutput)
	set(&it.Name, src.Name, 200)
	set(&it.Server, src.Server, 200)
	set(&it.Error, src.Error, maxArgs)
	set(&it.Query, src.Query, maxArgs)
	if a := compact(src.Arguments); a != "" {
		it.Args = clip(a, maxArgs)
	}
	if src.ExitCode != nil {
		it.ExitCode = src.ExitCode
	}
	if len(src.Changes) > 0 {
		it.Changes = it.Changes[:0]
		for _, ch := range src.Changes {
			it.Changes = append(it.Changes, Change{Path: ch.Path, Kind: string(ch.Kind)})
		}
	}
	return it
}

func (l *sessionLog) snapshot() *Transcript {
	out := &Transcript{Turns: make([]Turn, 0, len(l.turns)), Waiting: []Waiting{}, Earlier: l.dropped, Cursor: l.cursor}
	for _, t := range l.turns {
		cp := *t
		cp.Items = make([]*Item, len(t.Items))
		for i, it := range t.Items {
			c := *it
			c.Changes = append([]Change(nil), it.Changes...)
			cp.Items[i] = &c
		}
		if t.Usage != nil {
			u := *t.Usage
			cp.Usage = &u
		}
		out.Turns = append(out.Turns, cp)
	}
	for _, id := range l.order {
		out.Waiting = append(out.Waiting, l.waiting[id])
	}
	return out
}

func usageOf(u canopy.Usage) *Usage {
	return &Usage{Input: u.InputTokens, Cached: u.CachedInputTokens, Output: u.OutputTokens}
}

func partsText(parts []canopy.ContentPart) string {
	var b []string
	for _, p := range parts {
		switch p.Type {
		case canopy.ContentPartTypeText:
			b = append(b, p.Text)
		case canopy.ContentPartTypeImage:
			b = append(b, "[image]")
		}
	}
	return clip(strings.Join(b, "\n"), maxText)
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// clip keeps the start of s.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// clipTail keeps the end of s, where command output and streaming text
// have what's newest.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-n:], "")
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
