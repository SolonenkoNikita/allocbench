package env

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func extractRootfs(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	links := map[string]bool{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		name, err := localName(hdr.Name, links)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		path := filepath.Join(dir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeFile(path, tr, hdr.FileInfo().Mode())
		case tar.TypeSymlink:
			links[name] = true
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.Symlink(hdr.Linkname, path)
			}
		case tar.TypeLink:
			var target string
			if target, err = localName(hdr.Linkname, links); err == nil {
				if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
					err = os.Link(filepath.Join(dir, target), path)
				}
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo, tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("rootfs entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("rootfs entry %q: %w", hdr.Name, err)
		}
	}
}

func localName(name string, links map[string]bool) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." {
		return clean, nil
	}
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("rootfs entry %q is outside the rootfs", name)
	}
	for p := filepath.Dir(clean); p != "."; p = filepath.Dir(p) {
		if links[p] {
			return "", fmt.Errorf("rootfs entry %q is inside the symlink %q", name, p)
		}
	}
	return clean, nil
}

func writeFile(path string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode.Perm()|0o600)
}
