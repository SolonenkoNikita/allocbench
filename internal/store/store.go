package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

type Kind string

const (
	Src   Kind = "src"
	Env   Kind = "env"
	Build Kind = "build"
)

var (
	ErrExists   = errors.New("store entry already exists")
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Store keeps entries addressed by hash. An entry appears only when complete:
// it is written to tmp/ and renamed into place on Commit.
type Store struct {
	root string
}

func Open(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{string(Src), string(Env), string(Build), "tmp", "locks", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

func (s *Store) Path(kind Kind, hash string) string {
	return filepath.Join(s.root, string(kind), hash)
}

func (s *Store) Log(kind Kind, hash string) string {
	return filepath.Join(s.root, "logs", string(kind)+"-"+hash+".log")
}

func (s *Store) Has(kind Kind, hash string) bool {
	_, err := os.Stat(s.Path(kind, hash))
	return err == nil
}

type Entry struct {
	Dir   string
	final string
	lock  *os.File
}

// Create locks the hash and returns a temporary directory for a new entry.
// It waits while another process writes the same hash, and returns ErrExists
// if the entry is already complete.
func (s *Store) Create(kind Kind, hash string) (*Entry, error) {
	if !hashPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid hash %q", hash)
	}
	name := string(kind) + "-" + hash

	lock, err := os.OpenFile(filepath.Join(s.root, "locks", name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}

	e := &Entry{final: s.Path(kind, hash), lock: lock}
	if s.Has(kind, hash) {
		e.unlock()
		return nil, ErrExists
	}

	stale, err := filepath.Glob(filepath.Join(s.root, "tmp", name+"-*"))
	if err != nil {
		e.unlock()
		return nil, err
	}
	for _, dir := range stale {
		if err := os.RemoveAll(dir); err != nil {
			e.unlock()
			return nil, err
		}
	}

	e.Dir, err = os.MkdirTemp(filepath.Join(s.root, "tmp"), name+"-")
	if err != nil {
		e.unlock()
		return nil, err
	}
	return e, nil
}

func (e *Entry) Commit() error {
	if e.lock == nil {
		return errors.New("store entry is already closed")
	}
	defer e.unlock()
	if err := os.Rename(e.Dir, e.final); err != nil {
		os.RemoveAll(e.Dir)
		return err
	}
	return nil
}

// Abort removes the temporary directory and releases the lock.
// It does nothing after Commit, so it is safe to defer.
func (e *Entry) Abort() error {
	if e.lock == nil {
		return nil
	}
	defer e.unlock()
	return os.RemoveAll(e.Dir)
}

func (e *Entry) unlock() {
	syscall.Flock(int(e.lock.Fd()), syscall.LOCK_UN)
	e.lock.Close()
	e.lock = nil
}
