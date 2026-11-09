package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

type Fetcher struct {
	Store  *store.Store
	Root   string
	Git    string
	Client *http.Client
}

type Result struct {
	Key  string
	Tree string
	Dir  string
}

func (f *Fetcher) Fetch(src recipe.Source) (Result, error) {
	key, err := f.key(src)
	if err != nil {
		return Result{}, err
	}

	e, err := f.Store.Create(store.Src, key)
	if errors.Is(err, store.ErrExists) {
		return f.result(key)
	}
	if err != nil {
		return Result{}, err
	}
	defer e.Abort()

	switch {
	case src.Git != "":
		err = f.fetchGit(src, e.Dir)
	case src.URL != "":
		err = f.fetchURL(src, e.Dir)
	case src.Path != "":
		err = copyTree(filepath.Join(f.Root, src.Path), e.Dir)
	default:
		err = errors.New("source has no git, url or path")
	}
	if err != nil {
		return Result{}, err
	}

	if src.Git != "" && src.SHA256 != "" {
		tree, err := TreeHash(e.Dir)
		if err != nil {
			return Result{}, err
		}
		if tree != src.SHA256 {
			return Result{}, fmt.Errorf("%s at %s: tree hash %s does not match sha256 %s", src.Git, src.Rev, tree, src.SHA256)
		}
	}

	for _, p := range src.Patches {
		if err := f.run(e.Dir, f.git(), "apply", "--whitespace=nowarn", filepath.Join(f.Root, p)); err != nil {
			return Result{}, fmt.Errorf("patch %s: %w", p, err)
		}
	}

	if err := e.Commit(); err != nil {
		return Result{}, err
	}
	return f.result(key)
}

func (f *Fetcher) result(key string) (Result, error) {
	dir := f.Store.Path(store.Src, key)
	tree, err := TreeHash(dir)
	if err != nil {
		return Result{}, err
	}
	return Result{Key: key, Tree: tree, Dir: dir}, nil
}

func (f *Fetcher) key(src recipe.Source) (string, error) {
	h := sha256.New()
	switch {
	case src.Git != "":
		fmt.Fprintf(h, "git\x00%s\x00%s\x00%s\n", src.Git, src.Rev, src.SHA256)
	case src.URL != "":
		fmt.Fprintf(h, "url\x00%s\x00%s\n", src.URL, src.SHA256)
	case src.Path != "":
		tree, err := TreeHash(filepath.Join(f.Root, src.Path))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "path\x00%s\n", tree)
	default:
		return "", errors.New("source has no git, url or path")
	}

	for _, p := range src.Patches {
		sum, _, err := fileHash(filepath.Join(f.Root, p))
		if err != nil {
			return "", fmt.Errorf("patch %s: %w", p, err)
		}
		fmt.Fprintf(h, "patch\x00%s\n", sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (f *Fetcher) fetchGit(src recipe.Source, dir string) error {
	git := f.git()
	if err := f.run(dir, git, "init", "-q"); err != nil {
		return err
	}
	if err := f.run(dir, git, "fetch", "-q", "--depth", "1", src.Git, src.Rev); err != nil {
		if err := f.run(dir, git, "fetch", "-q", src.Git, "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return fmt.Errorf("git fetch %s: %w", src.Git, err)
		}
	}
	if err := f.run(dir, git, "-c", "advice.detachedHead=false", "checkout", "-q", src.Rev); err != nil {
		return fmt.Errorf("git checkout %s: %w", src.Rev, err)
	}
	return os.RemoveAll(filepath.Join(dir, ".git"))
}

func (f *Fetcher) fetchURL(src recipe.Source, dir string) error {
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(src.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", src.URL, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dir), "download-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return fmt.Errorf("%s: %w", src.URL, err)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); src.SHA256 != "" && sum != src.SHA256 {
		return fmt.Errorf("%s: sha256 %s does not match %s", src.URL, sum, src.SHA256)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	name := filepath.Base(resp.Request.URL.Path)
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return extractTar(tmp, dir, true)
	case strings.HasSuffix(name, ".tar"):
		return extractTar(tmp, dir, false)
	}
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, tmp)
	return err
}

func (f *Fetcher) git() string {
	if f.Git != "" {
		return f.Git
	}
	return "git"
}

func (f *Fetcher) run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
