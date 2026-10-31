package main

import (
	"path"
	"sort"
	"strings"
)

// clearExplorerFile closes the file of the Files view.
func (a *app) clearExplorerFile() {
	a.selectedFile = ""
	a.codeState.Buffer.Reset("")
	a.codeState.DiffBase = nil
	a.contentSeq++
}

// reselectExplorerFile reads the open file again after the repository
// changed. When the file is gone (another branch has no such file, it was
// deleted) another one is opened in its place, so that the view never shows
// a file that is not there.
func (a *app) reselectExplorerFile(snap *RepoSnapshot) {
	if inTree(snap, a.selectedFile) {
		a.selectExplorerFile(a.selectedFile)
		return
	}
	if next := nearestFile(snap, a.selectedFile); next != "" {
		a.selectExplorerFile(next)
		return
	}
	a.clearExplorerFile()
}

func inTree(snap *RepoSnapshot, rel string) bool {
	if _, isDir := snap.TreeDirs[rel]; isDir {
		return false
	}
	i := sort.SearchStrings(snap.AllFiles, rel)
	return i < len(snap.AllFiles) && snap.AllFiles[i] == rel
}

// nearestFile picks the file to open when rel is gone: its neighbour in the
// same directory, else in the closest directory above that has files.
func nearestFile(snap *RepoSnapshot, rel string) string {
	files := snap.AllFiles
	if len(files) == 0 {
		return ""
	}
	i := sort.SearchStrings(files, rel)
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		prefix := dir + "/"
		if dir == "." || dir == "/" {
			prefix = ""
		}
		if f := closestWithPrefix(files, i, prefix); f != "" {
			return f
		}
		if prefix == "" {
			return ""
		}
	}
}

// closestWithPrefix returns the file with prefix nearest to position i of the
// sorted files, looking at the next one before the previous one.
func closestWithPrefix(files []string, i int, prefix string) string {
	for lo, hi := i-1, i; lo >= 0 || hi < len(files); lo, hi = lo-1, hi+1 {
		if hi < len(files) && strings.HasPrefix(files[hi], prefix) {
			return files[hi]
		}
		if lo >= 0 && strings.HasPrefix(files[lo], prefix) {
			return files[lo]
		}
	}
	return ""
}
