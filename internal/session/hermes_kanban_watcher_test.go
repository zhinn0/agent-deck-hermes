package session

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// makeTestDB creates a fresh SQLite file in a temp dir with the Hermes
// kanban.db schema. Returns the path and an open *sql.DB the caller can use
// to seed rows. The DB is closed automatically when the test ends.
func makeTestDB(t *testing.T) (path string, db *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "kanban.db")
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.Exec(`
        CREATE TABLE tasks (
            id TEXT PRIMARY KEY,
            title TEXT NOT NULL,
            status TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            workspace_kind TEXT NOT NULL DEFAULT 'scratch'
        );
        CREATE TABLE task_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            task_id TEXT NOT NULL,
            run_id INTEGER,
            kind TEXT NOT NULL,
            payload TEXT,
            created_at INTEGER NOT NULL
        );
    `)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return path, d
}

// insertTask inserts a single tasks row.
func insertTask(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO tasks (id, title, status, created_at) VALUES (?, ?, ?, ?)`,
		id, "task "+id, status, time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

// insertEvent inserts a task_events row and returns its rowid.
func insertEvent(t *testing.T, db *sql.DB, taskID, kind string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO task_events (task_id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		taskID, kind, "{}", time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// waitForCounts polls Counts() until both equal the desired values or the
// timeout elapses. Returns true on success.
func waitForCounts(w *KanbanWatcher, wantRunning, wantBlocked int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, b := w.Counts()
		if r == wantRunning && b == wantBlocked {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// waitForHealthy polls IsHealthy() until true or the timeout elapses.
func waitForHealthy(w *KanbanWatcher, want bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.IsHealthy() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// ----------------------------------------------------------------------------
// Construction / lifecycle
// ----------------------------------------------------------------------------

func TestKanbanWatcher_CountsStartAtZero(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestKanbanWatcher_IsHealthyFalseBeforeStart(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true before Start; want false")
	}
}

func TestKanbanWatcher_StopIsIdempotent(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "does-not-exist.db"))
	w.Stop()
	w.Stop()
	w.Stop()
}

func TestKanbanWatcher_StartIsIdempotent(t *testing.T) {
	path, _ := makeTestDB(t)
	w := NewKanbanWatcher(path)
	w.Start()
	w.Start()
	w.Start()
	defer w.Stop()
	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy")
	}
}

// ----------------------------------------------------------------------------
// applyEvent — state machine unit tests
// ----------------------------------------------------------------------------

func TestApplyEvent_ClaimedNewTask(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Fatalf("TaskStatus(T1) = %q, want running", got)
	}
}

func TestApplyEvent_ClaimedAlreadyRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "claimed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — no double increment", r, b)
	}
}

func TestApplyEvent_BlockedFromRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "blocked" {
		t.Fatalf("TaskStatus(T1) = %q, want blocked", got)
	}
}

func TestApplyEvent_BlockedUnseenTask(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1)", r, b)
	}
}

func TestApplyEvent_BlockedAlreadyBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 1 {
		t.Fatalf("counts = (%d,%d), want (0,1) — no double increment", r, b)
	}
}

func TestApplyEvent_UnblockedFromBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "unblocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Fatalf("TaskStatus(T1) = %q, want running", got)
	}
}

func TestApplyEvent_UnblockedFromRunningIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "unblocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — stale unblocked is no-op", r, b)
	}
}

func TestApplyEvent_UnblockedUnseenIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "unblocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0) — unseen unblocked is no-op", r, b)
	}
}

func TestApplyEvent_ReclaimedFromBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "reclaimed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
}

func TestApplyEvent_ReclaimedFromRunningIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "reclaimed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — already-running reclaim is no-op", r, b)
	}
}

func TestApplyEvent_ReclaimedUnseenIncrementsRunning(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "reclaimed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0)", r, b)
	}
}

func TestApplyEvent_TerminalKindsDecrementRunning(t *testing.T) {
	for _, kind := range []string{"completed", "archived", "crashed", "timed_out", "gave_up"} {
		t.Run(kind, func(t *testing.T) {
			w := NewKanbanWatcher("ignored")
			w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
			w.applyEvent(kanbanEvent{ID: 2, Kind: kind, TaskID: "T1"})
			r, b := w.Counts()
			if r != 0 || b != 0 {
				t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
			}
			if got := w.TaskStatus("T1"); got != "" {
				t.Fatalf("TaskStatus(T1) = %q, want empty", got)
			}
		})
	}
}

func TestApplyEvent_CompletedFromBlockedDecrementsBlocked(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "completed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestApplyEvent_CompletedUnseenIsNoop(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "completed", TaskID: "T1"})
	r, b := w.Counts()
	if r != 0 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (0,0)", r, b)
	}
}

func TestApplyEvent_DuplicateEventIgnored(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 5, Kind: "claimed", TaskID: "T1"})
	// Same id replayed — cursor already at 5, so this must be ignored even
	// though logically the kind would otherwise be a no-op anyway. Replay
	// with a count-changing kind to make the assertion sharp.
	w.applyEvent(kanbanEvent{ID: 5, Kind: "blocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — duplicate id must be ignored", r, b)
	}
}

func TestApplyEvent_LowerIDIgnored(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 10, Kind: "claimed", TaskID: "T1"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "blocked", TaskID: "T1"})
	r, b := w.Counts()
	if r != 1 || b != 0 {
		t.Fatalf("counts = (%d,%d), want (1,0) — out-of-order id below cursor must be ignored", r, b)
	}
}

func TestApplyEvent_AdvancesCursor(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	w.applyEvent(kanbanEvent{ID: 7, Kind: "claimed", TaskID: "T1"})
	w.mu.RLock()
	got := w.cursor
	w.mu.RUnlock()
	if got != 7 {
		t.Fatalf("cursor = %d, want 7", got)
	}
}

// ----------------------------------------------------------------------------
// Counts never go negative under pathological input
// ----------------------------------------------------------------------------

func TestApplyEvent_CountsNeverNegative(t *testing.T) {
	w := NewKanbanWatcher("ignored")

	// Terminate tasks that were never seen — must not push counters negative.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "completed", TaskID: "ghost-1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "archived", TaskID: "ghost-2"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "crashed", TaskID: "ghost-3"})
	w.applyEvent(kanbanEvent{ID: 4, Kind: "timed_out", TaskID: "ghost-4"})
	w.applyEvent(kanbanEvent{ID: 5, Kind: "gave_up", TaskID: "ghost-5"})
	w.applyEvent(kanbanEvent{ID: 6, Kind: "unblocked", TaskID: "ghost-6"})

	r, b := w.Counts()
	if r != 0 {
		t.Fatalf("running = %d, want 0", r)
	}
	if b != 0 {
		t.Fatalf("blocked = %d, want 0", b)
	}
}

// ----------------------------------------------------------------------------
// Subscribe / Unsubscribe behavior
// ----------------------------------------------------------------------------

func TestSubscribe_ReceivesNotificationOnChange(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()

	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})

	select {
	case <-ch:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("subscriber did not receive notification within 200ms")
	}
}

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()
	w.Unsubscribe(ch)

	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})

	select {
	case <-ch:
		t.Fatal("unsubscribed channel still received notification")
	default:
		// good
	}
}

func TestSubscribe_SecondSubscriberStillReceives(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	chA := w.Subscribe()
	chB := w.Subscribe()
	w.Unsubscribe(chA)

	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})

	select {
	case <-chB:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("remaining subscriber did not receive notification")
	}

	// chA should still not see anything.
	select {
	case <-chA:
		t.Fatal("unsubscribed channel A unexpectedly received notification")
	default:
	}
}

func TestSubscribe_NoNotifyWhenCountsUnchanged(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	// Pre-claim so the second claimed event is a no-op.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})

	ch := w.Subscribe()
	w.applyEvent(kanbanEvent{ID: 2, Kind: "claimed", TaskID: "T1"}) // no change

	select {
	case <-ch:
		t.Fatal("subscriber received notification despite no count change")
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestDroppedNotifications_IncrementsWhenSubscriberFull(t *testing.T) {
	w := NewKanbanWatcher("ignored")
	ch := w.Subscribe()

	// First event fills the buffer (cap 1).
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "T1"})
	// Second count-changing event finds the channel already full → dropped.
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "T1"})
	// Third count-changing event also dropped (we never drained).
	w.applyEvent(kanbanEvent{ID: 3, Kind: "unblocked", TaskID: "T1"})

	if got := w.DroppedNotifications(); got < 2 {
		t.Fatalf("DroppedNotifications() = %d, want >= 2", got)
	}
	// Drain so the test goroutine doesn't leak references; nothing more to assert.
	select {
	case <-ch:
	default:
	}
}

// ----------------------------------------------------------------------------
// Integration test: real SQLite file driven by NewKanbanWatcher.Start()
// ----------------------------------------------------------------------------

func TestKanbanWatcher_Integration_SeedsAndTailsEvents(t *testing.T) {
	path, db := makeTestDB(t)

	// Seed: T1 running, T2 claimed (also counts as running), T3 blocked, T4 done (ignored).
	insertTask(t, db, "T1", "running")
	insertTask(t, db, "T2", "claimed")
	insertTask(t, db, "T3", "blocked")
	insertTask(t, db, "T4", "done")

	w := NewKanbanWatcher(path)

	// Subscribe BEFORE any insert to avoid races; this channel should fire when
	// the new task_events row is observed.
	sub := w.Subscribe()

	w.Start()
	defer w.Stop()

	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy")
	}

	// Initial counts: T1 + T2 running, T3 blocked.
	if !waitForCounts(w, 2, 1, 2*time.Second) {
		r, b := w.Counts()
		t.Fatalf("initial counts = (%d,%d), want (2,1)", r, b)
	}
	if got := w.TaskStatus("T1"); got != "running" {
		t.Errorf("TaskStatus(T1) = %q, want running", got)
	}
	if got := w.TaskStatus("T3"); got != "blocked" {
		t.Errorf("TaskStatus(T3) = %q, want blocked", got)
	}

	// The seed itself might have notified once if counts changed from zero
	// (they did). Drain any pending signal so the next assertion is sharp.
	select {
	case <-sub:
	case <-time.After(50 * time.Millisecond):
	}

	// Append a "blocked" event for T1 (which is currently running).
	insertEvent(t, db, "T1", "blocked")

	// Expected: running 2→1, blocked 1→2.
	if !waitForCounts(w, 1, 2, 2*time.Second) {
		r, b := w.Counts()
		t.Fatalf("post-event counts = (%d,%d), want (1,2)", r, b)
	}

	// Subscriber should have been signaled.
	select {
	case <-sub:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received notification for new event")
	}

	if got := w.TaskStatus("T1"); got != "blocked" {
		t.Errorf("after event, TaskStatus(T1) = %q, want blocked", got)
	}
}

func TestKanbanWatcher_Integration_StopClosesSubscribers(t *testing.T) {
	path, _ := makeTestDB(t)

	w := NewKanbanWatcher(path)
	w.Start()
	if !waitForHealthy(w, true, 2*time.Second) {
		w.Stop()
		t.Fatal("watcher never became healthy")
	}
	sub := w.Subscribe()

	w.Stop()

	// Reading from a closed channel returns immediately with the zero value
	// and ok=false. We just need the read to unblock within a reasonable time.
	select {
	case _, ok := <-sub:
		if ok {
			// A pre-close notification is fine, but the channel must then close;
			// read once more to confirm.
			select {
			case _, ok2 := <-sub:
				if ok2 {
					t.Fatal("subscriber channel did not close after Stop()")
				}
			case <-time.After(time.Second):
				t.Fatal("subscriber channel did not close after Stop()")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel did not close after Stop()")
	}
}

// ----------------------------------------------------------------------------
// IsHealthy lifecycle
// ----------------------------------------------------------------------------

func TestKanbanWatcher_IsHealthy_FalseWhenDBMissing(t *testing.T) {
	// Point at a path that doesn't exist; seed will fail every time.
	path := filepath.Join(t.TempDir(), "missing.db")
	w := NewKanbanWatcher(path)
	w.Start()
	defer w.Stop()

	// Give the poll loop a moment to attempt and fail to seed.
	time.Sleep(300 * time.Millisecond)
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true when db file is missing; want false")
	}
}

func TestKanbanWatcher_IsHealthy_TrueAfterSeed(t *testing.T) {
	path, _ := makeTestDB(t)
	w := NewKanbanWatcher(path)
	w.Start()
	defer w.Stop()

	if !waitForHealthy(w, true, 2*time.Second) {
		t.Fatal("watcher never became healthy with a valid db")
	}
}

func TestKanbanWatcher_Seed_DirectCall(t *testing.T) {
	path, db := makeTestDB(t)
	insertTask(t, db, "T1", "running")
	insertTask(t, db, "T2", "blocked")
	// Pre-existing event so the cursor advances at seed.
	cursorID := insertEvent(t, db, "T1", "claimed")

	w := NewKanbanWatcher(path)
	if err := w.seed(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r, b := w.Counts()
	if r != 1 || b != 1 {
		t.Fatalf("counts after seed = (%d,%d), want (1,1)", r, b)
	}
	if !w.IsHealthy() {
		t.Fatal("IsHealthy() = false after successful seed; want true")
	}

	// Cursor should equal the max task_events.id observed.
	w.mu.RLock()
	gotCursor := w.cursor
	w.mu.RUnlock()
	if gotCursor != cursorID {
		t.Fatalf("cursor after seed = %d, want %d", gotCursor, cursorID)
	}
}

func TestKanbanWatcher_Seed_FailsWhenFileMissing(t *testing.T) {
	w := NewKanbanWatcher(filepath.Join(t.TempDir(), "nope.db"))
	if err := w.seed(); err == nil {
		t.Fatal("seed() returned nil error for missing file; want error")
	}
	if w.IsHealthy() {
		t.Fatal("IsHealthy() = true after failed seed; want false")
	}
}
