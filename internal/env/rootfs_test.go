package env

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	mode             int64
	typ              byte
}

func rootfsTar(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: mode, Linkname: e.link}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if e.typ == tar.TypeChar {
			hdr.Devmajor, hdr.Devminor = 1, 3
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			w.Write([]byte(e.body))
		}
	}
	w.Close()
	return &buf
}

func TestExtractRootfs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rootfs")
	err := extractRootfs(rootfsTar(t,
		entry{name: "./", typ: tar.TypeDir},
		entry{name: "./usr/", typ: tar.TypeDir},
		entry{name: "./usr/bin/", typ: tar.TypeDir},
		entry{name: "./usr/bin/bunzip2", body: "bz", mode: 0o755, typ: tar.TypeReg},
		entry{name: "./usr/bin/bzcat", link: "./usr/bin/bunzip2", typ: tar.TypeLink},
		entry{name: "./usr/bin/su", body: "su", mode: 0o4755, typ: tar.TypeReg},
		entry{name: "./etc/shadow", body: "root:*", mode: 0o640, typ: tar.TypeReg},
		entry{name: "./etc/readonly", body: "ro", mode: 0o444, typ: tar.TypeReg},
		entry{name: "./bin", link: "usr/bin", typ: tar.TypeSymlink},
		entry{name: "./etc/localtime", link: "/usr/share/zoneinfo/UTC", typ: tar.TypeSymlink},
		entry{name: "./dev/", typ: tar.TypeDir},
		entry{name: "./dev/null", typ: tar.TypeChar, mode: 0o666},
		entry{name: "./run/initctl", typ: tar.TypeFifo},
	), dir)
	if err != nil {
		t.Fatalf("extractRootfs: %v", err)
	}

	a, _ := os.Stat(filepath.Join(dir, "usr/bin/bunzip2"))
	b, _ := os.Stat(filepath.Join(dir, "usr/bin/bzcat"))
	if !os.SameFile(a, b) {
		t.Error("hardlink is not the same file")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "usr/bin/su")); fi.Mode()&os.ModeSetuid != 0 || fi.Mode().Perm() != 0o755 {
		t.Errorf("su mode: %v", fi.Mode())
	}
	if data, err := os.ReadFile(filepath.Join(dir, "etc/shadow")); err != nil || string(data) != "root:*" {
		t.Errorf("shadow is not readable: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "etc/readonly")); fi.Mode().Perm()&0o200 == 0 {
		t.Errorf("read-only file is not writable by the owner: %v", fi.Mode())
	}
	if link, _ := os.Readlink(filepath.Join(dir, "etc/localtime")); link != "/usr/share/zoneinfo/UTC" {
		t.Errorf("absolute symlink: %q", link)
	}
	for _, skipped := range []string{"dev/null", "run/initctl"} {
		if _, err := os.Lstat(filepath.Join(dir, skipped)); !os.IsNotExist(err) {
			t.Errorf("%s should be skipped", skipped)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("rootfs cannot be removed: %v", err)
	}
}

func TestExtractRootfsErrors(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		want    string
	}{
		{"path traversal", []entry{{name: "../evil", body: "x", typ: tar.TypeReg}}, "outside the rootfs"},
		{"absolute path", []entry{{name: "/etc/evil", body: "x", typ: tar.TypeReg}}, "outside the rootfs"},
		{"hardlink outside", []entry{{name: "./passwd", link: "../../etc/passwd", typ: tar.TypeLink}}, "outside the rootfs"},
		{"write through symlink", []entry{
			{name: "./lib", link: "/lib", typ: tar.TypeSymlink},
			{name: "./lib/evil.so", body: "x", typ: tar.TypeReg},
		}, "inside the symlink"},
		{"duplicate file", []entry{
			{name: "./a", body: "1", typ: tar.TypeReg},
			{name: "./a", body: "2", typ: tar.TypeReg},
		}, "file exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := extractRootfs(rootfsTar(t, tt.entries...), filepath.Join(t.TempDir(), "rootfs"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
