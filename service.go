package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

type Service interface {
	Close() error
	Run(ctx context.Context, req Request) (*Result, error)
}

type ServiceMiddleware func(Service) Service

func NewService(cfg Config) (Service, error) {
	log := zap.L().With(
		zap.String("service", "claude-runner"),
	)

	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}

	return &service{
		cfg: cfg,
		log: log,
	}, nil
}

type service struct {
	cfg Config
	log *zap.Logger
}

func (svc *service) Close() error {
	return nil
}

func (svc *service) Run(ctx context.Context, req Request) (*Result, error) {
	if req.Prompt == "" {
		return nil, ErrInvalidPrompt
	}

	id := ulid.Make().String()

	workDir, err := svc.prepareWorkDir(ctx, req, id)
	if err != nil {
		return nil, err
	}

	args := svc.buildArgs(req)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := &Result{ID: id}

	if err := cmd.Run(); err != nil {
		result.Output = stdout.String()
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}

		return result, nil
	}

	result.Output = stdout.String()

	return result, nil
}

func (svc *service) buildArgs(req Request) []string {
	args := []string{"-p", req.Prompt}

	if len(svc.cfg.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(svc.cfg.AllowedTools, ","))
	}

	if svc.cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", svc.cfg.MaxTurns))
	}

	return args
}

func (svc *service) prepareWorkDir(ctx context.Context, req Request, id string) (string, error) {
	if req.WorkDir != "" {
		return req.WorkDir, nil
	}

	if req.Repo == "" {
		return svc.cfg.WorkDir, nil
	}

	workDir := filepath.Join(svc.cfg.WorkDir, id)

	args := []string{"clone", "--depth", "1"}
	if req.Ref != "" {
		args = append(args, "--branch", req.Ref)
	}

	args = append(args, req.Repo, workDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return workDir, nil
}
