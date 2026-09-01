package git

import (
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// reachable is the set of commits reachable from a specific HEAD, read once.
//
// Every question this package asks of history is a question about what HEAD
// contains: which baseline tag is on this line, and which commits sit between
// that baseline and HEAD. Asking the repository per chart re-walks the same
// graph N times and re-decodes the same objects; the answer is identical every
// time. Reading once and answering from memory collapses that to a single pass.
//
// head is recorded so the snapshot is discarded the moment HEAD moves — this
// package writes commits and tags during a release, so a snapshot that outlived
// its HEAD would answer for a repository state that no longer exists.
type reachable struct {
	head    plumbing.Hash
	commits map[plumbing.Hash]struct{}
}

// reachableFromHead returns the commit set reachable from the current HEAD,
// reusing the snapshot while HEAD has not moved.
func (c *Client) reachableFromHead() (*reachable, error) {
	head, err := c.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}
	if c.reachableCache != nil && c.reachableCache.head == head.Hash() {
		return c.reachableCache, nil
	}

	commits, err := c.ancestorsOf(head.Hash())
	if err != nil {
		return nil, err
	}
	c.reachableCache = &reachable{head: head.Hash(), commits: commits}
	return c.reachableCache, nil
}

// contains reports whether the commit is part of the history HEAD describes.
func (r *reachable) contains(commit plumbing.Hash) bool {
	_, ok := r.commits[commit]
	return ok
}

// tagTarget resolves a tag reference to the commit it releases, following an
// annotated tag object to its target.
func (c *Client) tagTarget(ref *plumbing.Reference) plumbing.Hash {
	if tagObj, err := c.repo.TagObject(ref.Hash()); err == nil {
		return tagObj.Target
	}
	return ref.Hash()
}

// ancestorsOf returns every commit hash reachable from start.
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
