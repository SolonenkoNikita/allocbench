package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SolonenkoNikita/allocbench/internal/recipe"
	"github.com/SolonenkoNikita/allocbench/internal/store"
)

func newFetcher(t *testing.T) *Fetcher {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return &Fetcher{Store: s, Root: t.TempDir()}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func srcEntries(t *testing.T, f *Fetcher) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(f.Store.Path(store.Src, strings.Repeat("0", 64))))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestFetchPath(t *testing.T) {
	f := newFetcher(t)
	writeFile(t, filepath.Join(f.Root, "bench-src", "cfrac", "cfrac.c"), "int main() {}\n", 0o644)
	writeFile(t, filepath.Join(f.Root, "bench-src", "cfrac", "run.sh"), "#!/bin/sh\n", 0o755)
	src := recipe.Source{Path: "bench-src/cfrac"}

	r, err := f.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := readFile(t, filepath.Join(r.Dir, "cfrac.c")); got != "int main() {}\n" {
		t.Errorf("content: %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(r.Dir, "run.sh")); fi.Mode()&0o111 == 0 {
		t.Error("executable bit lost")
	}
	if want := mustTreeHash(t, filepath.Join(f.Root, "bench-src", "cfrac")); r.Tree != want {
		t.Errorf("tree: got %s, want %s", r.Tree, want)
	}

	again, err := f.Fetch(context.Background(), src)
	if err != nil || again != r {
		t.Errorf("second Fetch: %+v, %v", again, err)
	}

	writeFile(t, filepath.Join(f.Root, "bench-src", "cfrac", "cfrac.c"), "int main() { return 1; }\n", 0o644)
	changed, err := f.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Key == r.Key || changed.Tree == r.Tree {
		t.Error("editing a file did not change the key")
	}
	if got := readFile(t, filepath.Join(r.Dir, "cfrac.c")); got != "int main() {}\n" {
		t.Error("the old store entry changed")
	}
}

func gitRepo(t *testing.T) (url string, revs []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for _, v := range []string{"1", "2"} {
		writeFile(t, filepath.Join(dir, "src", "version.c"), "int version = "+v+";\n", 0o644)
		git("add", ".")
		git("commit", "-q", "-m", "v"+v)
		revs = append(revs, git("rev-parse", "HEAD"))
	}
	return "file://" + dir, revs
}

func TestFetchGit(t *testing.T) {
	url, revs := gitRepo(t)
	f := newFetcher(t)

	r, err := f.Fetch(context.Background(), recipe.Source{Git: url, Rev: revs[0]})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := readFile(t, filepath.Join(r.Dir, "src", "version.c")); got != "int version = 1;\n" {
		t.Errorf("checked out the wrong commit: %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); !os.IsNotExist(err) {
		t.Error(".git was not removed")
	}

	r2, err := f.Fetch(context.Background(), recipe.Source{Git: url, Rev: revs[1]})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Key == r.Key || r2.Tree == r.Tree {
		t.Error("different revs got the same key or tree")
	}
}

func TestFetchGitSHA256(t *testing.T) {
	url, revs := gitRepo(t)
	f := newFetcher(t)
	r, err := f.Fetch(context.Background(), recipe.Source{Git: url, Rev: revs[0]})
	if err != nil {
		t.Fatal(err)
	}

	f2 := newFetcher(t)
	if _, err := f2.Fetch(context.Background(), recipe.Source{Git: url, Rev: revs[0], SHA256: r.Tree}); err != nil {
		t.Errorf("matching sha256: %v", err)
	}

	f3 := newFetcher(t)
	_, err = f3.Fetch(context.Background(), recipe.Source{Git: url, Rev: revs[0], SHA256: strings.Repeat("0", 64)})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("wrong sha256: got %v", err)
	}
	if n := srcEntries(t, f3); n != 0 {
		t.Errorf("failed fetch left %d store entries", n)
	}
}

func TestFetchGitUsesStore(t *testing.T) {
	url, revs := gitRepo(t)
	f := newFetcher(t)
	src := recipe.Source{Git: url, Rev: revs[0]}
	r, err := f.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	os.RemoveAll(strings.TrimPrefix(url, "file://"))
	again, err := f.Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("Fetch from store without the repository: %v", err)
	}
	if again != r {
		t.Errorf("got %+v, want %+v", again, r)
	}
}

func TestFetchGitUnknownRev(t *testing.T) {
	url, _ := gitRepo(t)
	f := newFetcher(t)
	_, err := f.Fetch(context.Background(), recipe.Source{Git: url, Rev: strings.Repeat("d", 40)})
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := srcEntries(t, f); n != 0 {
		t.Errorf("failed fetch left %d store entries", n)
	}
}

type tarEntry struct {
	name, body, link string
	mode             int64
	typ              byte
}

func makeTar(t *testing.T, gz bool, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w *tar.Writer
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = tar.NewWriter(zw)
	} else {
		w = tar.NewWriter(&buf)
	}
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			w.Write([]byte(e.body))
		}
	}
	w.Close()
	if zw != nil {
		zw.Close()
	}
	return buf.Bytes()
}

func serve(t *testing.T, files map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestFetchURLArchive(t *testing.T) {
	archive := makeTar(t, true,
		tarEntry{name: "cmake-3.31/", typ: tar.TypeDir},
		tarEntry{name: "cmake-3.31/bin/cmake", body: "#!/bin/sh\n", mode: 0o755},
		tarEntry{name: "cmake-3.31/share/readme", body: "hello"},
		tarEntry{name: "cmake-3.31/bin/ccmake", link: "cmake", typ: tar.TypeSymlink},
	)
	base := serve(t, map[string][]byte{"/cmake.tar.gz": archive})
	f := newFetcher(t)

	r, err := f.Fetch(context.Background(), recipe.Source{URL: base + "/cmake.tar.gz", SHA256: sum(archive)})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := readFile(t, filepath.Join(r.Dir, "share", "readme")); got != "hello" {
		t.Errorf("top directory was not stripped or content is wrong: %q", got)
	}
	if fi, err := os.Stat(filepath.Join(r.Dir, "bin", "cmake")); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("executable bit lost: %v", err)
	}
	if link, err := os.Readlink(filepath.Join(r.Dir, "bin", "ccmake")); err != nil || link != "cmake" {
		t.Errorf("symlink: %q, %v", link, err)
	}
}

func TestFetchURLPlainTarAndFile(t *testing.T) {
	plain := makeTar(t, false, tarEntry{name: "a.txt", body: "a"}, tarEntry{name: "b.txt", body: "b"})
	base := serve(t, map[string][]byte{"/src.tar": plain, "/tool.sh": []byte("#!/bin/sh\n")})
	f := newFetcher(t)

	r, err := f.Fetch(context.Background(), recipe.Source{URL: base + "/src.tar"})
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(r.Dir, "a.txt")) != "a" || readFile(t, filepath.Join(r.Dir, "b.txt")) != "b" {
		t.Error("plain tar was not extracted")
	}

	r, err = f.Fetch(context.Background(), recipe.Source{URL: base + "/tool.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(r.Dir, "tool.sh")) != "#!/bin/sh\n" {
		t.Error("plain file was not saved")
	}
}

func TestFetchURLErrors(t *testing.T) {
	good := makeTar(t, true, tarEntry{name: "x", body: "x"})
	tests := []struct {
		name    string
		archive []byte
		path    string
		sha     string
		want    string
	}{
		{"wrong sha256", good, "/a.tar.gz", strings.Repeat("0", 64), "does not match"},
		{"not found", nil, "/missing.tar.gz", "", "404"},
		{"path traversal", makeTar(t, true, tarEntry{name: "../evil", body: "x"}), "/a.tar.gz", "", "outside the archive"},
		{"absolute path", makeTar(t, true, tarEntry{name: "/etc/evil", body: "x"}), "/a.tar.gz", "", "outside the archive"},
		{"write through symlink", makeTar(t, true,
			tarEntry{name: "etc", link: "/etc", typ: tar.TypeSymlink},
			tarEntry{name: "etc/evil", body: "x"}), "/a.tar.gz", "", "inside the symlink"},
		{"device", makeTar(t, true, tarEntry{name: "dev", typ: tar.TypeChar}), "/a.tar.gz", "", "unsupported type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string][]byte{}
			if tt.archive != nil {
				files[tt.path] = tt.archive
			}
			base := serve(t, files)
			f := newFetcher(t)
			_, err := f.Fetch(context.Background(), recipe.Source{URL: base + tt.path, SHA256: tt.sha})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
			if n := srcEntries(t, f); n != 0 {
				t.Errorf("failed fetch left %d store entries", n)
			}
		})
	}
}

const patchV2 = `--- a/main.c
+++ b/main.c
@@ -1 +1 @@
-int x = 1;
+int x = 2;
`

func TestFetchPatches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := newFetcher(t)
	writeFile(t, filepath.Join(f.Root, "src", "main.c"), "int x = 1;\n", 0o644)
	writeFile(t, filepath.Join(f.Root, "patches", "x2.patch"), patchV2, 0o644)

	plain, err := f.Fetch(context.Background(), recipe.Source{Path: "src"})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := f.Fetch(context.Background(), recipe.Source{Path: "src", Patches: []string{"patches/x2.patch"}})
	if err != nil {
		t.Fatalf("Fetch with patch: %v", err)
	}
	if got := readFile(t, filepath.Join(patched.Dir, "main.c")); got != "int x = 2;\n" {
		t.Errorf("patch not applied: %q", got)
	}
	if patched.Key == plain.Key {
		t.Error("patch did not change the key")
	}
	if got := readFile(t, filepath.Join(f.Root, "src", "main.c")); got != "int x = 1;\n" {
		t.Error("patch changed the project sources")
	}

	writeFile(t, filepath.Join(f.Root, "patches", "bad.patch"), strings.ReplaceAll(patchV2, "int x = 1;", "int y = 7;"), 0o644)
	before := srcEntries(t, f)
	if _, err := f.Fetch(context.Background(), recipe.Source{Path: "src", Patches: []string{"patches/bad.patch"}}); err == nil || !strings.Contains(err.Error(), "patch patches/bad.patch") {
		t.Errorf("bad patch: got %v", err)
	}
	if srcEntries(t, f) != before {
		t.Error("failed patch left a store entry")
	}
}

func TestFetchMissingSources(t *testing.T) {
	f := newFetcher(t)
	if _, err := f.Fetch(context.Background(), recipe.Source{Path: "nope"}); err == nil {
		t.Error("missing path: expected an error")
	}
	if _, err := f.Fetch(context.Background(), recipe.Source{Path: ".", Patches: []string{"nope.patch"}}); err == nil {
		t.Error("missing patch: expected an error")
	}
	if _, err := f.Fetch(context.Background(), recipe.Source{}); err == nil {
		t.Error("empty source: expected an error")
	}
}

func TestKey(t *testing.T) {
	f := newFetcher(t)
	writeFile(t, filepath.Join(f.Root, "src", "a.c"), "int a;\n", 0o644)
	writeFile(t, filepath.Join(f.Root, "fix.patch"), "patch\n", 0o644)
	src := recipe.Source{Path: "src"}

	k1, err := f.Key(src)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if r.Key != k1 {
		t.Errorf("Fetch key %s differs from Key %s", r.Key, k1)
	}
	if !f.Store.Has(store.Src, k1) {
		t.Error("fetched source is not in the store")
	}

	writeFile(t, filepath.Join(f.Root, "src", "a.c"), "int b;\n", 0o644)
	k2, _ := f.Key(src)
	patched, _ := f.Key(recipe.Source{Path: "src", Patches: []string{"fix.patch"}})
	git, _ := f.Key(recipe.Source{Git: "https://example.com/x", Rev: strings.Repeat("a", 40)})
	if len(map[string]bool{k1: true, k2: true, patched: true, git: true}) != 4 {
		t.Errorf("keys are not distinct: %s %s %s %s", k1, k2, patched, git)
	}
	if f.Store.Has(store.Src, k2) {
		t.Error("Key wrote to the store")
	}

	if _, err := f.Key(recipe.Source{}); err == nil {
		t.Error("expected an error for an empty source")
	}
}
