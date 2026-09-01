// Package git provides git operations for helm-semver.
package git

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// CommitInfo holds data about a single commit.
type CommitInfo struct {
	Subject string
	Hash    string
	PR      int
}

var rePR = regexp.MustCompile(`\(#(\d+)\)\s*$`)

func parsePR(subject string) int {
	m := rePR.FindStringSubmatch(subject)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Subjects returns the Subject field of each CommitInfo, preserving order.
func Subjects(commits []CommitInfo) []string {
	out := make([]string, len(commits))
	for i, c := range commits {
		out[i] = c.Subject
	}
	return out
}

// Client wraps a go-git repository.
type Client struct {
	repo *gogit.Repository

	// reachableCache holds the commit set of the current HEAD so history is
	// read once per HEAD rather than once per chart.
	reachableCache *reachable
}

// Open opens the git repository at the given path.
//
// DetectDotGit lets the caller point at any directory inside the working tree,
// and EnableDotGitCommonDir follows the .git file of a linked worktree through
// its commondir pointer: without it the worktree gitdir is treated as the whole
// repository, refs/HEAD live in the shared gitdir, and every HEAD resolution
// fails with "reference not found" on lanes.
func Open(path string) (*Client, error) {
	repo, err := gogit.PlainOpenWithOptions(path, &gogit.PlainOpenOptions{
		DetectDotGit: true,
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, fmt.Errorf("opening git repo at %s: %w", path, err)
	}
	return &Client{repo: repo}, nil
}

// LatestTag returns the highest-versioned tag matching "<chart>-v*" that is
// reachable from HEAD, or an empty string if none exists.
//
// Reachability is part of the answer, not a refinement of it. A tag sitting on
// a branch HEAD never integrated describes a release line this HEAD is not on,
// so adopting it as the baseline would measure this chart against work that is
// absent from HEAD.
//
// Ordering is semantic, not lexicographic: as strings "v0.4.99" sorts above
// "v0.4.120", which would pin the baseline to a stale release once a chart
// passes patch .99. Tags whose suffix is not valid semver are ignored.
func (c *Client) LatestTag(chart string) (string, error) {
	tags, err := c.repo.Tags()
	if err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	head, err := c.reachableFromHead()
	if err != nil {
		return "", err
	}
	prefix := chart + "-v"
	var bestName string
	var bestVersion *semver.Version
	err = tags.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		v, err := semver.NewVersion(strings.TrimPrefix(name, prefix))
		if err != nil {
			return nil
		}
		if !head.contains(c.tagTarget(ref)) {
			return nil
		}
		if bestVersion == nil || v.GreaterThan(bestVersion) {
			bestVersion = v
			bestName = name
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("iterating tags: %w", err)
	}
	return bestName, nil
}
