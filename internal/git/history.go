package git

import (
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func pathMatches(path, filter string) bool {
	if filter == "" {
		return true
	}
	filter = strings.TrimSuffix(filter, "/")
	return path == filter || strings.HasPrefix(path, filter+"/")
}


func (c *Client) treeHashAt(commitHash plumbing.Hash, path string) (plumbing.Hash, bool, error) {
	commit, err := c.repo.CommitObject(commitHash)
	if err != nil {
		return plumbing.ZeroHash, false, fmt.Errorf("reading commit %s: %w", commitHash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return plumbing.ZeroHash, false, fmt.Errorf("reading tree of %s: %w", commitHash, err)
	}
	entry, err := tree.FindEntry(path)
	if err != nil {
		return plumbing.ZeroHash, false, nil
	}
	return entry.Hash, true, nil
}

func (c *Client) PathUnchangedSince(tag, path string) (bool, error) {
	if tag == "" || path == "" {
		return false, nil
	}
	ref, err := c.repo.Tag(tag)
	if err != nil {
		return false, fmt.Errorf("resolving tag %q: %w", tag, err)
	}
	tagged := c.tagTarget(ref)
	head, err := c.repo.Head()
	if err != nil {
		return false, fmt.Errorf("resolving HEAD: %w", err)
	}
	before, hadBefore, err := c.treeHashAt(tagged, path)
	if err != nil {
		return false, err
	}
	after, hasAfter, err := c.treeHashAt(head.Hash(), path)
	if err != nil {
		return false, err
	}
	if !hadBefore || !hasAfter {
		return false, nil
	}
	return before == after, nil
}

func (c *Client) CommitsSince(tag, pathFilter string) ([]CommitInfo, error) {
	var excluded map[plumbing.Hash]struct{}
	if tag != "" {
		ref, err := c.repo.Tag(tag)
		if err != nil {
			return nil, fmt.Errorf("resolving tag %q: %w", tag, err)
		}
		if excluded, err = c.ancestorsOf(c.tagTarget(ref)); err != nil {
			return nil, err
		}
	}
	head, err := c.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}
	iter, err := c.repo.Log(&gogit.LogOptions{
		From:  head.Hash(),
		Order: gogit.LogOrderCommitterTime,
		PathFilter: func(path string) bool {
			return pathMatches(path, pathFilter)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	defer iter.Close()
	var commits []CommitInfo
	if err := iter.ForEach(func(commit *object.Commit) error {
		if _, released := excluded[commit.Hash]; released {
			return nil
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(commit.Message), "\n")
		commits = append(commits, CommitInfo{Subject: subject, Hash: commit.Hash.String(), PR: parsePR(subject)})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterating commits: %w", err)
	}
	return commits, nil
}
