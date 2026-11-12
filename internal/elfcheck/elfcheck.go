package elfcheck

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSymlinks   = 40
	verdefSize    = 20
	verdauxSize   = 8
	verdefFlagOff = 2
	verdefCntOff  = 6
	verdefAuxOff  = 12
	verdefNextOff = 16
	verFlagBase   = 1
	originVar     = "$ORIGIN"
)

var elfMagic = []byte(elf.ELFMAG)

type Problem struct {
	File    string
	Message string
}

func (p Problem) String() string {
	return p.File + ": " + p.Message
}

type Checker struct {
	Root string

	versions map[string]map[string]bool
}

func (c *Checker) CheckDir(dir string) ([]Problem, error) {
	var problems []Problem
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		found, err := c.CheckFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		for _, pr := range found {
			pr.File = filepath.ToSlash(rel)
			problems = append(problems, pr)
		}
		return nil
	})
	return problems, err
}

func (c *Checker) CheckFile(name string) ([]Problem, error) {
	ok, err := isELF(name)
	if err != nil || !ok {
		return nil, err
	}
	f, err := elf.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return nil, nil
	}

	libs, err := f.ImportedLibraries()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	imported, err := f.ImportedSymbols()
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	needs := map[string]map[string]bool{}
	for _, sym := range imported {
		if sym.Version == "" || sym.Library == "" {
			continue
		}
		if needs[sym.Library] == nil {
			needs[sym.Library] = map[string]bool{}
		}
		needs[sym.Library][sym.Version] = true
	}

	dirs, err := c.searchDirs(f, name)
	if err != nil {
		return nil, err
	}
	var problems []Problem
	for _, lib := range libs {
		found, err := c.find(lib, dirs)
		if err != nil {
			return nil, err
		}
		if found == "" {
			problems = append(problems, Problem{name, fmt.Sprintf("needs %s, not in the run environment", lib)})
			continue
		}
		if len(needs[lib]) == 0 {
			continue
		}
		have, err := c.defined(found)
		if err != nil {
			return nil, err
		}
		var missing []string
		for v := range needs[lib] {
			if !have[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			problems = append(problems, Problem{name, fmt.Sprintf("needs %s from %s, not in the run environment", strings.Join(missing, ", "), lib)})
		}
	}
	return problems, nil
}

func isELF(name string) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(elfMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(head, elfMagic), nil
}

func (c *Checker) searchDirs(f *elf.File, name string) ([]string, error) {
	var dirs []string
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		values, err := f.DynString(tag)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, v := range values {
			for _, d := range strings.Split(v, ":") {
				switch {
				case strings.HasPrefix(d, originVar):
					dirs = append(dirs, "host:"+filepath.Join(filepath.Dir(name), strings.TrimPrefix(d, originVar)))
				case path.IsAbs(d):
					dirs = append(dirs, d)
				}
			}
		}
	}
	if arch := multiarch(f.Machine); arch != "" {
		dirs = append(dirs, "/lib/"+arch, "/usr/lib/"+arch)
	}
	return append(dirs, "/lib64", "/usr/lib64", "/lib", "/usr/lib"), nil
}

func multiarch(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "x86_64-linux-gnu"
	case elf.EM_AARCH64:
		return "aarch64-linux-gnu"
	case elf.EM_386:
		return "i386-linux-gnu"
	}
	return ""
}

func (c *Checker) find(lib string, dirs []string) (string, error) {
	if strings.Contains(lib, "/") {
		return c.resolve(lib)
	}
	for _, d := range dirs {
		if host, ok := strings.CutPrefix(d, "host:"); ok {
			p := filepath.Join(host, lib)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return p, nil
			}
			continue
		}
		p, err := c.resolve(path.Join(d, lib))
		if err != nil {
			return "", err
		}
		if p != "" {
			return p, nil
		}
	}
	return "", nil
}

func (c *Checker) resolve(name string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(path.Clean("/"+name), "/"), "/")
	cur := "/"
	for hops := 0; len(parts) > 0; {
		next := path.Join(cur, parts[0])
		parts = parts[1:]
		fi, err := os.Lstat(filepath.Join(c.Root, next))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			if len(parts) > 0 && !fi.IsDir() {
				return "", nil
			}
			cur = next
			continue
		}
		if hops++; hops > maxSymlinks {
			return "", nil
		}
		target, err := os.Readlink(filepath.Join(c.Root, next))
		if err != nil {
			return "", err
		}
		if !path.IsAbs(target) {
			target = path.Join(cur, target)
		}
		parts = append(strings.Split(strings.TrimPrefix(path.Clean(target), "/"), "/"), parts...)
		cur = "/"
	}
	fi, err := os.Stat(filepath.Join(c.Root, cur))
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil
	}
	return filepath.Join(c.Root, cur), nil
}

func (c *Checker) defined(lib string) (map[string]bool, error) {
	if v, ok := c.versions[lib]; ok {
		return v, nil
	}
	f, err := elf.Open(lib)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lib, err)
	}
	defer f.Close()
	v, err := Versions(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lib, err)
	}
	if c.versions == nil {
		c.versions = map[string]map[string]bool{}
	}
	c.versions[lib] = v
	return v, nil
}

func Versions(f *elf.File) (map[string]bool, error) {
	out := map[string]bool{}
	sec := f.SectionByType(elf.SHT_GNU_VERDEF)
	if sec == nil {
		return out, nil
	}
	if int(sec.Link) >= len(f.Sections) {
		return nil, errors.New("version definitions link to a missing string table")
	}
	data, err := sec.Data()
	if err != nil {
		return nil, err
	}
	strs, err := f.Sections[sec.Link].Data()
	if err != nil {
		return nil, err
	}

	order := f.ByteOrder
	for off := 0; ; {
		if off < 0 || off+verdefSize > len(data) {
			return nil, errors.New("truncated version definition")
		}
		flags := order.Uint16(data[off+verdefFlagOff:])
		cnt := order.Uint16(data[off+verdefCntOff:])
		aux := off + int(order.Uint32(data[off+verdefAuxOff:]))
		next := int(order.Uint32(data[off+verdefNextOff:]))
		if cnt > 0 && flags&verFlagBase == 0 {
			if aux < 0 || aux+verdauxSize > len(data) {
				return nil, errors.New("truncated version definition")
			}
			name, ok := cstring(strs, int(order.Uint32(data[aux:])))
			if !ok {
				return nil, errors.New("bad version name")
			}
			out[name] = true
		}
		if next == 0 {
			return out, nil
		}
		off += next
	}
}

func cstring(data []byte, off int) (string, bool) {
	if off < 0 || off >= len(data) {
		return "", false
	}
	end := bytes.IndexByte(data[off:], 0)
	if end < 0 {
		return "", false
	}
	return string(data[off : off+end]), true
}
