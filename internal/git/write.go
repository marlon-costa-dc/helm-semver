package git

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Commit records the given paths as one commit and leaves the rest of the
// index exactly as it found it.
//
// The paths are the entire content of the commit. A release commit is signed
// as the bot, carries [skip ci], and lands on the branch the pipeline pushes;
// committing the whole index would make it adopt whatever else happened to be
// staged — another step's work, a hook's output, a developer's local staging —
// and publish it under the bot's name in a commit nobody reviews.
//
// go-git commits the index as a whole, with no path selection, so isolation is
// built here: the caller's index is captured, reduced to HEAD plus this
// release's paths for the duration of the commit, then restored. Staging is
// done here rather than by the caller so no window exists in which this
// package has staged a file it has not yet committed.
func (c *Client) Commit(message, authorName, authorEmail string, paths ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("committing %q: no paths given", message)
	}
	wt, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}

	callerIndex, err := c.repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("reading index: %w", err)
	}
	preserved := *callerIndex
	preserved.Entries = append([]*index.Entry(nil), callerIndex.Entries...)

	if err := c.stageOnly(wt, paths); err != nil {
		return err
	}

	_, commitErr := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})

	// The caller's index is restored whether or not the commit succeeded: the
	// entries it holds belong to whoever staged them, and a failed release must
	// not consume them either.
	if err := c.restoreIndex(&preserved, paths); err != nil {
		if commitErr != nil {
			return fmt.Errorf("committing: %w", commitErr)
		}
		return err
	}
	if commitErr != nil {
		return fmt.Errorf("committing: %w", commitErr)
	}
	return nil
}

// stageOnly reduces the index to what HEAD already records plus the given
// paths, so the next commit records exactly this release.
func (c *Client) stageOnly(wt *gogit.Worktree, paths []string) error {
	headEntries, err := c.headIndexEntries()
	if err != nil {
		return err
	}

	idx, err := c.repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("reading index: %w", err)
	}
	idx.Entries = headEntries
	if err := c.repo.Storer.SetIndex(idx); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}

	for _, path := range paths {
		if _, err := wt.Add(path); err != nil {
			return fmt.Errorf("staging %s: %w", path, err)
		}
	}
	return nil
}

// headIndexEntries returns the index entries that reproduce the tree HEAD
// records, so a commit built from them changes nothing on its own.
func (c *Client) headIndexEntries() ([]*index.Entry, error) {
	head, err := c.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}
	commit, err := c.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("reading commit %s: %w", head.Hash(), err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("reading tree of %s: %w", head.Hash(), err)
	}

	var entries []*index.Entry
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("walking tree of %s: %w", head.Hash(), err)
		}
		if !entry.Mode.IsFile() {
			continue
		}
		entries = append(entries, &index.Entry{
			Name: name,
			Hash: entry.Hash,
			Mode: entry.Mode,
		})
	}
	return entries, nil
}

// restoreIndex gives the caller's index back, carrying forward the release
// paths as they were just committed so they are not left looking modified.
func (c *Client) restoreIndex(preserved *index.Index, paths []string) error {
	committed, err := c.headIndexEntries()
	if err != nil {
		return err
	}
	released := make(map[string]*index.Entry, len(paths))
	for _, entry := range committed {
		released[entry.Name] = entry
	}

	restored := make([]*index.Entry, 0, len(preserved.Entries))
	for _, entry := range preserved.Entries {
		if current, ok := released[entry.Name]; ok {
			restored = append(restored, current)
			delete(released, entry.Name)
			continue
		}
		restored = append(restored, entry)
	}
	for _, path := range paths {
		if entry, ok := released[path]; ok {
			restored = append(restored, entry)
		}
	}

	idx, err := c.repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("reading index: %w", err)
	}
	idx.Entries = restored
	if err := c.repo.Storer.SetIndex(idx); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}
	return nil
}

func (c *Client) Tag(name string) error {
	head, err := c.repo.Head()
	if err != nil {
		return fmt.Errorf("resolving HEAD: %w", err)
	}
	if _, err := c.repo.CreateTag(name, head.Hash(), nil); err != nil {
		return fmt.Errorf("creating tag %q: %w", name, err)
	}
	return nil
}

func (c *Client) Push(remote, token string) error {
	opts := &gogit.PushOptions{
		RemoteName: remote,
		RefSpecs: []config.RefSpec{
			"refs/heads/*:refs/heads/*",
			"refs/tags/*:refs/tags/*",
		},
	}
	if token != "" && c.remoteIsHTTPS(remote) {
		opts.Auth = &http.BasicAuth{Username: "x-access-token", Password: token}
	}
	if err := c.repo.Push(opts); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("pushing to %s: %w", remote, err)
	}
	return nil
}

func (c *Client) remoteIsHTTPS(name string) bool {
	r, err := c.repo.Remote(name)
	if err != nil || len(r.Config().URLs) == 0 {
		return true
	}
	u := r.Config().URLs[0]
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}
