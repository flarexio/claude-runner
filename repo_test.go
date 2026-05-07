package runner

import (
	"errors"
	"testing"
)

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"slug", "owner/repo", "owner/repo"},
		{"https", "https://github.com/owner/repo", "owner/repo"},
		{"https-git-suffix", "https://github.com/owner/repo.git", "owner/repo"},
		{"https-trailing-slash", "https://github.com/owner/repo/", "owner/repo"},
		{"ssh", "git@github.com:owner/repo.git", "owner/repo"},
		{"ssh-protocol", "ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"with-spaces", "  owner/repo  ", "owner/repo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRepo(tc.in)
			if err != nil {
				t.Fatalf("NormalizeRepo(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeRepoRejectsInvalid(t *testing.T) {
	cases := []string{"", "   ", "owner", "/repo", "owner/"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := NormalizeRepo(in); !errors.Is(err, ErrInvalidRepo) {
				t.Fatalf("NormalizeRepo(%q) err = %v, want ErrInvalidRepo", in, err)
			}
		})
	}
}
