package runner

import (
	"fmt"
	"strings"
)

// NormalizeRepo extracts the canonical "owner/repo" slug from common repo
// reference forms:
//   - owner/repo
//   - https://github.com/owner/repo
//   - https://github.com/owner/repo.git
//   - git@github.com:owner/repo.git
//   - ssh://git@github.com/owner/repo.git
func NormalizeRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", ErrInvalidRepo
	}

	s := repo
	switch {
	case strings.HasPrefix(s, "git@"):
		if idx := strings.Index(s, ":"); idx >= 0 {
			s = s[idx+1:]
		}
	case strings.Contains(s, "://"):
		if idx := strings.Index(s, "://"); idx >= 0 {
			s = s[idx+3:]
		}
		if idx := strings.Index(s, "/"); idx >= 0 {
			s = s[idx+1:]
		}
	}

	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")

	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}

	owner := parts[len(parts)-2]
	name := parts[len(parts)-1]
	if owner == "" || name == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}

	return owner + "/" + name, nil
}
