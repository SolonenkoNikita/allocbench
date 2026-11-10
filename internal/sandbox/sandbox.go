package sandbox

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type Mount struct {
	Src string
	Dst string
	RW  bool
}

type Spec struct {
	Rootfs string
	Mounts []Mount
	Tmpfs  []string
	Env    map[string]string
	Dir    string
	Cmd    []string
}

var (
	rootfsDirs = []string{"usr", "etc", "bin", "sbin", "lib", "lib32", "lib64", "libx32"}
	defaultEnv = map[string]string{
		"PATH":   "/usr/local/bin:/usr/bin:/bin",
		"HOME":   "/tmp",
		"LC_ALL": "C.UTF-8",
		"TZ":     "UTC",
	}
)

func Args(s Spec) ([]string, error) {
	if s.Rootfs == "" {
		return nil, fmt.Errorf("rootfs is required")
	}
	if len(s.Cmd) == 0 || !path.IsAbs(s.Cmd[0]) {
		return nil, fmt.Errorf("command must start with an absolute path, got %q", s.Cmd)
	}

	args := []string{"--unshare-all", "--die-with-parent", "--new-session", "--clearenv"}

	for _, name := range rootfsDirs {
		src := filepath.Join(s.Rootfs, name)
		fi, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(src)
			if err != nil {
				return nil, err
			}
			args = append(args, "--symlink", target, "/"+name)
		case fi.IsDir():
			args = append(args, "--ro-bind", src, "/"+name)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Rootfs, "usr")); err != nil {
		return nil, fmt.Errorf("rootfs %s has no usr: %w", s.Rootfs, err)
	}

	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var", "--tmpfs", "/run")

	for _, dst := range s.Tmpfs {
		if err := checkDst(dst); err != nil {
			return nil, err
		}
		args = append(args, "--tmpfs", dst)
	}
	for _, m := range s.Mounts {
		if err := checkDst(m.Dst); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(m.Src) {
			return nil, fmt.Errorf("mount source %q must be an absolute path", m.Src)
		}
		flag := "--ro-bind"
		if m.RW {
			flag = "--bind"
		}
		args = append(args, flag, m.Src, m.Dst)
	}

	env := map[string]string{}
	for k, v := range defaultEnv {
		env[k] = v
	}
	for k, v := range s.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--setenv", k, env[k])
	}

	dir := s.Dir
	if dir == "" {
		dir = "/"
	}
	args = append(args, "--hostname", "sandbox", "--chdir", dir)
	return append(args, s.Cmd...), nil
}

func checkDst(dst string) error {
	if !path.IsAbs(dst) || path.Clean(dst) != dst {
		return fmt.Errorf("mount point %q must be a clean absolute path", dst)
	}
	if dst == "/" {
		return fmt.Errorf("mount point %q is reserved", dst)
	}
	for _, d := range append(rootfsDirs, "proc", "dev") {
		if dst == "/"+d || strings.HasPrefix(dst, "/"+d+"/") {
			return fmt.Errorf("mount point %q is inside the read-only rootfs", dst)
		}
	}
	return nil
}
