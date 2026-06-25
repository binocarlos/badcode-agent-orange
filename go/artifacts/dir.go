package artifacts

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DirEntry is one regular file inside a directory artifact.
type DirEntry struct {
	RelPath string `json:"relPath"` // forward-slash, relative to the dir root, no leading "./"
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// WriteTarToBlobs reads a tar stream and invokes write(relPath, reader) for each
// regular file, returning a manifest sorted by RelPath. Leading "./" is stripped.
// Directory, symlink, and other non-regular entries are skipped. The reader passed
// to write yields the file's bytes; its SHA-256 and size are recorded in the manifest.
func WriteTarToBlobs(ctx context.Context, tarStream io.Reader, write func(relPath string, r io.Reader) error) ([]DirEntry, error) {
	tr := tar.NewReader(tarStream)
	var entries []DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("artifacts: read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := strings.TrimPrefix(hdr.Name, "./")
		if rel == "" || hasDotDot(rel) {
			continue // skip empty / path-traversal entries
		}
		h := sha256.New()
		size, err := writeAndHash(rel, tr, h, write)
		if err != nil {
			return nil, err
		}
		entries = append(entries, DirEntry{RelPath: rel, Size: size, SHA256: fmt.Sprintf("%x", h.Sum(nil))})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries, nil
}

// writeAndHash tees the entry bytes through the hasher while handing them to write,
// returning the number of bytes seen.
func writeAndHash(rel string, src io.Reader, h io.Writer, write func(string, io.Reader) error) (int64, error) {
	counter := &countingReader{r: io.TeeReader(src, h)}
	if err := write(rel, counter); err != nil {
		return 0, fmt.Errorf("artifacts: write blob %q: %w", rel, err)
	}
	// Drain anything write() didn't read so the hash + size cover the whole file.
	if _, err := io.Copy(io.Discard, counter); err != nil {
		return 0, fmt.Errorf("artifacts: drain blob %q: %w", rel, err)
	}
	return counter.n, nil
}

// hasDotDot reports whether any "/"-separated component of p is exactly "..",
// catching path-traversal entries without rejecting legitimate names that merely
// contain ".." (e.g. "files/..hidden.txt").
func hasDotDot(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// DirDigest returns a stable content digest over a manifest, independent of input
// order — sha256 of sorted "relPath\x00sha256\n" lines.
func DirDigest(entries []DirEntry) string {
	cp := make([]DirEntry, len(entries))
	copy(cp, entries)
	sort.Slice(cp, func(i, j int) bool { return cp[i].RelPath < cp[j].RelPath })
	h := sha256.New()
	for _, e := range cp {
		fmt.Fprintf(h, "%s\x00%s\n", e.RelPath, e.SHA256)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
