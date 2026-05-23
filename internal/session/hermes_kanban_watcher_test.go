package session

import (
	"testing"
	"time"
)

// TestKanbanWatcher_CountsStartAtZero verifies that a newly created watcher
// reports zero for both running and blocked counts before any events are applied.
func TestKanbanWatcher_CountsStartAtZero(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0") // unreachable; no Start()
	running, blocked := w.Counts()
	if running != 0 {
		t.Errorf("running = %d, want 0", running)
	}
	if blocked != 0 {
		t.Errorf("blocked = %d, want 0", blocked)
	}
}

// TestKanbanWatcher_StopIsIdempotent verifies that calling Stop() multiple times
// does not panic or deadlock.
func TestKanbanWatcher_StopIsIdempotent(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.Stop()
	w.Stop() // second call must not panic
	w.Stop() // third call must not panic
}

// TestKanbanWatcher_SubscribeNotifies verifies that a subscriber channel
// receives a notification when a count-changing event is applied.
func TestKanbanWatcher_SubscribeNotifies(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	ch := w.Subscribe()

	// Apply a "claimed" event which increments running count.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "task-1"})

	select {
	case <-ch:
		// good
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber notification after claimed event")
	}

	// Verify count changed.
	running, _ := w.Counts()
	if running != 1 {
		t.Errorf("running = %d, want 1 after claimed event", running)
	}
}

// TestKanbanWatcher_SubscribeNoNotifyOnNoChange verifies that applying an event
// that does not change counts (e.g. unblocked when blocked=0) does not notify.
func TestKanbanWatcher_SubscribeNoNotifyOnNoChange(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	ch := w.Subscribe()

	// "unblocked" with blocked=0 should be a no-op and not notify.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "unblocked", TaskID: "task-1"})

	select {
	case <-ch:
		t.Error("received unexpected notification when counts did not change")
	case <-time.After(50 * time.Millisecond):
		// good — no spurious notification
	}
}

// TestKanbanWatcher_ApplyEventClaimed verifies running increments on "claimed".
func TestKanbanWatcher_ApplyEventClaimed(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	running, blocked := w.Counts()
	if running != 1 || blocked != 0 {
		t.Errorf("after claimed: running=%d blocked=%d, want 1 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventCompleted verifies running decrements on "completed".
func TestKanbanWatcher_ApplyEventCompleted(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "completed"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after claimed+completed: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventBlocked verifies blocked increments on "blocked".
func TestKanbanWatcher_ApplyEventBlocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 1 {
		t.Errorf("after blocked: running=%d blocked=%d, want 0 1", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventUnblocked verifies blocked decrements on "unblocked".
func TestKanbanWatcher_ApplyEventUnblocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "unblocked"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after blocked+unblocked: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventCrashed verifies running decrements on "crashed".
func TestKanbanWatcher_ApplyEventCrashed(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "crashed"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after claimed+crashed: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_NeverNegative verifies counts never go below zero.
func TestKanbanWatcher_NeverNegative(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	// Apply events that would underflow if not guarded.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "completed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "crashed"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "unblocked"})
	running, blocked := w.Counts()
	if running < 0 || blocked < 0 {
		t.Errorf("counts went negative: running=%d blocked=%d", running, blocked)
	}
}

// TestKanbanWatcher_BuildWSURL verifies URL conversion from HTTP to WebSocket.
func TestKanbanWatcher_BuildWSURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080/api/plugins/kanban/events"},
		{"https://example.com", "wss://example.com/api/plugins/kanban/events"},
		{"ws://127.0.0.1:9000", "ws://127.0.0.1:9000/api/plugins/kanban/events"},
		{"wss://example.com", "wss://example.com/api/plugins/kanban/events"},
		{"http://127.0.0.1:8080/", "ws://127.0.0.1:8080/api/plugins/kanban/events"},
	}
	for _, tt := range tests {
		w := NewKanbanWatcher(tt.input)
		got := w.buildWSURL()
		if got != tt.want {
			t.Errorf("buildWSURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestKanbanWatcher_BuildHTTPURL verifies URL conversion from WS to HTTP.
func TestKanbanWatcher_BuildHTTPURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"https://example.com", "https://example.com"},
		{"ws://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"wss://example.com", "https://example.com"},
	}
	for _, tt := range tests {
		w := NewKanbanWatcher(tt.input)
		got := w.buildHTTPURL()
		if got != tt.want {
			t.Errorf("buildHTTPURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
