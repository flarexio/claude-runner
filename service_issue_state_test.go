package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestAcquireWorkspaceLockFresh(t *testing.T) {
	stateDir := t.TempDir()

	if err := acquireWorkspaceLock(stateDir, "01ATTEMPT", zap.NewNop()); err != nil {
		t.Fatalf("acquireWorkspaceLock() error = %v", err)
	}

	lock, err := readWorkspaceLock(filepath.Join(stateDir, lockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	if lock.AttemptID != "01ATTEMPT" {
		t.Fatalf("attempt_id = %q, want 01ATTEMPT", lock.AttemptID)
	}
	if lock.PID != os.Getpid() {
		t.Fatalf("pid = %d, want self %d", lock.PID, os.Getpid())
	}
	if time.Since(lock.StartedAt) > time.Minute {
		t.Fatalf("started_at = %v, want recent", lock.StartedAt)
	}
}

func TestAcquireWorkspaceLockReturnsBusyOnFreshLock(t *testing.T) {
	stateDir := t.TempDir()

	writeLock(t, stateDir, workspaceLock{
		AttemptID: "01OTHER",
		PID:       999999,
		StartedAt: time.Now().UTC(),
	})

	err := acquireWorkspaceLock(stateDir, "01NEW", zap.NewNop())
	if !errors.Is(err, ErrIssueWorkspaceBusy) {
		t.Fatalf("err = %v, want ErrIssueWorkspaceBusy", err)
	}

	// Existing lock must not be overwritten.
	lock, _ := readWorkspaceLock(filepath.Join(stateDir, lockFileName))
	if lock.AttemptID != "01OTHER" {
		t.Fatalf("attempt_id = %q, want preserved 01OTHER", lock.AttemptID)
	}
}

func TestAcquireWorkspaceLockTakesOverStale(t *testing.T) {
	stateDir := t.TempDir()

	writeLock(t, stateDir, workspaceLock{
		AttemptID: "01STALE",
		PID:       999999,
		StartedAt: time.Now().UTC().Add(-2 * staleLockTTL),
	})

	if err := acquireWorkspaceLock(stateDir, "01NEW", zap.NewNop()); err != nil {
		t.Fatalf("acquireWorkspaceLock() error = %v, want take-over", err)
	}

	lock, _ := readWorkspaceLock(filepath.Join(stateDir, lockFileName))
	if lock.AttemptID != "01NEW" {
		t.Fatalf("attempt_id = %q, want 01NEW after take-over", lock.AttemptID)
	}
}

func TestAcquireWorkspaceLockTakesOverCorrupt(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, lockFileName), []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt lock: %v", err)
	}

	if err := acquireWorkspaceLock(stateDir, "01NEW", zap.NewNop()); err != nil {
		t.Fatalf("acquireWorkspaceLock() error = %v, want take-over of corrupt lock", err)
	}

	lock, err := readWorkspaceLock(filepath.Join(stateDir, lockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock after take-over: %v", err)
	}
	if lock.AttemptID != "01NEW" {
		t.Fatalf("attempt_id = %q, want 01NEW", lock.AttemptID)
	}
}

func TestReleaseWorkspaceLockRemovesFile(t *testing.T) {
	stateDir := t.TempDir()
	writeLock(t, stateDir, workspaceLock{AttemptID: "x", PID: 1, StartedAt: time.Now().UTC()})

	releaseWorkspaceLock(stateDir, zap.NewNop())

	if _, err := os.Stat(filepath.Join(stateDir, lockFileName)); !os.IsNotExist(err) {
		t.Fatalf("lock file still present, stat err = %v", err)
	}
}

func TestWriteJSONAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	first := taskState{TaskID: "t1", LastStatus: statusRunning}
	if err := writeJSONAtomic(path, &first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := taskState{TaskID: "t1", LastStatus: statusCompleted}
	if err := writeJSONAtomic(path, &second); err != nil {
		t.Fatalf("second write: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got taskState
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LastStatus != statusCompleted {
		t.Fatalf("last_status = %q, want %q", got.LastStatus, statusCompleted)
	}
}

func writeLock(t *testing.T, stateDir string, lock workspaceLock) {
	t.Helper()
	body, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, lockFileName), body, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}
