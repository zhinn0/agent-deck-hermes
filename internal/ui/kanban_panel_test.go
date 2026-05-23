package ui

import (
	"fmt"
	"strings"
	"testing"
)

// TestKanbanPanel_NilSafe verifies that nil-receiver methods don't panic.
func TestKanbanPanel_NilSafe(t *testing.T) {
	var p *KanbanPanel
	if p.IsVisible() {
		t.Error("nil panel should not be visible")
	}
	// None of these should panic
	p.Show()
	p.Hide()
	p.SetSize(120, 40)
	p.SetTasks(nil, "")
	p.MoveUp()
	p.MoveDown()
	p.SwitchColumn()
	if p.Toggle() {
		t.Error("nil panel toggle should return false")
	}
	if p.View() != "" {
		t.Error("nil panel View should return empty string")
	}
	if p.SelectedTask() != nil {
		t.Error("nil panel SelectedTask should return nil")
	}
}

// TestKanbanPanel_ToggleVisibility verifies that Toggle flips IsVisible.
func TestKanbanPanel_ToggleVisibility(t *testing.T) {
	p := NewKanbanPanel()
	if p.IsVisible() {
		t.Error("new panel should not be visible")
	}
	if !p.Toggle() {
		t.Error("first Toggle should return true (now visible)")
	}
	if !p.IsVisible() {
		t.Error("panel should be visible after first toggle")
	}
	if p.Toggle() {
		t.Error("second Toggle should return false (now hidden)")
	}
	if p.IsVisible() {
		t.Error("panel should not be visible after second toggle")
	}
}

// TestKanbanPanel_ShowSetsLoading verifies that Show marks the panel as loading.
func TestKanbanPanel_ShowSetsLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	if !p.loading {
		t.Error("Show should set loading=true")
	}
	if p.fetchErr != "" {
		t.Error("Show should clear fetchErr")
	}
}

// TestKanbanPanel_SetTasksClearsLoading verifies that SetTasks marks loading done.
func TestKanbanPanel_SetTasksClearsLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	p.SetTasks([]KanbanTask{{ID: "T1", Title: "Fix login", Status: "running"}}, "")
	if p.loading {
		t.Error("SetTasks should clear loading flag")
	}
	if len(p.tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(p.tasks))
	}
}

// TestKanbanPanel_SetTasksWithError verifies that error string is stored.
func TestKanbanPanel_SetTasksWithError(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	p.SetTasks(nil, "hermes not found")
	if p.fetchErr != "hermes not found" {
		t.Errorf("fetchErr = %q, want %q", p.fetchErr, "hermes not found")
	}
}

// TestKanbanPanel_ViewHidden returns empty when not visible.
func TestKanbanPanel_ViewHidden(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(120, 40)
	if got := p.View(); got != "" {
		t.Errorf("hidden panel View() = %q, want empty", got)
	}
}

// TestKanbanPanel_ViewVisible returns non-empty string when visible.
func TestKanbanPanel_ViewVisible(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(120, 40)
	p.Show()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running task", Status: "running"},
		{ID: "T2", Title: "Blocked task", Status: "blocked", BlockReason: "waiting for API key"},
	}, "")
	view := p.View()
	if view == "" {
		t.Error("visible panel View() should return non-empty string")
	}
	if !strings.Contains(view, "RUNNING") {
		t.Error("View should contain RUNNING header")
	}
	if !strings.Contains(view, "BLOCKED") {
		t.Error("View should contain BLOCKED header")
	}
}

// TestKanbanPanel_ViewShowsErrorWhenSet verifies error text appears in view.
func TestKanbanPanel_ViewShowsErrorWhenSet(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(80, 30)
	p.Show()
	p.SetTasks(nil, "hermes not found")
	view := p.View()
	if !strings.Contains(view, "hermes not found") {
		t.Errorf("View should show error text; got: %s", view)
	}
}

// TestKanbanPanel_ViewShowsLoadingWhenLoading verifies loading state shows in view.
func TestKanbanPanel_ViewShowsLoadingWhenLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(80, 30)
	p.Show() // sets loading=true
	view := p.View()
	if !strings.Contains(view, "loading") {
		t.Errorf("View should show 'loading' when loading=true; got: %s", view)
	}
}

// TestTruncate verifies string truncation.
func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		maxW int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello w…"},
		{"hi", 3, "hi"},
		{"hello", 0, "hello"},   // maxW ≤ 3 → passthrough
		{"hello", 3, "hello"},   // maxW == 3 → passthrough
		{"hello", 4, "hel…"},
	}
	for _, tt := range tests {
		got := truncate(tt.s, tt.maxW)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxW, got, tt.want)
		}
	}
}

// TestPad verifies right-padding.
func TestPad(t *testing.T) {
	tests := []struct {
		s     string
		width int
		want  string
	}{
		{"hi", 5, "hi   "},
		{"hello", 5, "hello"},
		{"toolong", 4, "toolong"}, // longer than width — no truncation
		{"", 3, "   "},
	}
	for _, tt := range tests {
		got := pad(tt.s, tt.width)
		if got != tt.want {
			t.Errorf("pad(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
		}
	}
}

// TestIsHermesNotFound verifies error classification.
func TestIsHermesNotFound(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"exec: \"hermes\": executable file not found in $PATH", true},
		{"fork/exec /usr/bin/hermes: no such file or directory", true},
		{"hermes: not found", true},
		{"hermes: exit status 1", false},
		{"connection refused", false},
		{"", false},
	}
	for _, tt := range tests {
		var err error
		if tt.msg != "" {
			err = fmt.Errorf("%s", tt.msg)
		}
		got := isHermesNotFound(err)
		if got != tt.want {
			t.Errorf("isHermesNotFound(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

// TestKanbanPanel_MoveDown_Clamps verifies MoveDown does not exceed the last row.
func TestKanbanPanel_MoveDown_Clamps(t *testing.T) {
	p := NewKanbanPanel()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Task 1", Status: "running"},
		{ID: "T2", Title: "Task 2", Status: "running"},
	}, "")
	for i := 0; i < 5; i++ {
		p.MoveDown()
	}
	if p.selectedRow != 1 {
		t.Errorf("selectedRow = %d after clamped MoveDown, want 1", p.selectedRow)
	}
}

// TestKanbanPanel_MoveUp_Clamps verifies MoveUp does not go below row 0.
func TestKanbanPanel_MoveUp_Clamps(t *testing.T) {
	p := NewKanbanPanel()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Task 1", Status: "running"},
	}, "")
	p.MoveUp()
	if p.selectedRow != 0 {
		t.Errorf("selectedRow = %d after MoveUp at row 0, want 0", p.selectedRow)
	}
}

// TestKanbanPanel_SwitchColumn verifies SwitchColumn toggles between 0 and 1.
func TestKanbanPanel_SwitchColumn(t *testing.T) {
	p := NewKanbanPanel()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running", Status: "running"},
		{ID: "T2", Title: "Blocked", Status: "blocked"},
	}, "")
	if p.selectedCol != 0 {
		t.Fatalf("initial selectedCol = %d, want 0", p.selectedCol)
	}
	p.SwitchColumn()
	if p.selectedCol != 1 {
		t.Errorf("after SwitchColumn selectedCol = %d, want 1", p.selectedCol)
	}
	p.SwitchColumn()
	if p.selectedCol != 0 {
		t.Errorf("after second SwitchColumn selectedCol = %d, want 0", p.selectedCol)
	}
}

// TestKanbanPanel_SwitchColumn_EmptyTarget verifies switching to an empty column
// keeps selectedRow=0 and SelectedTask returns nil.
func TestKanbanPanel_SwitchColumn_EmptyTarget(t *testing.T) {
	p := NewKanbanPanel()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running", Status: "running"},
	}, "")
	if p.selectedCol != 0 {
		t.Fatalf("initial selectedCol = %d, want 0", p.selectedCol)
	}
	p.SwitchColumn()
	if p.selectedCol != 1 {
		t.Errorf("selectedCol = %d, want 1", p.selectedCol)
	}
	if p.selectedRow != 0 {
		t.Errorf("selectedRow = %d, want 0", p.selectedRow)
	}
	if p.SelectedTask() != nil {
		t.Error("SelectedTask() should be nil for empty blocked column")
	}
}

// TestKanbanPanel_SelectedTask verifies the correct task is returned.
func TestKanbanPanel_SelectedTask(t *testing.T) {
	p := NewKanbanPanel()
	p.SetTasks([]KanbanTask{
		{ID: "R1", Title: "Running 1", Status: "running"},
		{ID: "R2", Title: "Running 2", Status: "running"},
		{ID: "B1", Title: "Blocked 1", Status: "blocked"},
	}, "")
	p.selectedCol = 0
	p.selectedRow = 1
	task := p.SelectedTask()
	if task == nil {
		t.Fatal("SelectedTask() returned nil, want second running task")
	}
	if task.ID != "R2" {
		t.Errorf("SelectedTask().ID = %q, want %q", task.ID, "R2")
	}
}

// TestKanbanPanel_SetTasks_ClampsRow verifies selectedRow is clamped after SetTasks.
func TestKanbanPanel_SetTasks_ClampsRow(t *testing.T) {
	p := NewKanbanPanel()
	p.selectedRow = 5
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Task 1", Status: "running"},
		{ID: "T2", Title: "Task 2", Status: "running"},
	}, "")
	if p.selectedRow != 1 {
		t.Errorf("selectedRow = %d after SetTasks clamp, want 1", p.selectedRow)
	}
}

// TestKanbanPanel_SetTasks_SwitchesColIfEmpty verifies that when the current
// column becomes empty and the other has tasks, selectedCol switches.
func TestKanbanPanel_SetTasks_SwitchesColIfEmpty(t *testing.T) {
	p := NewKanbanPanel()
	p.selectedCol = 1 // start on blocked column
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running 1", Status: "running"},
		{ID: "T2", Title: "Running 2", Status: "running"},
	}, "")
	if p.selectedCol != 0 {
		t.Errorf("selectedCol = %d, want 0 (switched from empty blocked to running)", p.selectedCol)
	}
}

// TestKanbanPanel_SelectedTask_NilWhenEmpty verifies SelectedTask returns nil for
// an empty panel and also for a nil receiver.
func TestKanbanPanel_SelectedTask_NilWhenEmpty(t *testing.T) {
	p := NewKanbanPanel()
	if p.SelectedTask() != nil {
		t.Error("SelectedTask() should be nil when no tasks")
	}
	var nilP *KanbanPanel
	if nilP.SelectedTask() != nil {
		t.Error("SelectedTask() on nil receiver should return nil")
	}
}

// TestKanbanPanel_ViewHighlightsSelected verifies that the selection indicator
// appears in the View output when a task is selected.
func TestKanbanPanel_ViewHighlightsSelected(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(80, 30)
	p.Show()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running task", Status: "running"},
		{ID: "T2", Title: "Blocked task", Status: "blocked"},
	}, "")
	view := p.View()
	if !strings.Contains(view, "▶") {
		t.Error("View() should contain ▶ selection indicator")
	}
}
