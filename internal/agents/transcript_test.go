package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alee792/canopy/api/canopy"
)

// testdata/approvals.jsonl is a real session log (native lines left out):
// Claude Code on Canopy with the scripted model, an agent whose Bash calls
// ask first. Turn one runs "echo hello" and is allowed; turn two runs
// "false" and is denied.
func loadLog(t *testing.T, name string) []canopy.StreamMessage {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []canopy.StreamMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m canopy.StreamMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestTranscriptFromALog(t *testing.T) {
	l := newSessionLog()
	msgs := loadLog(t, "approvals.jsonl")
	for _, m := range msgs {
		l.apply(m)
	}
	tr := l.snapshot()
	if len(tr.Turns) != 2 || len(tr.Waiting) != 0 {
		t.Fatalf("turns %d, waiting %d", len(tr.Turns), len(tr.Waiting))
	}
	one, two := tr.Turns[0], tr.Turns[1]
	if one.Input != "run: echo hello" || one.Status != "completed" || one.Started == nil || one.Ended == nil || one.Usage == nil {
		t.Fatalf("turn one: %+v", one)
	}
	if len(one.Items) != 2 {
		t.Fatalf("turn one items: %s", js(one.Items))
	}
	cmd, say := one.Items[0], one.Items[1]
	if cmd.Kind != "command_execution" || cmd.Command != "echo hello" || cmd.Output != "hello" || cmd.ExitCode == nil || *cmd.ExitCode != 0 || cmd.Decision != "allow" {
		t.Errorf("command: %s", js(cmd))
	}
	if say.Kind != "assistant_message" || !strings.Contains(say.Text, "hello") || say.Status != "completed" {
		t.Errorf("reply: %s", js(say))
	}
	if two.Input != "run: false" || two.Items[0].Decision != "deny" || two.Items[0].Status != "failed" || *two.Items[0].ExitCode != 1 {
		t.Errorf("turn two: %s", js(two))
	}

	// Reading the same records again changes nothing.
	for _, m := range msgs {
		l.apply(m)
	}
	if again := l.snapshot(); js(again) != js(tr) {
		t.Error("re-applying the log changed the transcript")
	}
}

func TestTranscriptWhileWaiting(t *testing.T) {
	l := newSessionLog()
	for _, m := range loadLog(t, "approvals.jsonl") {
		l.apply(m)
		if m.Seq == 26 { // the session went requires_action
			break
		}
	}
	tr := l.snapshot()
	if len(tr.Waiting) != 1 || tr.Waiting[0].Command != "echo hello" || tr.Waiting[0].Kind != "approval" {
		t.Fatalf("waiting: %s", js(tr.Waiting))
	}
	if tr.Turns[0].Status != "running" || tr.Turns[0].Items[0].Status != "in_progress" {
		t.Fatalf("turn: %s", js(tr.Turns[0]))
	}
}

func TestTranscriptStreamsText(t *testing.T) {
	l := newSessionLog()
	now := time.Now()
	ev := func(seq int64, e canopy.Event, item string) canopy.StreamMessage {
		return canopy.StreamMessage{Seq: seq, Type: canopy.RecordTypeEvent, Ts: now,
			Record: canopy.Record{Seq: seq, Type: canopy.RecordTypeEvent, TurnId: "turn_1", ItemId: item, Event: e}}
	}
	l.apply(canopy.StreamMessage{Seq: 1, Type: canopy.RecordTypeTurnQueued, Record: canopy.Record{TurnId: "turn_1"}})
	l.apply(ev(2, canopy.Event{Type: canopy.EventTypeTurnStarted}, ""))
	l.apply(ev(3, canopy.Event{Type: canopy.EventTypeItemStarted, Item: canopy.Item{Id: "h1", Kind: canopy.ItemKindAssistantMessage, Status: canopy.ItemStatusInProgress}}, "item_a"))
	l.apply(ev(4, canopy.Event{Type: canopy.EventTypeItemDelta, Delta: canopy.Delta{ItemId: "h1", Field: canopy.DeltaFieldText, Text: "Hel"}}, "item_a"))
	l.apply(ev(5, canopy.Event{Type: canopy.EventTypeItemDelta, Delta: canopy.Delta{ItemId: "h1", Field: canopy.DeltaFieldText, Text: "lo"}}, "item_a"))
	tr := l.snapshot()
	if got := tr.Turns[0].Items[0]; got.Text != "Hello" || got.Status != "in_progress" {
		t.Fatalf("streamed: %s", js(got))
	}
	// A turn that ends leaves nothing looking like it's still running.
	l.apply(ev(6, canopy.Event{Type: canopy.EventTypeTurnCancelled}, ""))
	if got := l.snapshot().Turns[0]; got.Status != "cancelled" || got.Items[0].Status != "incomplete" {
		t.Fatalf("cancelled: %s", js(got))
	}
}

func TestTranscriptKeepsTheLatestTurns(t *testing.T) {
	l := newSessionLog()
	var seq int64
	for i := 0; i < maxTurns+5; i++ {
		seq++
		l.apply(canopy.StreamMessage{Seq: seq, Type: canopy.RecordTypeUserInput, Record: canopy.Record{Input: canopy.Input{Type: canopy.InputTypeMessage,
			Content: []canopy.ContentPart{{Type: canopy.ContentPartTypeText, Text: fmt.Sprint("message ", i)}}}}})
		seq++
		l.apply(canopy.StreamMessage{Seq: seq, Type: canopy.RecordTypeTurnQueued, Record: canopy.Record{TurnId: fmt.Sprint("turn_", i)}})
	}
	tr := l.snapshot()
	if len(tr.Turns) != maxTurns || tr.Earlier != 5 || tr.Turns[0].Input != "message 5" || len(l.byID) != maxTurns {
		t.Fatalf("turns %d, earlier %d, first %q, index %d", len(tr.Turns), tr.Earlier, tr.Turns[0].Input, len(l.byID))
	}
}

func TestClipKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("é", 10)
	for n := 1; n < len(s); n++ {
		for _, got := range []string{clip(s, n), clipTail(s, n)} {
			if !json.Valid([]byte(fmt.Sprintf("%q", got))) || strings.ContainsRune(got, '�') {
				t.Fatalf("n=%d: %q", n, got)
			}
		}
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
