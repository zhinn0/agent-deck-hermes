package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var kanbanLog = logging.ForComponent("hermes-kanban")

const (
	kanbanInitialBackoff = 1 * time.Second
	kanbanMaxBackoff     = 30 * time.Second
	kanbanBackoffFactor  = 2
)

// kanbanEvent is the minimal shape of a Hermes Kanban WebSocket event.
type kanbanEvent struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	TaskID string `json:"task_id"`
}

// kanbanBoardResponse is used to seed initial counts from the HTTP board endpoint.
type kanbanBoardResponse struct {
	Tasks []kanbanTask `json:"tasks"`
}

type kanbanTask struct {
	Status string `json:"status"`
}

// KanbanWatcher maintains a live count of running/blocked Kanban tasks
// by streaming events from the Hermes gateway WebSocket endpoint.
// Falls back gracefully when the gateway is unreachable.
type KanbanWatcher struct {
	gatewayURL string

	mu      sync.RWMutex
	running int
	blocked int

	lastEventID int64

	stopCh   chan struct{}
	stopOnce sync.Once
	subsMu   sync.Mutex
	subs     []chan struct{}
}

// NewKanbanWatcher creates a new KanbanWatcher for the given gateway URL.
// The URL should be the HTTP/WS base URL of the Hermes gateway
// (e.g. "http://127.0.0.1:8080" or "ws://127.0.0.1:8080").
func NewKanbanWatcher(gatewayURL string) *KanbanWatcher {
	return &KanbanWatcher{
		gatewayURL: gatewayURL,
		stopCh:     make(chan struct{}),
	}
}

// Start runs the reconnect loop in a goroutine. Safe to call once.
func (w *KanbanWatcher) Start() {
	go w.reconnectLoop()
}

// Stop signals the watcher to stop. Idempotent.
func (w *KanbanWatcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
	})
}

// Counts returns the current running and blocked task counts.
// Always instant — reads from in-memory state protected by RWMutex.
func (w *KanbanWatcher) Counts() (running, blocked int) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running, w.blocked
}

// Subscribe returns a channel that receives an empty struct whenever the
// running or blocked count changes. The channel is buffered (capacity 1);
// slow consumers miss coalesced updates but never block the watcher.
func (w *KanbanWatcher) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	w.subsMu.Lock()
	w.subs = append(w.subs, ch)
	w.subsMu.Unlock()
	return ch
}

// notify sends to all subscriber channels (non-blocking).
func (w *KanbanWatcher) notify() {
	w.subsMu.Lock()
	defer w.subsMu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// setCountsAndNotify updates counts and notifies subscribers if changed.
func (w *KanbanWatcher) setCountsAndNotify(running, blocked int) {
	w.mu.Lock()
	changed := w.running != running || w.blocked != blocked
	w.running = running
	w.blocked = blocked
	w.mu.Unlock()
	if changed {
		w.notify()
	}
}

// reconnectLoop dials WebSocket, reads events, and reconnects on disconnect.
func (w *KanbanWatcher) reconnectLoop() {
	backoff := kanbanInitialBackoff
	for {
		select {
		case <-w.stopCh:
			return
		default:
		}

		err := w.runSession()
		if err != nil {
			kanbanLog.Debug("kanban_watcher_disconnected",
				slog.String("error", err.Error()),
				slog.Duration("backoff", backoff),
			)
		}

		// Exponential backoff before retry
		select {
		case <-w.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= kanbanBackoffFactor
		if backoff > kanbanMaxBackoff {
			backoff = kanbanMaxBackoff
		}
	}
}

// runSession connects, seeds counts, reads events, returns on disconnect or error.
func (w *KanbanWatcher) runSession() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stop context when watcher is stopped
	go func() {
		select {
		case <-w.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Seed initial counts via HTTP board endpoint
	running, blocked, err := w.seedCounts(ctx)
	if err != nil {
		return fmt.Errorf("seed counts: %w", err)
	}
	w.setCountsAndNotify(running, blocked)

	// Build WebSocket URL
	wsURL := w.buildWSURL()

	// Retrieve last event ID under lock for the query param
	w.mu.RLock()
	lastID := w.lastEventID
	w.mu.RUnlock()

	if lastID > 0 {
		wsURL = fmt.Sprintf("%s?since=%d", wsURL, lastID)
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Reset backoff on success
	kanbanLog.Debug("kanban_watcher_connected", slog.String("url", wsURL))

	readDone := make(chan error, 1)
	go func() {
		readDone <- w.readEvents(conn)
	}()

	select {
	case <-w.stopCh:
		_ = conn.Close()
		return nil
	case err := <-readDone:
		return err
	}
}

// seedCounts fetches the current board state via HTTP and returns running/blocked counts.
func (w *KanbanWatcher) seedCounts(ctx context.Context) (running, blocked int, err error) {
	boardURL := w.buildHTTPURL() + "/api/plugins/kanban/board?include_archived=false"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, boardURL, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("http get board: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("board endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return 0, 0, fmt.Errorf("read body: %w", err)
	}

	var board kanbanBoardResponse
	if err := json.Unmarshal(body, &board); err != nil {
		return 0, 0, fmt.Errorf("unmarshal board: %w", err)
	}

	for _, t := range board.Tasks {
		switch t.Status {
		case "running", "claimed":
			running++
		case "blocked":
			blocked++
		}
	}
	return running, blocked, nil
}

// readEvents reads WebSocket messages and updates counts.
func (w *KanbanWatcher) readEvents(conn *websocket.Conn) error {
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}

		var evt kanbanEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			kanbanLog.Debug("kanban_event_unmarshal_failed",
				slog.String("error", err.Error()),
				slog.String("raw", string(msg)),
			)
			continue
		}

		w.applyEvent(evt)
	}
}

// applyEvent updates in-memory counts based on the event kind.
func (w *KanbanWatcher) applyEvent(evt kanbanEvent) {
	w.mu.Lock()
	running := w.running
	blocked := w.blocked

	switch evt.Kind {
	case "claimed":
		running++
	case "completed", "archived":
		if running > 0 {
			running--
		}
	case "blocked":
		blocked++
	case "unblocked":
		if blocked > 0 {
			blocked--
		}
	case "reclaimed", "crashed", "timed_out":
		if running > 0 {
			running--
		}
	}

	changed := w.running != running || w.blocked != blocked
	w.running = running
	w.blocked = blocked
	if evt.ID > w.lastEventID {
		w.lastEventID = evt.ID
	}
	w.mu.Unlock()

	if changed {
		w.notify()
	}
}

// buildWSURL converts the gateway base URL to a WebSocket events endpoint.
// Handles http://, https://, ws://, wss:// prefixes.
func (w *KanbanWatcher) buildWSURL() string {
	base := w.gatewayURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	base = strings.TrimRight(base, "/")
	return base + "/api/plugins/kanban/events"
}

// buildHTTPURL converts the gateway base URL to an HTTP URL.
func (w *KanbanWatcher) buildHTTPURL() string {
	base := w.gatewayURL
	switch {
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	}
	return strings.TrimRight(base, "/")
}
