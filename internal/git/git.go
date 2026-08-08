// Package git provides git operations for helm-semver: reading commit history,
// writing version bump commits, tagging, and pushing.
package git

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// CommitInfo holds data about a single commit.
type CommitInfo struct {
	Subject string // First line of the commit message.
	Hash    string // Full 40-character SHA.
	PR      int    // GitHub PR number parsed from trailing "(#N)", 0 if absent.
}

var rePR = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// parsePR extracts a GitHub PR number from a commit subject line that ends with
// "(#N)", as produced by GitHub's default merge commit message format. Returns 0
// if no such suffix is present.
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
}

// Open opens the git repository at the given path.
func Open(path string) (*Client, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("opening git repo at %s: %w", path, err)
	}
	return &Client{repo: repo}, nil
}

// LatestTag returns the highest-versioned tag matching the pattern "<chart>-v*",
// or an empty string if none exists.
//
// Ordering is semantic, not lexicographic: as strings "v0.4.99" sorts above
// "v0.4.120", which would silently pin the baseline to a stale release once a
// chart passes patch .99 and make every later run re-read already-released
// history. Tags whose suffix is not valid semver are ignored rather than
// allowed to win the comparison.
func (c *Client) LatestTag(chart string) (string, error) {
	tags, err := c.repo.Tags()
	if err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}

	prefix := chart + "-v"
	var (
		bestName    string
		bestVersion *semver.Version
	)
	err = tags.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		v, err := semver.NewVersion(strings.TrimPrefix(name, prefix))
		if err != nil {
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

// pathMatches reports whether a changed file path lies inside the directory
// named by filter. A bare strings.HasPrefix would make the filter
// "charts/cosmos-vault" also swallow "charts/cosmos-vault-extras/Chart.yaml",
// attributing a sibling chart's commits to the wrong chart.
func pathMatches(path, filter string) bool {
	if filter == "" {
		return true
	}
	filter = strings.TrimSuffix(filter, "/")
	return path == filter || strings.HasPrefix(path, filter+"/")
}

// ancestorsOf returns the set of every commit hash reachable from start,
// walking the full graph with no path filter.
func (c *Client) ancestorsOf(start plumbing.Hash) (map[plumbing.Hash]struct{}, error) {
	iter, err := c.repo.Log(&gogit.LogOptions{From: start, Order: gogit.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("walking ancestry: %w", err)
	}
	defer iter.Close()

	seen := make(map[plumbing.Hash]struct{})
	if err := iter.ForEach(func(commit *object.Commit) error {
		seen[commit.Hash] = struct{}{}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("collecting ancestry: %w", err)
	}
	return seen, nil
}

// CommitsSince returns commits that touched pathFilter since the given tag,
// implementing `git log <tag>..HEAD -- <pathFilter>` semantics.
// If tag is empty, all commits touching pathFilter are returned. Each CommitInfo
// carries the commit subject, full SHA hash, and any GitHub PR number parsed
// from a "(#N)" trailer.
//
// Exclusion is by ANCESTRY, not by stopping on an exact hash match. The walk is
// path-filtered, so it only ever yields commits that touched this chart; a tag
// placed on a commit that did not touch this chart therefore never appears in
// that stream and a stop-on-hash check can never fire. The walk then runs off
// the end of history and re-collects commits that were already released, so a
// single old `feat:` commit re-bumps the chart on every subsequent run. That is
// the normal shape here: baseline tags are routinely created by release or CI
// commits that touch no chart directory at all.
func (c *Client) CommitsSince(tag, pathFilter string) ([]CommitInfo, error) {
	var excluded map[plumbing.Hash]struct{}

	if tag != "" {
		ref, err := c.repo.Tag(tag)
		if err != nil {
			return nil, fmt.Errorf("resolving tag %q: %w", tag, err)
		}
		// Tags can point to tag objects or directly to commits.
		since := ref.Hash()
		if tagObj, err := c.repo.TagObject(ref.Hash()); err == nil {
			since = tagObj.Target
		}
		if excluded, err = c.ancestorsOf(since); err != nil {
			return nil, err
		}
	}

	head, err := c.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	logOpts := &gogit.LogOptions{
		From:  head.Hash(),
		Order: gogit.LogOrderCommitterTime,
		PathFilter: func(path string) bool {
			return pathMatches(path, pathFilter)
		},
	}

	iter, err := c.repo.Log(logOpts)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	defer iter.Close()

	var commits []CommitInfo
	err = iter.ForEach(func(commit *object.Commit) error {
		if _, released := excluded[commit.Hash]; released {
			return nil
		}
		subject := strings.SplitN(strings.TrimSpace(commit.Message), "\n", 2)[0]
		commits = append(commits, CommitInfo{
			Subject: subject,
			Hash:    commit.Hash.String(),
			PR:      parsePR(subject),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterating commits: %w", err)
	}

	return commits, nil
}

// StageFile adds a file to the git index.
func (c *Client) StageFile(path string) error {
	wt, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}
	if _, err := wt.Add(path); err != nil {
		return fmt.Errorf("staging %s: %w", path, err)
	}
	return nil
}

// Commit creates a new commit with the given message and author details.
func (c *Client) Commit(message, authorName, authorEmail string) error {
	wt, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}

	opts := &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	}

	if _, err := wt.Commit(message, opts); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// Tag creates a lightweight tag at HEAD.
func (c *Client) Tag(name string) error {
	head, err := c.repo.Head()
	if err != nil {
		return fmt.Errorf("resolving HEAD: %w", err)
	}

	_, err = c.repo.CreateTag(name, head.Hash(), nil)
	if err != nil {
		return fmt.Errorf("creating tag %q: %w", name, err)
	}
	return nil
}

// Push pushes the current branch and all tags to the named remote.
// If token is non-empty and the remote URL uses HTTPS, the token is used as
// Basic Auth ("x-access-token" / token). For SSH remotes the token is ignored
// — the host's SSH agent handles authentication instead.
func (c *Client) Push(remote, token string) error {
	opts := &gogit.PushOptions{
		RemoteName: remote,
		RefSpecs: []config.RefSpec{
			"refs/heads/*:refs/heads/*",
			"refs/tags/*:refs/tags/*",
		},
	}
	if token != "" && c.remoteIsHTTPS(remote) {
		opts.Auth = &http.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	}
	err := c.repo.Push(opts)
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("pushing to %s: %w", remote, err)
	}
	return nil
}

// remoteIsHTTPS returns true when the first URL of the named remote uses
// http:// or https://.  SSH remotes (git@ or ssh://) return false.
func (c *Client) remoteIsHTTPS(name string) bool {
	r, err := c.repo.Remote(name)
	if err != nil || len(r.Config().URLs) == 0 {
		// Can't determine — assume HTTPS so we at least try to auth.
		return true
	}
	u := r.Config().URLs[0]
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}
