package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tiennm99/ghglance/internal/card"
	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

// genDir holds every rendered generation. <data>/<user> is a symlink to the
// current one, so publishing a new set is a single atomic rename of that
// link and readers never see a half-written directory. Logins cannot start
// with a dot, so neither name collides with a user.
const genDir = ".gen"

const metaFile = "meta.json"

// Meta is the small record written beside a user's cards.
type Meta struct {
	Login       string    `json:"login"`
	GeneratedAt time.Time `json:"generated_at"`
	// Scope is "private" when the submitter's own token rendered private
	// repos, otherwise "public".
	Scope   string  `json:"scope"`
	Options Options `json:"options"`
}

// Store reads and publishes card sets under one data directory. Reads go
// through an os.Root, so even a path that slipped past validation cannot
// leave the data directory.
type Store struct {
	dir  string
	root *os.Root
	// mu serializes link swaps in Publish with removals in Expire, so an
	// expiry never deletes a set that was republished after it was checked.
	mu sync.Mutex
}

// OpenStore creates dir if needed and clears generations a crash left
// unpublished.
func OpenStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, genDir), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: abs, root: root}
	s.sweep()
	return s, nil
}

// Close releases the data directory handle.
func (s *Store) Close() error { return s.root.Close() }

// userKey is the on-disk name for a login. GitHub logins are
// case-insensitive, so every lookup goes through the lowercased form.
func userKey(login string) string { return strings.ToLower(login) }

// Meta returns the published record for login, or fs.ErrNotExist.
func (s *Store) Meta(login string) (*Meta, error) {
	if !validUsername(login) {
		return nil, fs.ErrNotExist
	}
	raw, err := s.root.ReadFile(path.Join(userKey(login), metaFile))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode meta: %w", err)
	}
	return &m, nil
}

// OpenCard opens one rendered SVG. Every segment is validated against the
// known logins, themes and cards before it touches the filesystem.
func (s *Store) OpenCard(login, themeID, file string) (*os.File, error) {
	if !validUsername(login) || !validTheme(themeID) || !validCard(file) {
		return nil, fs.ErrNotExist
	}
	return s.root.Open(path.Join(userKey(login), themeID, file))
}

// Publish renders every theme for p into a fresh generation directory,
// writes meta.json, then atomically repoints <data>/<user> at it and removes
// the generation it replaced.
func (s *Store) Publish(p *github.Profile, meta Meta) error {
	if !validUsername(meta.Login) {
		return fmt.Errorf("invalid login %q", meta.Login)
	}
	key := userKey(meta.Login)
	gen := key + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	genRel := path.Join(genDir, gen)
	genAbs := filepath.Join(s.dir, genDir, gen)

	ok := false
	defer func() {
		if !ok {
			logCleanup(os.RemoveAll(genAbs))
		}
	}()

	for _, id := range theme.IDs() {
		t, _ := theme.Lookup(id)
		if err := card.RenderAll(p, t, genAbs); err != nil {
			return fmt.Errorf("render %s: %w", id, err)
		}
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(genAbs, metaFile), raw, 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	old, _ := s.root.Readlink(key)
	tmpLink := ".link-" + gen
	if err := s.root.Symlink(genRel, tmpLink); err != nil {
		return fmt.Errorf("link generation: %w", err)
	}
	if err := s.root.Rename(tmpLink, key); err != nil {
		logCleanup(s.root.Remove(tmpLink))
		return fmt.Errorf("publish generation: %w", err)
	}
	ok = true
	if old != "" && old != genRel && isGenTarget(old) {
		logCleanup(s.root.RemoveAll(old))
	}
	return nil
}

// logCleanup logs a failed best-effort removal. Nothing depends on it
// succeeding: the sweep at the next start removes whatever is left.
func logCleanup(err error) {
	if err != nil {
		log.Printf("store cleanup: %v", err)
	}
}

// isGenTarget reports whether a link target is a generation directory this
// store created, so cleanup never follows a link anywhere else.
func isGenTarget(target string) bool {
	dir, name := path.Split(target)
	return dir == genDir+"/" && name != "" && name != "." && name != ".."
}

// sweep removes generations no user link points at and leftover temporary
// links; both are what a crash between render and publish leaves behind.
func (s *Store) sweep() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".link-") {
			logCleanup(s.root.Remove(name))
			continue
		}
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		if target, err := s.root.Readlink(name); err == nil && isGenTarget(target) {
			live[path.Base(target)] = true
		}
	}
	gens, err := os.ReadDir(filepath.Join(s.dir, genDir))
	if err != nil {
		return
	}
	for _, g := range gens {
		if !live[g.Name()] {
			logCleanup(s.root.RemoveAll(path.Join(genDir, g.Name())))
		}
	}
}

// Expire removes every user whose cards were generated more than retention
// before now, along with their generation directory. retention <= 0 keeps
// cards forever. It returns how many users were removed.
func (s *Store) Expire(retention time.Duration, now time.Time) int {
	if retention <= 0 {
		return 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.Type()&fs.ModeSymlink == 0 || !validUsername(name) {
			continue
		}
		if s.expireOne(name, retention, now) {
			removed++
		}
	}
	return removed
}

// expireOne removes one user's link and generation when its meta is older
// than retention, re-reading both under the lock.
func (s *Store) expireOne(key string, retention time.Duration, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Meta(key)
	if err != nil || now.Sub(m.GeneratedAt) <= retention {
		return false
	}
	target, err := s.root.Readlink(key)
	if err != nil {
		return false
	}
	if err := s.root.Remove(key); err != nil {
		return false
	}
	if isGenTarget(target) {
		logCleanup(s.root.RemoveAll(target))
	}
	return true
}

// cooldownLeft is how long until login's cards may be regenerated without
// the submitter's own token; zero when they may be regenerated now.
func (s *Store) cooldownLeft(login string, cooldown time.Duration, now time.Time) time.Duration {
	m, err := s.Meta(login)
	if err != nil {
		return 0
	}
	left := m.GeneratedAt.Add(cooldown).Sub(now)
	if left < 0 {
		return 0
	}
	return left
}

// isNotExist reports a missing user, theme or card.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
