package build

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/hoshivel/hoshi-build/internal/config"
)

// archiveExt is the filename suffix for a format.
func archiveExt(format string) string {
	switch format {
	case config.ArchiveZip:
		return ".zip"
	case config.ArchiveTarGz:
		return ".tar.gz"
	default:
		return ""
	}
}

// makeArchive packs src (a file or a directory) into dst.
//
// Entries are stored under a single top-level directory named after the
// artifact, so unpacking never scatters files into the current directory.
//
// exe is the absolute path of the program binary inside src, empty when there
// is none. **Its mode is forced to 0o755 rather than copied off disk**, and
// that is the whole point: NTFS has no executable bit, so `os.Stat` on Windows
// reports 0666 for every regular file. Copying that mode into the archive
// produces an artifact that unpacks non-executable on the node — a build that
// succeeds, an archive that exists, and a binary that will not run. The
// failure surfaces at deploy time, on a different machine, with nothing
// pointing back at the machine it was built on.
//
// The organisation builds on Windows and deploys to Debian, so this is the
// normal path, not an edge case. Caught 2026-08-22 by the Windows job — a
// cross-compile check cannot see it, because cross-compiling produces the same
// wrong mode.
func makeArchive(format, src, dst, prefix, exe string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	switch format {
	case config.ArchiveZip:
		err = writeZip(f, src, prefix, exe)
	case config.ArchiveTarGz:
		err = writeTarGz(f, src, prefix, exe)
	default:
		err = fmt.Errorf("不認得的封裝格式 %q", format)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// archiveEntry is one file to store.
type archiveEntry struct {
	src  string      // absolute source path
	name string      // slash-separated name inside the archive
	mode os.FileMode //
	size int64
}

// entryMode is the mode to store for one source file. The program binary is
// executable by construction, so its mode comes from that fact rather than
// from the filesystem it happens to be sitting on.
func entryMode(path, exe string, info os.FileInfo) os.FileMode {
	if exe != "" && path == exe {
		return 0o755
	}
	return info.Mode()
}

func collectEntries(src, prefix, exe string) ([]archiveEntry, error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []archiveEntry{{
			src:  src,
			name: path.Join(prefix, filepath.Base(src)),
			mode: entryMode(src, exe, st),
			size: st.Size(),
		}}, nil
	}

	var out []archiveEntry
	err = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rest, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out = append(out, archiveEntry{
			src:  p,
			name: path.Join(prefix, filepath.ToSlash(rest)),
			mode: entryMode(p, exe, info),
			size: info.Size(),
		})
		return nil
	})
	return out, err
}

func writeZip(w io.Writer, src, prefix, exe string) error {
	entries, err := collectEntries(src, prefix, exe)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		// The executable bit has to survive: an artifact that unpacks
		// non-executable is not an artifact. See makeArchive for why the
		// binary's mode does not come from the filesystem.
		hdr.SetMode(e.mode.Perm())

		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if err := copyInto(fw, e.src); err != nil {
			return err
		}
	}
	return zw.Close()
}

func writeTarGz(w io.Writer, src, prefix, exe string) error {
	entries, err := collectEntries(src, prefix, exe)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     int64(e.mode.Perm()),
			Size:     e.size,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if err := copyInto(tw, e.src); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func copyInto(w io.Writer, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
