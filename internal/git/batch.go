package git

import (
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ChartRange names one chart's release window: the directory it owns and the
// tag its last release was cut from. An empty Tag means no baseline exists.
type ChartRange struct {
	Chart string
	Tag   string
	Path  string
}

// changedPaths returns the paths a single-parent commit changed against its
// real parent, and false for a merge.
func changedPaths(commit *object.Commit) (map[string]struct{}, bool, error) {
	if commit.NumParents() > 1 {
		return nil, false, nil
	}

	var parentTree *object.Tree
	if commit.NumParents() == 1 {
		parent, err := commit.Parent(0)
		if err != nil {
			return nil, false, fmt.Errorf("reading parent of %s: %w", commit.Hash, err)
		}
		parentTree, err = parent.Tree()
		if err != nil {
			return nil, false, fmt.Errorf("reading tree of %s: %w", parent.Hash, err)
		}
	}
	commitTree, err := commit.Tree()
	if err != nil {
		return nil, false, fmt.Errorf("reading tree of %s: %w", commit.Hash, err)
	}
	changes, err := object.DiffTree(parentTree, commitTree)
	if err != nil {
		return nil, false, fmt.Errorf("diffing %s: %w", commit.Hash, err)
	}

	paths := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if change.From.Name != "" {
			paths[change.From.Name] = struct{}{}
		}
		if change.To.Name != "" {
			paths[change.To.Name] = struct{}{}
		}
	}
	return paths, true, nil
}

// CommitsSinceBatch answers CommitsSince for every chart from ONE read of the
// shared range, instead of re-walking the same history once per chart.
//
// The work is identical for every chart: which commits exist between the
// baselines and HEAD, and which paths each one touched. Doing it per chart
// re-decodes the same trees N times — against the real fleet that is ~54s per
// changed chart, because go-git's path-filtered walk diffs a tree per visited
// commit and the packfile is large and deltified.
//
// Each chart still receives exactly its own `<tag>..HEAD -- <path>` set: the
// shared index is filtered per chart by ancestry of that chart's own tag, so a
// chart whose baseline is newer than the shared floor never sees commits it
// already released.
func (c *Client) CommitsSinceBatch(ranges []ChartRange) (map[string][]CommitInfo, error) {
	result := make(map[string][]CommitInfo, len(ranges))
	if len(ranges) == 0 {
		return result, nil
	}

	excluded := make(map[string]map[plumbing.Hash]struct{}, len(ranges))
	for _, r := range ranges {
		result[r.Chart] = nil
		if r.Tag == "" {
			continue
		}
		ref, err := c.repo.Tag(r.Tag)
		if err != nil {
			return nil, fmt.Errorf("resolving tag %q: %w", r.Tag, err)
		}
		ancestors, err := c.ancestorsOf(c.tagTarget(ref))
		if err != nil {
			return nil, err
		}
		excluded[r.Chart] = ancestors
	}

	head, err := c.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}
	iter, err := c.repo.Log(&gogit.LogOptions{From: head.Hash(), Order: gogit.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	defer iter.Close()

	if err := iter.ForEach(func(commit *object.Commit) error {
		wanted := make([]ChartRange, 0, len(ranges))
		for _, r := range ranges {
			if set, ok := excluded[r.Chart]; ok {
				if _, released := set[commit.Hash]; released {
					continue
				}
			}
			wanted = append(wanted, r)
		}
		if len(wanted) == 0 {
			return nil
		}

		paths, single, err := changedPaths(commit)
		if err != nil {
			return err
		}
		if !single {
			return nil
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(commit.Message), "\n")
		info := CommitInfo{Subject: subject, Hash: commit.Hash.String(), PR: parsePR(subject)}
		for _, r := range wanted {
			for path := range paths {
				if pathMatches(path, r.Path) {
					result[r.Chart] = append(result[r.Chart], info)
					break
				}
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterating commits: %w", err)
	}
	return result, nil
}
