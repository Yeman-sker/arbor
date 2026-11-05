package git

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// hiddenEntry reports whether the Files view leaves out what is called name:
// Git's own directory (or file, in a linked worktree) and the folder settings
// Finder scatters around. Neither is something anyone opens to read.
func hiddenEntry(name string) bool {
	return name == ".git" || name == ".DS_Store"
}

// parentDir returns the directory of the slash separated path p, "" for the
// root.
func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// addIgnoredPath records a path from a `! ` line of `git status`. A directory
// ends in a slash, and what is in it is not listed.
func addIgnoredPath(snap *RepoSnapshot, p string) {
	// Without -z, Git quotes a path holding a quote or a control character.
	if strings.HasPrefix(p, `"`) {
		if unquoted, err := strconv.Unquote(p); err == nil {
			p = unquoted
		}
	}
	p = filepath.ToSlash(p)
	name, isDir := strings.CutSuffix(p, "/")
	if name == "" || hiddenEntry(path.Base(name)) {
		return
	}
	if snap.Ignored == nil {
		snap.Ignored = make(map[string]bool)
	}
	snap.Ignored[name] = isDir
}

// IsIgnored reports whether Git ignores rel, by itself or with a directory it
// is in.
func (s *RepoSnapshot) IsIgnored(rel string) bool {
	if len(s.Ignored) == 0 {
		return false
	}
	for p := rel; p != ""; p = parentDir(p) {
		if _, ok := s.Ignored[p]; ok {
			return true
		}
	}
	return false
}

// DirEntry is a file or directory the Files tree shows.
type DirEntry struct {
	Path  string // relative to the repository, slash separated
	IsDir bool
	// Link says it is a symbolic link. A link to a directory has IsDir set.
	Link bool
}

// ReadDir lists the directory rel of the working tree ("" for its root),
// directories first. What cannot be opened and read is left out: a socket or
// a pipe would hang whoever opens it, and Git never lists those either.
func ReadDir(root, rel string) ([]DirEntry, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if hiddenEntry(name) {
			continue
		}
		typ := e.Type()
		link := typ&fs.ModeSymlink != 0
		if link {
			// node_modules is full of links to directories under pnpm. A link
			// that leads nowhere still shows, and says so when it is opened.
			typ = 0
			if fi, err := os.Stat(filepath.Join(abs, name)); err == nil {
				typ = fi.Mode().Type()
			}
		}
		if !typ.IsDir() && !typ.IsRegular() {
			continue
		}
		p := name
		if rel != "" {
			p = rel + "/" + name
		}
		out = append(out, DirEntry{Path: p, IsDir: typ.IsDir(), Link: link})
	}
	sortDirEntries(out)
	return out, nil
}

// Children returns what the tree shows in the directory dir ("" for the root):
// what is on disk, as ReadDir read it, and what Git lists that is not there,
// which is what shows that a tracked file was deleted. Before the disk has been
// read, disk is nil and this is what Git lists.
func (s *RepoSnapshot) Children(dir string, disk []DirEntry) []DirEntry {
	listed := s.TreeRoots
	if dir != "" {
		listed = s.TreeDirs[dir]
	}
	out := make([]DirEntry, 0, len(disk)+len(listed))
	out = append(out, disk...)
	var seen map[string]bool
	if len(disk) > 0 && len(listed) > 0 {
		seen = make(map[string]bool, len(disk))
		for _, e := range disk {
			seen[e.Path] = true
		}
	}
	added := false
	for _, p := range listed {
		if seen[p] {
			continue
		}
		_, isDir := s.TreeDirs[p]
		out = append(out, DirEntry{Path: p, IsDir: isDir})
		added = true
	}
	if added && len(disk) > 0 {
		sortDirEntries(out)
	}
	return out
}

// ListIgnoredFiles returns every file Git ignores, including those in ignored
// directories, for searching them. It walks all of node_modules, so it is for
// the background and only when someone searches.
func ListIgnoredFiles(root string) ([]string, error) {
	out, err := Run(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, strings.Count(out, "\x00"))
	for _, p := range strings.Split(out, "\x00") {
		p = filepath.ToSlash(p)
		// A path ending in a slash is another repository, which Git does not
		// look into.
		if p == "" || strings.HasSuffix(p, "/") || hiddenEntry(path.Base(p)) {
			continue
		}
		files = append(files, p)
	}
	return files, nil
}

// PreviewKind says what opening a file found.
type PreviewKind uint8

const (
	// PreviewText is a file with text to show.
	PreviewText PreviewKind = iota
	// PreviewEmpty is a file with nothing in it.
	PreviewEmpty
	// PreviewBinary is a file that is not text.
	PreviewBinary
	// PreviewMissing is a path with nothing there.
	PreviewMissing
	// PreviewDirectory is a directory that Git lists as a single entry, as it
	// does a submodule.
	PreviewDirectory
	// PreviewUnreadable is a file that could not be read, for Reason.
	PreviewUnreadable
)

// previewPartBytes is how much of a file the Files view holds at a time. A file
// that fits shows whole. A larger one, as a log can be of any size, shows a part
// at a time, which it moves through.
const previewPartBytes = 16 << 20

// PlaceKind says how a PreviewPlace finds the part of a file to read.
type PlaceKind uint8

const (
	// PlaceFrom is the part that starts at Offset.
	PlaceFrom PlaceKind = iota
	// PlaceUpTo is the part that ends at Offset.
	PlaceUpTo
	// PlaceEnd is the part that ends with the file.
	PlaceEnd
)

// PreviewPlace says which part of a file to read. Its zero value is the start.
type PreviewPlace struct {
	Kind   PlaceKind
	Offset int64
}

// FilePreview is what the Files view shows for a file.
type FilePreview struct {
	Kind PreviewKind
	// Text is the text of a PreviewText file, or of the part of it that was read.
	Text string
	// Size is the size of the file, in bytes.
	Size int64
	// Start and End are the bytes of the file that Text holds.
	Start, End int64
	// Reason says why a PreviewUnreadable file could not be read.
	Reason string
}

// Partial reports whether Text holds less than the whole file.
func (p FilePreview) Partial() bool {
	return p.Start > 0 || p.End < p.Size
}

// ReadFilePreview reads the file rel of the working tree. It never blocks on
// something that is not a file, as opening a pipe would.
func ReadFilePreview(root, rel string) FilePreview {
	return ReadFilePreviewAt(root, rel, PreviewPlace{})
}

// ReadFilePreviewAt is ReadFilePreview for a part of the file. A part ends and
// starts with whole lines, so that no line shows cut in two, unless it has no line
// break to go by.
func ReadFilePreviewAt(root, rel string, place PreviewPlace) FilePreview {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if li, lerr := os.Lstat(abs); lerr == nil && li.Mode()&fs.ModeSymlink != 0 {
				return FilePreview{Kind: PreviewUnreadable, Reason: "The symbolic link points to something that doesn't exist."}
			}
			return FilePreview{Kind: PreviewMissing}
		}
		return FilePreview{Kind: PreviewUnreadable, Reason: readFailure(err)}
	}
	if fi.IsDir() {
		return FilePreview{Kind: PreviewDirectory}
	}
	if !fi.Mode().IsRegular() {
		return FilePreview{Kind: PreviewUnreadable, Reason: "It is not a regular file."}
	}
	f, err := os.Open(abs)
	if err != nil {
		return FilePreview{Kind: PreviewUnreadable, Size: fi.Size(), Reason: readFailure(err)}
	}
	defer f.Close()
	// Whether it is text goes by how the file starts, whichever part is asked for.
	head := make([]byte, binarySniffBytes)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return FilePreview{Kind: PreviewUnreadable, Size: fi.Size(), Reason: readFailure(err)}
	}
	switch {
	case n == 0:
		return FilePreview{Kind: PreviewEmpty}
	case looksBinary(head[:n]):
		return FilePreview{Kind: PreviewBinary, Size: fi.Size()}
	}

	place = place.within(fi.Size())
	start, want := previewRange(place, fi.Size())
	data, err := io.ReadAll(io.NewSectionReader(f, start, want))
	if err != nil {
		return FilePreview{Kind: PreviewUnreadable, Size: fi.Size(), Reason: readFailure(err)}
	}
	// It may have grown since it was looked at, as a log does.
	size := max(fi.Size(), start+int64(len(data)))
	end := start + int64(len(data))
	if end < size && place.Kind != PlaceUpTo {
		cut := alignPartEnd(data)
		data = data[:cut]
		end = start + int64(cut)
	}
	if start > 0 && place.Kind != PlaceFrom {
		cut := alignPartStart(data)
		data = data[cut:]
		start += int64(cut)
	}
	return FilePreview{Kind: PreviewText, Text: string(data), Size: size, Start: start, End: end}
}

// within returns the place in a file of the size: past its end, as in a log rotated
// since it was asked for, it is the end.
func (p PreviewPlace) within(size int64) PreviewPlace {
	switch {
	case p.Kind == PlaceFrom && p.Offset >= size && size > 0, p.Kind == PlaceUpTo && p.Offset > size:
		return PreviewPlace{Kind: PlaceEnd}
	}
	return p
}

// previewRange returns where the part of a file of the size starts, and how much
// of it to read.
func previewRange(place PreviewPlace, size int64) (start, want int64) {
	switch place.Kind {
	case PlaceFrom:
		return place.Offset, previewPartBytes
	case PlaceUpTo:
		start = max(0, place.Offset-previewPartBytes)
		return start, place.Offset - start
	}
	return max(0, size-previewPartBytes), previewPartBytes
}

// alignPartStart returns where the first whole line of a part starts. With no line
// break in it, it is where the first whole character does.
func alignPartStart(data []byte) int {
	if i := bytes.IndexByte(data, '\n'); i >= 0 && i+1 < len(data) {
		return i + 1
	}
	for i := 0; i < len(data) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(data[i]) {
			return i
		}
	}
	return 0
}

// alignPartEnd returns where the last whole line of a part ends. With no line break
// in it, it is where the last whole character does.
func alignPartEnd(data []byte) int {
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return i + 1
	}
	for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
		if utf8.RuneStart(data[i]) {
			if !utf8.FullRune(data[i:]) {
				return i
			}
			break
		}
	}
	return len(data)
}

// binarySniffBytes is how much of the start of a file tells whether it is text.
const binarySniffBytes = 8192

// looksBinary reports whether data, the start of a file, is not text.
func looksBinary(data []byte) bool {
	if len(data) > binarySniffBytes {
		data = data[:binarySniffBytes]
	}
	return bytes.IndexByte(data, 0) != -1
}

// readFailure puts why a file could not be read in a sentence for the user.
func readFailure(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "You don't have permission to read it."
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	msg := err.Error()
	r, n := utf8.DecodeRuneInString(msg)
	return fmt.Sprintf("%c%s.", unicode.ToUpper(r), msg[n:])
}

// sortDirEntries puts directories before files, each in natural order.
func sortDirEntries(entries []DirEntry) {
	slices.SortFunc(entries, func(a, b DirEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return compareNatural(a.Path, b.Path)
	})
}

// compareNatural orders paths as a person reads them: case does not matter
// and numbers go by their value, so README.md comes after arbor and file10
// after file2. Paths that differ only in case or in leading zeros still have
// an order, so that sorting is stable from one load to the next.
func compareNatural(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			si, sj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return cmp.Compare(len(na), len(nb))
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			continue
		}
		ra, wa := utf8.DecodeRuneInString(a[i:])
		rb, wb := utf8.DecodeRuneInString(b[j:])
		if la, lb := unicode.ToLower(ra), unicode.ToLower(rb); la != lb {
			return cmp.Compare(la, lb)
		}
		i += wa
		j += wb
	}
	if c := cmp.Compare(len(a)-i, len(b)-j); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
