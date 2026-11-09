package source

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const treeVersion = "allocbench-tree/v1"

func TreeHash(dir string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", treeVersion)

	err := walk(dir, func(rel, path string, d fs.DirEntry) error {
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l\x00%s\x00%s\n", rel, target)
		case d.IsDir():
			fmt.Fprintf(h, "d\x00%s\n", rel)
		default:
			sum, exec, err := fileHash(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "f\x00%s\x00%t\x00%s\n", rel, exec, sum)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func walk(dir string, fn func(rel, path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ContainsAny(rel, "\n\x00") {
			return fmt.Errorf("%s: unsupported file name", rel)
		}
		if !d.IsDir() && !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return fmt.Errorf("%s: unsupported file type %s", rel, d.Type())
		}
		return fn(filepath.ToSlash(rel), path, d)
	})
}

func fileHash(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), fi.Mode()&0o111 != 0, nil
}
