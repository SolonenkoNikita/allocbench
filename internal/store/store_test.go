package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func tmpEntries(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.root, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestOpenCreatesLayout(t *testing.T) {
	s := openStore(t)
	for _, dir := range []string{"src", "env", "build", "tmp", "locks"} {
		if fi, err := os.Stat(filepath.Join(s.root, dir)); err != nil || !fi.IsDir() {
			t.Errorf("%s: not a directory: %v", dir, err)
		}
	}
	if _, err := Open(s.root); err != nil {
		t.Errorf("opening an existing store: %v", err)
	}
}

func TestCreateCommit(t *testing.T) {
	s := openStore(t)

	e, err := s.Create(Build, hashA)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.Has(Build, hashA) {
		t.Error("entry is visible before Commit")
	}
	if err := os.WriteFile(filepath.Join(e.Dir, "lib.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if !s.Has(Build, hashA) {
		t.Fatal("entry is missing after Commit")
	}
	data, err := os.ReadFile(filepath.Join(s.Path(Build, hashA), "lib.so"))
	if err != nil || string(data) != "x" {
		t.Errorf("content: %q, %v", data, err)
	}
	if names := tmpEntries(t, s); len(names) != 0 {
		t.Errorf("tmp is not empty: %v", names)
	}
}

func TestAbort(t *testing.T) {
	s := openStore(t)

	e, err := s.Create(Build, hashA)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.Dir, "half-built"), nil, 0o644)
	if err := e.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	if s.Has(Build, hashA) {
		t.Error("aborted entry is visible")
	}
	if names := tmpEntries(t, s); len(names) != 0 {
		t.Errorf("tmp is not empty: %v", names)
	}
	if e2, err := s.Create(Build, hashA); err != nil {
		t.Errorf("Create after Abort: %v", err)
	} else {
		e2.Abort()
	}
}

func TestAbortAfterCommitIsNoop(t *testing.T) {
	s := openStore(t)
	e, err := s.Create(Build, hashA)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := e.Abort(); err != nil {
		t.Errorf("Abort after Commit: %v", err)
	}
	if !s.Has(Build, hashA) {
		t.Error("Abort after Commit removed the entry")
	}
	if err := e.Commit(); err == nil {
		t.Error("second Commit should fail")
	}
}

func TestCreateExisting(t *testing.T) {
	s := openStore(t)
	e, _ := s.Create(Build, hashA)
	e.Commit()

	if _, err := s.Create(Build, hashA); !errors.Is(err, ErrExists) {
		t.Errorf("got %v, want ErrExists", err)
	}
}

func TestKindsAreSeparate(t *testing.T) {
	s := openStore(t)
	e, _ := s.Create(Build, hashA)
	e.Commit()

	if s.Has(Src, hashA) || s.Has(Env, hashA) {
		t.Error("a build entry is visible under another kind")
	}
	e2, err := s.Create(Src, hashA)
	if err != nil {
		t.Fatalf("Create src with the same hash: %v", err)
	}
	e2.Abort()
}

func TestCreateRemovesStaleTmp(t *testing.T) {
	s := openStore(t)

	// A crashed run leaves its tmp directory behind without Commit or Abort.
	e, _ := s.Create(Build, hashA)
	crashed := e.Dir
	e.unlock()

	other, _ := s.Create(Build, hashB)
	defer other.Abort()

	e2, err := s.Create(Build, hashA)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Abort()

	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Errorf("stale tmp %s was not removed", crashed)
	}
	if _, err := os.Stat(other.Dir); err != nil {
		t.Errorf("tmp of another hash was removed: %v", err)
	}
}

func TestCreateWaitsForSameHash(t *testing.T) {
	s := openStore(t)

	first, err := s.Create(Build, hashA)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.Create(Build, hashA)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("second Create did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrExists) {
			t.Errorf("second Create: got %v, want ErrExists", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second Create is still waiting after Commit")
	}
}

func TestCreateDoesNotWaitForOtherHash(t *testing.T) {
	s := openStore(t)
	first, _ := s.Create(Build, hashA)
	defer first.Abort()

	done := make(chan error, 1)
	go func() {
		e, err := s.Create(Build, hashB)
		if err == nil {
			e.Abort()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Create of another hash: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Create of another hash is blocked")
	}
}

func TestInvalidHash(t *testing.T) {
	s := openStore(t)
	for _, h := range []string{"", "abc", "../../etc", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := s.Create(Build, h); err == nil {
			t.Errorf("Create(%q) should fail", h)
		}
	}
}
