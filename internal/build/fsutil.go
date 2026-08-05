package build

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rel renders p relative to root for display. It falls back to the absolute
// path rather than an error, because this only feeds messages.
func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// copyTree copies a file or directory tree to dst, replacing whatever is there.
//
// Symlinks are not followed. Copying through a link would silently duplicate
// whatever it points at — possibly a whole node_modules, possibly something
// outside the repository — and no artifact in this organisation needs it.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dst); err != nil {
		return err
	}

	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s 是符號連結，不複製", src)
	case st.IsDir():
		return copyDir(src, dst)
	case st.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFile(src, dst, st.Mode())
	default:
		return fmt.Errorf("%s 不是普通檔案或目錄", src)
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rest, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rest)

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s 是符號連結，不複製", p)
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode())
		default:
			return fmt.Errorf("%s 不是普通檔案或目錄", p)
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirSize totals the regular files under p, or returns p's own size.
func dirSize(p string) (int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		return st.Size(), nil
	}
	var total int64
	err = filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// humanSize renders a byte count for the build log.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// withinRoot reports whether p stays inside root once both are cleaned. Paths
// come from a config file and `clean` deletes what they point at, so this is
// checked again here rather than trusted from validation alone.
func withinRoot(root, p string) bool {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
