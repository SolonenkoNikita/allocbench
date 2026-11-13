package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SolonenkoNikita/allocbench/internal/logs"
	"github.com/SolonenkoNikita/allocbench/internal/proc"
	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/source"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

const (
	logTail   = 20
	rootfsDir = "rootfs"
	infoFile  = "env.json"
)

type Builder struct {
	Store      *store.Store
	Mmdebstrap string
	Mirror     string
	Proxy      string
	Log        io.Writer
}

type Env struct {
	Hash   string `json:"-"`
	Rootfs string `json:"-"`
	Tree   string `json:"tree"`
	Mirror string `json:"mirror"`
}

func (b *Builder) Ensure(ctx context.Context, e recipe.Env, hash string) (Env, error) {
	entry, err := b.Store.Create(store.Env, hash)
	if errors.Is(err, store.ErrExists) {
		return b.load(hash)
	}
	if err != nil {
		return Env{}, err
	}
	defer entry.Abort()

	mirror := b.mirror(e)
	tarball := filepath.Join(entry.Dir, "rootfs.tar")
	args, err := Args(e, tarball, mirror, b.Proxy)
	if err != nil {
		return Env{}, err
	}
	epoch, err := SnapshotEpoch(e.Snapshot)
	if err != nil {
		return Env{}, err
	}

	logPath := b.Store.Log(store.Env, hash)
	log, err := os.Create(logPath)
	if err != nil {
		return Env{}, err
	}
	defer log.Close()
	var w io.Writer = log
	if b.Log != nil {
		w = io.MultiWriter(log, b.Log)
	}

	cmd := proc.Command(ctx, b.mmdebstrap(), args...)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH="+strconv.FormatInt(epoch, 10))
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		if stopped := proc.Stopped(ctx); stopped != nil {
			return Env{}, stopped
		}
		return Env{}, fmt.Errorf("mmdebstrap: %w\n%s\nfull log: %s", err, logs.Tail(logPath, logTail), logPath)
	}

	f, err := os.Open(tarball)
	if err != nil {
		return Env{}, err
	}
	err = extractRootfs(f, filepath.Join(entry.Dir, rootfsDir))
	f.Close()
	if err != nil {
		return Env{}, err
	}
	if err := os.Remove(tarball); err != nil {
		return Env{}, err
	}

	tree, err := source.TreeHash(filepath.Join(entry.Dir, rootfsDir))
	if err != nil {
		return Env{}, err
	}
	info, err := json.MarshalIndent(Env{Tree: tree, Mirror: mirror}, "", "  ")
	if err != nil {
		return Env{}, err
	}
	if err := os.WriteFile(filepath.Join(entry.Dir, infoFile), info, 0o644); err != nil {
		return Env{}, err
	}

	if err := entry.Commit(); err != nil {
		return Env{}, err
	}
	return b.load(hash)
}

func (b *Builder) load(hash string) (Env, error) {
	dir := b.Store.Path(store.Env, hash)
	data, err := os.ReadFile(filepath.Join(dir, infoFile))
	if err != nil {
		return Env{}, err
	}
	var e Env
	if err := json.Unmarshal(data, &e); err != nil {
		return Env{}, fmt.Errorf("%s: %w", infoFile, err)
	}
	e.Hash = hash
	e.Rootfs = filepath.Join(dir, rootfsDir)
	return e, nil
}

func (b *Builder) mirror(e recipe.Env) string {
	if b.Mirror != "" {
		return b.Mirror
	}
	return SnapshotMirror(e.Snapshot)
}

func SnapshotMirror(snapshot string) string {
	return "https://snapshot.ubuntu.com/ubuntu/" + snapshot
}

func (b *Builder) mmdebstrap() string {
	if b.Mmdebstrap != "" {
		return b.Mmdebstrap
	}
	return "mmdebstrap"
}

func Args(e recipe.Env, out, mirror, proxy string) ([]string, error) {
	if e.Distro != "ubuntu" {
		return nil, fmt.Errorf("distro %q is not supported yet", e.Distro)
	}

	pkgs := slices.Clone(e.Packages)
	sort.Strings(pkgs)
	pkgs = slices.Compact(pkgs)

	args := []string{"--mode=unshare", "--variant=apt", "--include=" + strings.Join(pkgs, ",")}
	if proxy != "" {
		args = append(args,
			fmt.Sprintf("--aptopt=Acquire::http::Proxy %q", proxy),
			fmt.Sprintf("--aptopt=Acquire::https::Proxy %q", proxy))
	}
	args = append(args, e.Suite, out)
	for _, suite := range []string{e.Suite, e.Suite + "-updates", e.Suite + "-security"} {
		args = append(args, fmt.Sprintf("deb [check-valid-until=no] %s %s main universe", mirror, suite))
	}
	return args, nil
}

func SnapshotEpoch(snapshot string) (int64, error) {
	t, err := time.Parse("20060102T150405Z", snapshot)
	if err != nil {
		return 0, fmt.Errorf("snapshot %q: %w", snapshot, err)
	}
	return t.Unix(), nil
}
