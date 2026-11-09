package source

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func extractTar(r io.Reader, dir string, gz bool) error {
	if gz {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}

	links := map[string]bool{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		name := strings.TrimSuffix(filepath.Clean(hdr.Name), "/")
		if name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("archive entry %q is outside the archive", hdr.Name)
		}
		for p := filepath.Dir(name); p != "."; p = filepath.Dir(p) {
			if links[p] {
				return fmt.Errorf("archive entry %q is inside the symlink %q", hdr.Name, p)
			}
		}

		path := filepath.Join(dir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeEntry(path, tr, hdr.FileInfo().Mode())
		case tar.TypeSymlink:
			links[name] = true
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.Symlink(hdr.Linkname, path)
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("archive entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
		}
		if err != nil {
			return err
		}
	}
	return stripTopDir(dir)
}

func writeEntry(path string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, normMode(mode))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func stripTopDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return nil
	}

	top := dir + ".top"
	if err := os.Rename(filepath.Join(dir, entries[0].Name()), top); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	return os.Rename(top, dir)
}

func copyTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return walk(src, func(rel, path string, d fs.DirEntry) error {
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		return writeEntry(target, in, fi.Mode())
	})
}

func normMode(mode fs.FileMode) fs.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}
