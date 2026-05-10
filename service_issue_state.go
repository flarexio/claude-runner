package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	stateDirName    = ".claude-runner"
	attemptsDirName = "attempts"
	stateFileName   = "state.json"
	lockFileName    = "lock.json"

	staleLockTTL = time.Hour

	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"

	errorTypeCloneFailed  = "clone_failed"
	errorTypeClaudeFailed = "claude_failed"
	errorTypeExecError    = "exec_error"
)

type taskState struct {
	TaskID        string    `json:"task_id"`
	Repo          string    `json:"repo"`
	IssueNumber   int       `json:"issue_number"`
	LastAttemptID string    `json:"last_attempt_id"`
	LastStatus    string    `json:"last_status"`
	LastErrorType string    `json:"last_error_type,omitempty"`
	LastCommit    string    `json:"last_commit,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type attemptRecord struct {
	AttemptID   string    `json:"attempt_id"`
	TaskID      string    `json:"task_id"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitzero"`
	Status      string    `json:"status"`
	ErrorType   string    `json:"error_type,omitempty"`
	ErrorDetail string    `json:"error_detail,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Commit      string    `json:"commit,omitempty"`
	Model       string    `json:"model,omitempty"`
	ModelLabel  string    `json:"model_label,omitempty"`
}

type workspaceLock struct {
	AttemptID string    `json:"attempt_id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func acquireWorkspaceLock(stateDir, attemptID string, log *zap.Logger) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(stateDir, lockFileName)

	body, err := json.MarshalIndent(workspaceLock{
		AttemptID: attemptID,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}

	if err := writeLockExclusive(lockPath, body); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	existing, readErr := readWorkspaceLock(lockPath)
	switch {
	case readErr != nil:
		log.Warn("corrupt lock, taking over",
			zap.String("lock", lockPath),
			zap.Error(readErr))
	case time.Since(existing.StartedAt) < staleLockTTL:
		return fmt.Errorf("%w: held by attempt %s (pid %d, started %s)",
			ErrIssueWorkspaceBusy, existing.AttemptID, existing.PID,
			existing.StartedAt.Format(time.RFC3339))
	default:
		log.Warn("stale lock, taking over",
			zap.String("lock", lockPath),
			zap.String("previous_attempt_id", existing.AttemptID),
			zap.Int("previous_pid", existing.PID),
			zap.Time("previous_started_at", existing.StartedAt))
	}

	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeLockExclusive(lockPath, body)
}

func writeLockExclusive(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(body)
	return err
}

func readWorkspaceLock(path string) (*workspaceLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock workspaceLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	return &lock, nil
}

func releaseWorkspaceLock(stateDir string, log *zap.Logger) {
	lockPath := filepath.Join(stateDir, lockFileName)
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("release workspace lock", zap.String("lock", lockPath), zap.Error(err))
	}
}

func writeTaskState(stateDir string, state *taskState) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(stateDir, stateFileName), state)
}

func writeAttempt(stateDir string, attempt *attemptRecord) error {
	dir := filepath.Join(stateDir, attemptsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, attempt.AttemptID+".json"), attempt)
}

func writeJSONAtomic(path string, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func gitHeadCommit(ctx context.Context, workDir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
