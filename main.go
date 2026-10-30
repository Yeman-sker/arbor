package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	modeFiles = 0
	modeGit   = 1

	headerHeight = 44
)

type diffTargetKind int

const (
	diffNone diffTargetKind = iota
	diffWorking
	diffCommitFile
)

type app struct {
	win      *mygo.Window
	repoDir  string
	repo     *RepoSnapshot
	repoErr  string
	busyText string

	// Global UI state
	activeMode  int // 0 = Files, 1 = Git
	sidebarW    float32
	newBranchOpen bool
	newBranchName string

	// Mode 0: Files (Explorer) state
	openDirs      map[string]bool
	explorerList  ui.ListState
	explorerQuery string
	onlyChanged   bool
	selectedFile  string
	codeState     *editor.State
	codeSoftWrap  bool

	// Mode 1: Git (Source Control + Graph) state
	gitList         ui.ListState
	commitMsg       string
	changesOpen     bool
	commitsOpen     bool
	expandedCommit  string // Hash of the commit expanded in the sidebar
	selectedCommit  *Commit
	commitFiles     []CommitFile
	selectedCommitF int

	// Right pane Diff Viewer state (strictly read-only)
	diffKind      diffTargetKind
	diffTitle     string
	diffBadge     string
	diffFileStat  *FileStatus
	diffState     *editor.DiffState
	diffSplit     bool // true = Split, false = Unified
	diffWrap      bool
	diffExpandAll bool
}

func newApp(initialDir string) *app {
	if initialDir == "" {
		if wd, err := os.Getwd(); err == nil {
			initialDir = wd
		} else {
			initialDir = "."
		}
	}
	codeBuf := editor.NewBuffer("")
	oldBuf := editor.NewBuffer("")
	newBuf := editor.NewBuffer("")
	a := &app{
		repoDir:         initialDir,
		activeMode:      modeFiles,
		sidebarW:        280,
		openDirs:        make(map[string]bool),
		changesOpen:     true,
		commitsOpen:     true,
		selectedCommitF: -1,
		diffSplit:       true,
		codeState: &editor.State{
			Buffer:          codeBuf,
			ReadOnly:        true,
			NoAutoClose:     true,
			CursorShape:     editor.CursorBar,
			LineHeight:      1.55,
			NoScrollPastEnd: true,
		},
		diffState: &editor.DiffState{
			Old:        oldBuf,
			New:        newBuf,
			Editable:   false,
			Layout:     editor.DiffSplit,
			Inline:     editor.DiffInlineWordAlt,
			Indicators: editor.DiffBars,
			LineHeight: 1.5,
			RenderSeparator: func(c *ui.Context, sep ui.Element, s *editor.DiffSeparator) {
				t := c.Theme()
				// PassThrough on sep, bar, and label ensures trackpad/wheel scrolling
				// never gets intercepted when a separator scrolls under the cursor.
				sep.Padding(4).PassThrough()
				sep.Children(func() {
					bar := ui.Row(c).Grow(1).AlignSelf(ui.Stretch).AlignItems(ui.Center).Gap(6).Padding(0, 8).
						Radius(5).Background(t.SurfaceHover.Alpha(0.75)).PassThrough()
					bar.Children(func() {
						if s.Lines == 0 {
							ui.Text(c, s.Header).Font("monospace").FontSize(11).TextColor(t.TextMuted).SingleLine().PassThrough()
							return
						}
						btn := ui.ButtonBase(c).Key("expand").Size(20, 20).Radius(4).Center().
							Cursor(ui.CursorPointer).Label(fmt.Sprintf("Expand %d unmodified lines", s.Lines)).
							Tooltip(fmt.Sprintf("Expand %d unmodified lines", s.Lines))
						if btn.Hovered() {
							btn.Background(t.SurfacePressed).TextColor(t.Text)
						} else {
							btn.TextColor(t.TextMuted)
						}
						btn.Children(func() {
							ui.Icon(c, IconExpand).Size(12, 12)
						})
						if btn.Clicked() {
							if c.Modifiers()&ui.Shift != 0 || s.Lines <= s.Step {
								s.ExpandAll()
							} else if s.First {
								s.ExpandUp()
							} else {
								s.ExpandDown()
							}
						}
						ui.Textf(c, "%d unmodified lines", s.Lines).FontSize(11).TextColor(t.TextMuted).SingleLine().PassThrough()
					})
				})
			},
		},
	}
	a.refreshRepo()
	return a
}

func (a *app) refreshRepo() {
	snap, err := LoadRepo(a.repoDir)
	if err != nil {
		a.repo = nil
		a.repoErr = err.Error()
		return
	}
	a.repo = snap
	a.repoDir = snap.Root
	a.repoErr = ""

	// Expand first top-level directory by default on initial load
	if len(a.openDirs) == 0 {
		for _, r := range snap.TreeRoots {
			if _, isDir := snap.TreeDirs[r]; isDir {
				a.openDirs[r] = true
				break
			}
		}
	}

	// Ensure a file is open in Files mode
	if a.selectedFile == "" && len(snap.AllFiles) > 0 {
		pick := snap.AllFiles[0]
		for _, f := range snap.AllFiles {
			if strings.HasSuffix(f, "main.go") || strings.HasSuffix(f, ".go") {
				pick = f
				break
			}
		}
		a.selectExplorerFile(pick)
	} else if a.selectedFile != "" {
		a.selectExplorerFile(a.selectedFile)
	}

	// Ensure a diff target is ready in Git mode
	if a.diffKind == diffNone {
		if len(snap.Unstaged) > 0 {
			a.selectWorkingFile(snap.Unstaged[0])
		} else if len(snap.Staged) > 0 {
			a.selectWorkingFile(snap.Staged[0])
		} else if len(snap.Commits) > 0 {
			a.toggleCommit(0)
		}
	} else if a.diffKind == diffWorking && a.diffFileStat != nil {
		// Re-sync working file or fall back if it was committed/discarded
		found := false
		for _, st := range snap.Staged {
			if st.Path == a.diffFileStat.Path && a.diffFileStat.Staged {
				a.selectWorkingFile(st)
				found = true
				break
			}
		}
		if !found {
			for _, st := range snap.Unstaged {
				if st.Path == a.diffFileStat.Path {
					a.selectWorkingFile(st)
					found = true
					break
				}
			}
		}
		if !found {
			a.diffKind = diffNone
			if len(snap.Unstaged) > 0 {
				a.selectWorkingFile(snap.Unstaged[0])
			} else if len(snap.Staged) > 0 {
				a.selectWorkingFile(snap.Staged[0])
			} else if len(snap.Commits) > 0 {
				a.toggleCommit(0)
			}
		}
	}
}

func (a *app) runAsyncGit(label string, fn func(root string) error) {
	if a.repo == nil {
		return
	}
	root := a.repo.Root
	a.busyText = label
	if a.win == nil {
		_ = fn(root)
		a.busyText = ""
		a.refreshRepo()
		return
	}
	go func() {
		_ = fn(root)
		a.win.Update(func() {
			a.busyText = ""
			a.refreshRepo()
		})
	}()
}

func (a *app) selectExplorerFile(relPath string) {
	if a.repo == nil || relPath == "" {
		return
	}
	if _, isDir := a.repo.TreeDirs[relPath]; isDir {
		return
	}
	a.selectedFile = relPath
	content := ReadWorktreeFile(a.repo.Root, relPath)
	lang := editor.LanguageFor(relPath)
	a.codeState.Buffer.SetLanguage(lang)
	a.codeState.Buffer.Reset(content)
	if headText := ReadFileAtRef(a.repo.Root, "HEAD", relPath); headText != "" {
		a.codeState.DiffBase = editor.NewBuffer(headText)
	} else {
		a.codeState.DiffBase = nil
	}
}

func (a *app) selectWorkingFile(st FileStatus) {
	if a.repo == nil {
		return
	}
	stCopy := st
	a.diffKind = diffWorking
	a.diffFileStat = &stCopy
	a.diffTitle = st.Path
	a.diffBadge = st.Badge()
	oldText, newText := LoadWorkingDiffTexts(a.repo.Root, st)
	a.setDiffBuffers(st.Path, oldText, newText)
}

func (a *app) toggleCommit(idx int) {
	if a.repo == nil || idx < 0 || idx >= len(a.repo.Commits) {
		return
	}
	cm := a.repo.Commits[idx]
	if a.expandedCommit == cm.Hash {
		a.expandedCommit = ""
		return
	}
	a.expandedCommit = cm.Hash
	a.selectedCommit = &cm
	_, files, err := LoadCommitDetails(a.repo.Root, cm.Hash)
	if err != nil {
		a.commitFiles = nil
		return
	}
	a.commitFiles = files
	if len(files) > 0 {
		a.selectCommitFile(0)
	}
}

func (a *app) selectCommitFile(fIdx int) {
	if a.repo == nil || a.selectedCommit == nil || fIdx < 0 || fIdx >= len(a.commitFiles) {
		return
	}
	a.selectedCommitF = fIdx
	cf := a.commitFiles[fIdx]
	a.diffKind = diffCommitFile
	a.diffFileStat = nil
	a.diffTitle = cf.Path
	a.diffBadge = cf.Status
	oldText, newText := LoadCommitFileDiffTexts(a.repo.Root, a.selectedCommit.Hash, cf)
	a.setDiffBuffers(cf.Path, oldText, newText)
}

func (a *app) setDiffBuffers(path, oldText, newText string) {
	lang := editor.LanguageFor(path)
	a.diffState.Old.SetLanguage(lang)
	a.diffState.New.SetLanguage(lang)
	a.diffState.Old.Reset(oldText)
	a.diffState.New.Reset(newText)
}

func applyLinearTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	t.Spacing = 3
	t.Radius = 6
	t.FontSize = 13
	t.ScrollbarWidth = 5

	if t.Dark {
		t.Background = ui.Hex("#0B0C0E")
		t.Surface = ui.Hex("#111318")
		t.SurfaceHover = ui.RGBA(255, 255, 255, 0.055)
		t.SurfacePressed = ui.RGBA(255, 255, 255, 0.095)
		t.Border = ui.RGBA(255, 255, 255, 0.075)
		t.Text = ui.Hex("#ECECEE")
		t.TextMuted = ui.Hex("#7D8290")
		t.Accent = ui.Hex("#5E6AD2")
		t.AccentHover = ui.Hex("#6E7AE2")
		t.AccentPressed = ui.Hex("#4E5AC0")
		t.AccentText = ui.Hex("#FFFFFF")
		t.Success = ui.Hex("#4CB782")
		t.Warning = ui.Hex("#F2994A")
		t.Danger = ui.Hex("#EB5757")
		t.Selection = ui.RGBA(94, 106, 210, 0.24)
		t.Focus = ui.Hex("#5E6AD2")
	} else {
		t.Background = ui.Hex("#FFFFFF")
		t.Surface = ui.Hex("#F7F8FA")
		t.SurfaceHover = ui.RGBA(15, 23, 42, 0.05)
		t.SurfacePressed = ui.RGBA(15, 23, 42, 0.085)
		t.Border = ui.RGBA(15, 23, 42, 0.08)
		t.Text = ui.Hex("#16181D")
		t.TextMuted = ui.Hex("#6C7180")
		t.Accent = ui.Hex("#5E6AD2")
		t.AccentHover = ui.Hex("#4E5AC0")
		t.AccentPressed = ui.Hex("#414CA8")
		t.AccentText = ui.Hex("#FFFFFF")
		t.Success = ui.Hex("#2E9B68")
		t.Warning = ui.Hex("#D97706")
		t.Danger = ui.Hex("#DC2626")
		t.Selection = ui.RGBA(94, 106, 210, 0.16)
		t.Focus = ui.Hex("#5E6AD2")
	}
	c.SetTheme(&t)
	return &t
}

func statusColor(t *ui.Theme, badge string) ui.Color {
	switch badge {
	case "A", "U":
		return t.Success
	case "D":
		return t.Danger
	case "M", "R":
		return t.Warning
	default:
		return t.TextMuted
	}
}

func (a *app) view(c *ui.Context) {
	t := applyLinearTheme(c)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}

	// Global keyboard shortcuts
	if c.Shortcut(ui.Cmd, ui.Key1) {
		a.activeMode = modeFiles
	}
	if c.Shortcut(ui.Cmd, ui.Key2) {
		a.activeMode = modeGit
	}
	if c.Shortcut(ui.Cmd, ui.KeyR) {
		a.refreshRepo()
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		if a.activeMode == modeFiles {
			a.codeState.Run(editor.Find)
		} else {
			a.diffState.Run(editor.Find)
		}
	}

	ui.Split(c, &a.sidebarW, func() {
		a.sidebarView(c, t)
	}, func() {
		a.mainPaneView(c, t)
	}).Fill()

	a.modalsView(c, t)
}

// ============================================================================
// Left Sidebar: Mode Switcher + Repo/Branch Bar + Files Tree or Git/Graph List
// ============================================================================

func (a *app) sidebarView(c *ui.Context, t *ui.Theme) {
	side := ui.Column(c).Fill()
	if !c.Vibrancy() {
		side.Background(t.Surface)
	}
	side.Children(func() {
		bar := c.TitleBar()
		leftInset := max(bar.Left+8, 12)

		// 1. Top Inset Title Bar: Mode Switcher (Files | Git) + Refresh
		ui.Row(c).Height(headerHeight).FillWidth().Padding(0, 8, 0, leftInset).Gap(4).
			AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {

			// Segmented pill switcher
			ui.Row(c).Padding(2).Radius(6).Background(t.Background.Alpha(0.55)).Border(1, t.Border).Gap(2).Children(func() {
				if ghostBtn(c, "tab-files", IconFiles, "Files", "Explorer (⌘1)", a.activeMode == modeFiles) {
					a.activeMode = modeFiles
				}
				gitLabel := "Git"
				if a.repo != nil {
					if n := len(a.repo.Staged) + len(a.repo.Unstaged); n > 0 {
						gitLabel = fmt.Sprintf("Git · %d", n)
					}
				}
				if ghostBtn(c, "tab-git", IconGitBranch, gitLabel, "Source Control & Graph (⌘2)", a.activeMode == modeGit) {
					a.activeMode = modeGit
				}
			})

			ui.Spacer(c)

			if iconBtn(c, "btn-refresh", IconRefresh, "Refresh (⌘R)", false) {
				a.refreshRepo()
			}
		})

		if a.repo == nil {
			ui.Column(c).Grow(1).Center().Padding(16).Gap(10).Children(func() {
				ui.Text(c, "No Git repository").TextColor(t.TextMuted)
				if primaryActionBtn(c, "open-repo-empty", IconFolder, "Open Folder…", false) {
					a.openRepoDialog()
				}
			})
			return
		}

		// 2. Compact Repository & Branch Bar
		ui.Row(c).Height(34).FillWidth().Padding(0, 8).Gap(4).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {

			repoName := filepath.Base(a.repo.Root)
			if ghostBtn(c, "btn-repo", IconFolder, repoName, a.repo.Root+" (Click to switch repository)", false) {
				a.openRepoDialog()
			}

			ghostMenuBtn(c, "btn-branch", IconGitBranch, a.repo.Branch, "Switch or create branch", func(m *ui.Menu) {
				if m.Item("+ New Branch…").Chosen() {
					a.newBranchName = ""
					a.newBranchOpen = true
				}
				m.Separator()
				for _, br := range a.repo.Branches {
					if m.Item(br).Radio(br == a.repo.Branch).Chosen() && br != a.repo.Branch {
						target := br
						a.runAsyncGit("Checkout "+target, func(root string) error {
							return GitCheckoutBranch(root, target, false)
						})
					}
				}
			})

			ui.Spacer(c)

			if iconBtn(c, "btn-pull", IconArrowDown, "Pull (git pull --rebase)", false) {
				a.runAsyncGit("Pull", GitPull)
			}
			if iconBtn(c, "btn-push", IconArrowUp, "Push (git push)", false) {
				a.runAsyncGit("Push", GitPush)
			}
		})

		// 3. Mode Body
		if a.activeMode == modeFiles {
			a.filesSidebarBody(c, t)
		} else {
			a.gitSidebarBody(c, t)
		}
	})
}

type flatTreeNode struct {
	path  string
	name  string
	depth int
	isDir bool
	open  bool
	badge string
}

func (a *app) buildFlatTree(query string) []flatTreeNode {
	var out []flatTreeNode
	if query != "" || a.onlyChanged {
		for _, f := range a.repo.AllFiles {
			badge := a.repo.StatusByRel[f]
			if a.onlyChanged && badge == "" {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(f), query) {
				continue
			}
			out = append(out, flatTreeNode{
				path:  f,
				name:  f,
				depth: 0,
				isDir: false,
				badge: badge,
			})
		}
		return out
	}

	var walk func(items []string, depth int)
	walk = func(items []string, depth int) {
		for _, p := range items {
			kids, isDir := a.repo.TreeDirs[p]
			open := isDir && a.openDirs[p]
			out = append(out, flatTreeNode{
				path:  p,
				name:  filepath.Base(p),
				depth: depth,
				isDir: isDir,
				open:  open,
				badge: a.repo.StatusByRel[p],
			})
			if isDir && open {
				walk(kids, depth+1)
			}
		}
	}
	walk(a.repo.TreeRoots, 0)
	return out
}

func (a *app) filesSidebarBody(c *ui.Context, t *ui.Theme) {
	// Search & Filter bar
	ui.Row(c).Padding(6, 8).Gap(4).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		compactInput(c, "search-files", IconSearch, &a.explorerQuery, "Search files…")
		if iconBtn(c, "btn-filter-mod", IconFilter, "Show modified files only", a.onlyChanged) {
			a.onlyChanged = !a.onlyChanged
		}
	})

	q := strings.ToLower(strings.TrimSpace(a.explorerQuery))
	nodes := a.buildFlatTree(q)

	a.explorerList.Key = func(i int) any { return nodes[i].path }
	ui.List(c, &a.explorerList, len(nodes), func(i int) {
		n := nodes[i]
		selected := !n.isDir && n.path == a.selectedFile
		row := ui.Row(c.Key("tree:" + n.path)).Height(24).
			Padding(0, 8, 0, float32(6+n.depth*14)).Gap(6).Radius(5).
			AlignItems(ui.Center).Cursor(ui.CursorPointer)

		if selected {
			row.Background(t.Accent.Alpha(0.10)).TextColor(t.Text)
		} else if row.Hovered() {
			row.Background(t.SurfaceHover)
		}

		if row.Clicked() {
			if n.isDir {
				a.openDirs[n.path] = !a.openDirs[n.path]
			} else {
				a.selectExplorerFile(n.path)
			}
		}

		row.Children(func() {
			if n.isDir {
				chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(t.TextMuted)
				if n.open {
					chev.Rotate(90)
				}
				folderIc := IconFolder
				if n.open {
					folderIc = IconFolderOpen
				}
				ui.Icon(c, folderIc).Size(14, 14).TextColor(t.TextMuted)
				ui.Text(c, n.name).FontSize(12.5).SingleLine().Grow(1)
				if n.badge != "" {
					ui.Box(c).Size(5, 5).Radius(3).Background(t.Warning)
				}
			} else {
				ui.Box(c).Width(12) // align with folder chevron
				icColor := t.TextMuted
				if selected {
					icColor = t.Accent
				}
				ui.Icon(c, iconForFile(n.path)).Size(14, 14).TextColor(icColor)
				lbl := ui.Text(c, n.name).FontSize(12.5).SingleLine().Grow(1)
				if selected {
					lbl.FontWeight(500)
				}
				renderStatusBadge(c, n.badge)
			}
		})
	}).Grow(1).Gap(3).Padding(6, 8)
}

type gitRowKind int

const (
	gitRowSectionChanges gitRowKind = iota
	gitRowWorkingFile
	gitRowSectionCommits
	gitRowCommit
	gitRowCommitFile
)

type gitSidebarRow struct {
	kind       gitRowKind
	key        string
	fileStatus FileStatus
	commitIdx  int
	commitFile CommitFile
	cfIndex    int
}

func (a *app) buildGitSidebarRows() []gitSidebarRow {
	var rows []gitSidebarRow

	// 1. Changes Section
	rows = append(rows, gitSidebarRow{kind: gitRowSectionChanges, key: "sec:changes"})
	if a.changesOpen {
		for _, st := range a.repo.Staged {
			rows = append(rows, gitSidebarRow{
				kind:       gitRowWorkingFile,
				key:        "staged:" + st.Path,
				fileStatus: st,
			})
		}
		for _, st := range a.repo.Unstaged {
			rows = append(rows, gitSidebarRow{
				kind:       gitRowWorkingFile,
				key:        "work:" + st.Path,
				fileStatus: st,
			})
		}
	}

	// 2. Commits (Git Graph) Section
	rows = append(rows, gitSidebarRow{kind: gitRowSectionCommits, key: "sec:commits"})
	if a.commitsOpen {
		for i, cm := range a.repo.Commits {
			rows = append(rows, gitSidebarRow{
				kind:      gitRowCommit,
				key:       "commit:" + cm.Hash,
				commitIdx: i,
			})
			if a.expandedCommit == cm.Hash {
				for cfIdx, cf := range a.commitFiles {
					rows = append(rows, gitSidebarRow{
						kind:       gitRowCommitFile,
						key:        "cf:" + cm.Hash + ":" + cf.Path,
						commitIdx:  i,
						commitFile: cf,
						cfIndex:    cfIdx,
					})
				}
			}
		}
	}
	return rows
}

func (a *app) gitSidebarBody(c *ui.Context, t *ui.Theme) {
	totalChanges := len(a.repo.Staged) + len(a.repo.Unstaged)

	// Compact Commit Bar
	ui.Row(c).Padding(6, 8).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		sub := compactInput(c, "commit-msg", IconGitCommit, &a.commitMsg, "Commit message…")
		canCommit := strings.TrimSpace(a.commitMsg) != "" && totalChanges > 0
		if (primaryActionBtn(c, "btn-commit", IconCheck, "Commit", !canCommit) || sub) && canCommit {
			msg := strings.TrimSpace(a.commitMsg)
			needStage := len(a.repo.Staged) == 0 && len(a.repo.Unstaged) > 0
			a.commitMsg = ""
			a.runAsyncGit("Commit", func(root string) error {
				if needStage {
					if err := GitStageAll(root); err != nil {
						return err
					}
				}
				return GitCommit(root, msg, false)
			})
		}
	})

	rows := a.buildGitSidebarRows()
	a.gitList.Key = func(i int) any { return rows[i].key }

	ui.List(c, &a.gitList, len(rows), func(i int) {
		r := rows[i]
		switch r.kind {
		case gitRowSectionChanges:
			a.renderChangesSectionHeader(c, t, totalChanges)
		case gitRowWorkingFile:
			a.renderWorkingFileRow(c, t, r.fileStatus)
		case gitRowSectionCommits:
			a.renderCommitsSectionHeader(c, t)
		case gitRowCommit:
			a.renderCommitGraphRow(c, t, r.commitIdx)
		case gitRowCommitFile:
			a.renderCommitFileRow(c, t, r.commitIdx, r.cfIndex, r.commitFile)
		}
	}).Grow(1).PaddingY(2)
}

func (a *app) renderChangesSectionHeader(c *ui.Context, t *ui.Theme, count int) {
	hdr := ui.Row(c.Key("hdr-changes")).Height(28).Padding(0, 8).Gap(6).
		AlignItems(ui.Center).Cursor(ui.CursorPointer)
	actionClicked := false
	hdr.Children(func() {
		chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(t.TextMuted)
		if a.changesOpen {
			chev.Rotate(90)
		}
		ui.Textf(c, "CHANGES (%d)", count).FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
		if len(a.repo.Staged) > 0 {
			if rowIconBtn(c, "unstage-all", IconMinus, "Unstage All") {
				actionClicked = true
				a.runAsyncGit("Unstage All", GitUnstageAll)
			}
		}
		if len(a.repo.Unstaged) > 0 {
			if rowIconBtn(c, "stage-all", IconPlus, "Stage All") {
				actionClicked = true
				a.runAsyncGit("Stage All", GitStageAll)
			}
		}
	})
	if hdr.Clicked() && !actionClicked {
		a.changesOpen = !a.changesOpen
	}
}

func (a *app) renderWorkingFileRow(c *ui.Context, t *ui.Theme, st FileStatus) {
	selected := a.diffKind == diffWorking && a.diffFileStat != nil &&
		a.diffFileStat.Path == st.Path && a.diffFileStat.Staged == st.Staged

	rowKey := fmt.Sprintf("wf:%v:%s", st.Staged, st.Path)
	row := ui.Row(c.Key(rowKey)).Height(24).Padding(0, 8, 0, 12).Gap(6).
		Radius(5).Margin(1.5, 8).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hovered := row.Hovered()
	if selected {
		row.Background(t.Accent.Alpha(0.10)).TextColor(t.Text)
	} else if hovered {
		row.Background(t.SurfaceHover)
	}

	actionClicked := false
	row.Children(func() {
		icColor := t.TextMuted
		if selected {
			icColor = t.Accent
		}
		ui.Icon(c, iconForFile(st.Path)).Size(14, 14).TextColor(icColor).Shrink(0)
		lbl := ui.Text(c, filepath.Base(st.Path)).FontSize(12.5).SingleLine().Shrink(0)
		if selected {
			lbl.FontWeight(500)
		}
		dir := filepath.Dir(st.Path)
		if dir != "." && !hovered {
			ui.Text(c, dir).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
		} else {
			ui.Spacer(c)
		}
		if st.Staged && !hovered {
			ui.Row(c).Padding(1, 4).Radius(3).Background(t.Success.Alpha(0.15)).Children(func() {
				ui.Text(c, "staged").FontSize(9).TextColor(t.Success)
			})
		}
		if hovered {
			if st.Staged {
				if rowIconBtn(c, "unstage:"+st.Path, IconMinus, "Unstage") {
					actionClicked = true
					p := st.Path
					a.runAsyncGit("Unstage", func(root string) error { return GitUnstageFile(root, p) })
				}
			} else {
				if rowIconBtn(c, "discard:"+st.Path, IconUndo, "Discard changes") {
					actionClicked = true
					target := st
					a.runAsyncGit("Discard", func(root string) error { return GitDiscardFile(root, target) })
				}
				if rowIconBtn(c, "stage:"+st.Path, IconPlus, "Stage") {
					actionClicked = true
					p := st.Path
					a.runAsyncGit("Stage", func(root string) error { return GitStageFile(root, p) })
				}
			}
		}
		renderStatusBadge(c, st.Badge())
	})

	if row.Clicked() && !actionClicked {
		a.selectWorkingFile(st)
	}
}

func (a *app) renderCommitsSectionHeader(c *ui.Context, t *ui.Theme) {
	hdr := ui.Row(c.Key("hdr-commits")).Height(28).Padding(0, 8).Margin(4, 0, 0, 0).Gap(6).
		AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Cursor(ui.CursorPointer)
	hdr.Children(func() {
		chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(t.TextMuted)
		if a.commitsOpen {
			chev.Rotate(90)
		}
		ui.Textf(c, "GRAPH (%d)", len(a.repo.Commits)).FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
	})
	if hdr.Clicked() {
		a.commitsOpen = !a.commitsOpen
	}
}

func (a *app) renderCommitGraphRow(c *ui.Context, t *ui.Theme, cIdx int) {
	cm := a.repo.Commits[cIdx]
	gRow := a.repo.Graph[cIdx]
	expanded := a.expandedCommit == cm.Hash

	graphW := float32(max(gRow.MaxCols, 1)*14 + 6)
	if graphW > 96 {
		graphW = 96
	}

	row := ui.Row(c.Key("cm:"+cm.Hash)).Height(30).Padding(0, 8, 0, 2).Gap(5).
		Radius(5).Margin(0, 4).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if expanded {
		row.Background(t.SurfaceHover)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover.Alpha(0.6))
	}

	row.Children(func() {
		ui.Box(c).Width(graphW).FillHeight().Draw(func(p *ui.Painter, r ui.Rect) {
			DrawGraphCell(p, r, gRow, t.Surface)
		})

		if len(cm.Refs) > 0 {
			col := GraphColor(gRow.NodeColor)
			refLabel := cm.Refs[0]
			if after, ok := strings.CutPrefix(refLabel, "HEAD -> "); ok {
				refLabel = after
			}
			ui.Row(c).Padding(1, 5).Radius(4).Background(col.Alpha(0.16)).Children(func() {
				ui.Text(c, refLabel).FontSize(10).Bold().TextColor(col).SingleLine()
			})
		}

		ui.Text(c, cm.Subject).SingleLine().Grow(1)
		ui.Text(c, cm.ShortHash).Font("monospace").FontSize(10).TextColor(t.TextMuted).SingleLine()
	})

	if row.Clicked() {
		a.toggleCommit(cIdx)
	}
}

func (a *app) renderCommitFileRow(c *ui.Context, t *ui.Theme, cIdx, cfIdx int, cf CommitFile) {
	cm := a.repo.Commits[cIdx]
	selected := a.diffKind == diffCommitFile && a.selectedCommit != nil &&
		a.selectedCommit.Hash == cm.Hash && a.selectedCommitF == cfIdx

	row := ui.Row(c.Key(fmt.Sprintf("cfr:%s:%d", cm.Hash, cfIdx))).Height(24).
		Padding(0, 8, 0, 24).Gap(6).Radius(5).Margin(1.5, 8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if selected {
		row.Background(t.Accent.Alpha(0.10)).TextColor(t.Text)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}

	row.Children(func() {
		icColor := t.TextMuted
		if selected {
			icColor = t.Accent
		}
		ui.Icon(c, iconForFile(cf.Path)).Size(13, 13).TextColor(icColor).Shrink(0)
		lbl := ui.Text(c, filepath.Base(cf.Path)).FontSize(12).SingleLine().Shrink(0)
		if selected {
			lbl.FontWeight(500)
		}
		dir := filepath.Dir(cf.Path)
		if dir != "." {
			ui.Text(c, dir).FontSize(10).TextColor(t.TextMuted).SingleLine().Grow(1)
		} else {
			ui.Spacer(c)
		}
		renderStatusBadge(c, cf.Status)
	})

	if row.Clicked() {
		a.selectedCommit = &cm
		a.selectCommitFile(cfIdx)
	}
}

// ============================================================================
// Right Main Pane: Single Clean 44px Header + Full-Bleed Code or Diff View
// ============================================================================

func (a *app) mainPaneView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		if a.repo == nil {
			a.emptyRepoView(c, t)
			return
		}
		if a.activeMode == modeFiles {
			a.filesMainPane(c, t)
		} else {
			a.gitMainPane(c, t)
		}
	})
}

func (a *app) filesMainPane(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	rightInset := max(bar.Right+12, 14)

	// Single 44px Header Bar
	ui.Row(c).Height(headerHeight).FillWidth().Padding(0, rightInset, 0, 14).Gap(8).
		AlignItems(ui.Center).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {

		if a.selectedFile == "" {
			ui.Text(c, "No file selected").TextColor(t.TextMuted)
			return
		}

		ui.Icon(c, iconForFile(a.selectedFile)).Size(15, 15).TextColor(t.TextMuted)
		dir := filepath.Dir(a.selectedFile)
		if dir != "." {
			ui.Text(c, dir+" /").TextColor(t.TextMuted).SingleLine()
		}
		ui.Text(c, filepath.Base(a.selectedFile)).Bold().SingleLine()

		badge := a.repo.StatusByRel[a.selectedFile]
		renderStatusBadge(c, badge)

		ui.Spacer(c)

		if badge != "" {
			if ghostBtn(c, "jump-to-diff", IconGitCompare, "View Diff", "Inspect changes in Git view", false) {
				for _, st := range a.repo.Unstaged {
					if st.Path == a.selectedFile {
						a.selectWorkingFile(st)
						a.activeMode = modeGit
						return
					}
				}
				for _, st := range a.repo.Staged {
					if st.Path == a.selectedFile {
						a.selectWorkingFile(st)
						a.activeMode = modeGit
						return
					}
				}
			}
		}

		if iconBtn(c, "code-wrap", IconWrapText, "Toggle Soft Wrap", a.codeSoftWrap) {
			a.codeSoftWrap = !a.codeSoftWrap
		}
		if iconBtn(c, "code-find", IconSearch, "Find in File (⌘F)", false) {
			a.codeState.Run(editor.Find)
		}
	})

	if a.selectedFile == "" {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "Select a file from the sidebar to read").TextColor(t.TextMuted)
		})
		return
	}

	a.codeState.ReadOnly = true
	a.codeState.SoftWrap = a.codeSoftWrap
	editor.View(c, a.codeState).Grow(1).FillWidth()
}

func (a *app) gitMainPane(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	rightInset := max(bar.Right+12, 14)

	added, deleted := a.diffState.Stats()
	hunks := a.diffState.Hunks()
	curHunk, _ := a.diffState.CurrentHunk()

	// Single 44px Header Bar
	ui.Row(c).Height(headerHeight).FillWidth().Padding(0, rightInset, 0, 14).Gap(8).
		AlignItems(ui.Center).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {

		if a.diffKind == diffNone {
			ui.Text(c, "Diff Inspector").TextColor(t.TextMuted)
			return
		}

		ui.Icon(c, iconForFile(a.diffTitle)).Size(15, 15).TextColor(t.TextMuted)
		dir := filepath.Dir(a.diffTitle)
		if dir != "." {
			ui.Text(c, dir+" /").TextColor(t.TextMuted).SingleLine()
		}
		ui.Text(c, filepath.Base(a.diffTitle)).Bold().SingleLine()
		renderStatusBadge(c, a.diffBadge)

		// Context pill (Working Tree vs Commit Hash)
		if a.diffKind == diffCommitFile && a.selectedCommit != nil {
			ui.Row(c).Padding(2, 6).Radius(4).Gap(4).AlignItems(ui.Center).Background(t.SurfaceHover).Children(func() {
				ui.Icon(c, IconGitCommit).Size(12, 12).TextColor(t.Accent)
				ui.Text(c, a.selectedCommit.ShortHash).Font("monospace").FontSize(11).TextColor(t.Accent)
				ui.Text(c, a.selectedCommit.Subject).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		}

		if added > 0 || deleted > 0 {
			ui.Row(c).Gap(4).Padding(2, 6).Radius(4).Background(t.SurfaceHover).Children(func() {
				ui.Textf(c, "+%d", added).Font("monospace").FontSize(11).Bold().TextColor(t.Success)
				ui.Textf(c, "−%d", deleted).Font("monospace").FontSize(11).Bold().TextColor(t.Danger)
			})
		}

		ui.Spacer(c)

		if a.diffKind == diffWorking && a.diffFileStat != nil {
			st := *a.diffFileStat
			if st.Staged {
				if ghostBtn(c, "diff-unstage", IconMinus, "Unstage", "Unstage this file", false) {
					a.runAsyncGit("Unstage", func(root string) error { return GitUnstageFile(root, st.Path) })
				}
			} else {
				if ghostBtn(c, "diff-stage", IconPlus, "Stage", "Stage this file", false) {
					a.runAsyncGit("Stage", func(root string) error { return GitStageFile(root, st.Path) })
				}
			}
			if ghostBtn(c, "diff-open-file", IconFiles, "File", "Read full file in Explorer", false) {
				a.selectExplorerFile(st.Path)
				a.activeMode = modeFiles
			}
		}

		if len(hunks) > 0 {
			if iconBtn(c, "hunk-prev", IconChevronUp, "Previous Change", false) {
				prev := curHunk - 1
				if prev < 0 {
					prev = len(hunks) - 1
				}
				a.diffState.GoToHunk(prev)
			}
			if iconBtn(c, "hunk-next", IconChevronDown, "Next Change", false) {
				next := curHunk + 1
				if next >= len(hunks) {
					next = 0
				}
				a.diffState.GoToHunk(next)
			}
		}

		if iconBtn(c, "diff-expand", IconExpand, "Expand Unchanged Lines", a.diffExpandAll) {
			a.diffExpandAll = !a.diffExpandAll
		}
		if iconBtn(c, "diff-wrap", IconWrapText, "Toggle Soft Wrap", a.diffWrap) {
			a.diffWrap = !a.diffWrap
		}
		if iconBtn(c, "diff-split", IconColumns, "Side-by-Side Diff", a.diffSplit) {
			a.diffSplit = true
		}
		if iconBtn(c, "diff-unified", IconRows, "Unified Diff", !a.diffSplit) {
			a.diffSplit = false
		}
		if iconBtn(c, "diff-find", IconSearch, "Find in Diff (⌘F)", false) {
			a.diffState.Run(editor.Find)
		}
	})

	if a.diffKind == diffNone {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "Select a changed file or commit from the left sidebar").TextColor(t.TextMuted)
		})
		return
	}

	a.diffState.Editable = false
	a.diffState.Wrap = a.diffWrap
	a.diffState.ExpandUnchanged = a.diffExpandAll
	if a.diffSplit {
		a.diffState.Layout = editor.DiffSplit
	} else {
		a.diffState.Layout = editor.DiffUnified
	}
	editor.DiffView(c, a.diffState).Grow(1).FillWidth()
}

func (a *app) emptyRepoView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Padding(32).Children(func() {
		ui.Icon(c, IconGitBranch).Size(32, 32).TextColor(t.TextMuted)
		ui.Text(c, "No Git Repository Loaded").FontSize(16).Bold()
		if a.repoErr != "" {
			ui.Text(c, a.repoErr).TextColor(t.TextMuted)
		}
		if primaryActionBtn(c, "open-repo-main", IconFolder, "Open Repository Folder…", false) {
			a.openRepoDialog()
		}
	})
}

func (a *app) modalsView(c *ui.Context, t *ui.Theme) {
	ui.Modal(c, &a.newBranchOpen, func() {
		ui.Column(c).Width(340).Padding(16).Gap(12).Radius(10).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconGitBranch).Size(15, 15).TextColor(t.Accent)
				ui.Text(c, "Create & Checkout Branch").FontSize(14).Bold()
			})
			sub := ui.TextInput(c, &a.newBranchName).Placeholder("feature/branch-name").Label("Branch name").AutoFocus().FillWidth().Submitted()
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ghostBtn(c, "cancel-branch", nil, "Cancel", "", false) {
					a.newBranchOpen = false
				}
				name := strings.TrimSpace(a.newBranchName)
				if (primaryActionBtn(c, "create-branch", IconCheck, "Create", name == "") || sub) && name != "" {
					a.newBranchOpen = false
					a.runAsyncGit("Create branch "+name, func(root string) error {
						return GitCheckoutBranch(root, name, true)
					})
				}
			})
		})
	})
}

func (a *app) openRepoDialog() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:    a.win,
			Title:     "Open Git Repository",
			Directory: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		picked := paths[0]
		if a.win != nil {
			a.win.Update(func() {
				a.repoDir = picked
				a.selectedFile = ""
				a.diffKind = diffNone
				a.openDirs = make(map[string]bool)
				a.refreshRepo()
			})
		}
	}()
}

func main() {
	startDir := ""
	if len(os.Args) > 1 {
		startDir = os.Args[1]
	}
	a := newApp(startDir)

	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:          "Arbor",
			Width:          1280,
			Height:         820,
			MinWidth:       820,
			MinHeight:      520,
			StateKey:       "arbor-main",
			TitleBarStyle:  mygo.TitleBarHiddenInset,
			TitleBarHeight: headerHeight,
			Vibrancy:       mygo.VibrancySidebar,
			Content:        ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
