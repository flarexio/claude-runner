package runner

import "errors"

var (
	ErrTaskNotFound  = errors.New("task not found")
	ErrInvalidPrompt = errors.New("invalid prompt")
	ErrExecFailed    = errors.New("claude execution failed")
)

type Config struct {
	WorkDir      string   `yaml:"workDir"`
	AllowedTools []string `yaml:"allowedTools"`
	MaxTurns     int      `yaml:"maxTurns"`
}
