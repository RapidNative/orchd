package template

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// alwaysSkip are never included in a base bundle / materialized copy — deps and
// VCS metadata are regenerated (npm install) or irrelevant.
var alwaysSkip = []string{"node_modules", ".git"}

func skipSet(extra []string) map[string]bool {
	s := map[string]bool{}
	for _, e := range append(append([]string{}, alwaysSkip...), extra...) {
		s[e] = true
	}
	return s
}

// safeJoin joins rel onto root, refusing paths that escape root. Returns "" if
// the result would traverse outside.
func safeJoin(root, rel string) string {
	p := filepath.Join(root, filepath.Clean("/"+rel))
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return ""
	}
	return p
}

// Materialize builds a workload's working tree: copy the base (minus skipped
// dirs), then apply the delta (write files, remove tombstones). This is the
// base + delta the project runs from.
func Materialize(baseDir, destDir string, exclude []string, delta map[string]string, deleted []string) error {
	if err := copyTree(baseDir, destDir, skipSet(exclude)); err != nil {
		return err
	}
	for path, content := range delta {
		p := safeJoin(destDir, path)
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, path := range deleted {
		if p := safeJoin(destDir, path); p != "" {
			_ = os.RemoveAll(p)
		}
	}
	return nil
}

// ListFiles returns the base's file paths (relative, sorted), skipping excluded
// dirs — a browsable manifest of the template.
func ListFiles(baseDir string, exclude []string) ([]string, error) {
	skip := skipSet(exclude)
	var out []string
	err := filepath.WalkDir(baseDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != baseDir && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(baseDir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// ReadFile reads a single base file (path-traversal safe).
func ReadFile(baseDir, rel string) ([]byte, error) {
	p := safeJoin(baseDir, rel)
	if p == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(p)
}

// WriteFile writes a file under baseDir (creating parents; path-traversal safe).
func WriteFile(baseDir, rel string, content []byte) error {
	p := safeJoin(baseDir, rel)
	if p == "" {
		return os.ErrPermission
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0o644)
}

// DeleteFile removes a file/dir under baseDir (path-traversal safe).
func DeleteFile(baseDir, rel string) error {
	p := safeJoin(baseDir, rel)
	if p == "" {
		return os.ErrPermission
	}
	return os.RemoveAll(p)
}

// Bundle writes a tar.gz of the base (minus excluded dirs) — for the browser to
// hydrate its VFS, or ORCHD to seed a workload.
func Bundle(baseDir string, exclude []string, w io.Writer) error {
	skip := skipSet(exclude)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(baseDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != baseDir && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(baseDir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// copyTree recursively copies src to dst, skipping directories named in skip.
func copyTree(src, dst string, skip map[string]bool) error {
	return filepath.WalkDir(src, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if de.IsDir() {
			if rel != "." && skip[de.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if skip[de.Name()] {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		info, _ := in.Stat()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
