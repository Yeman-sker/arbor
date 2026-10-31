package main

import (
	"arbor/perf"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	paints   perf.PaintTracker
	cards    commitCards
	win      *mygo.Window
	repoDir  string
	repo     *RepoSnapshot
	repoErr  string
	busyText string
	live     liveState

	spanState // perf bookkeeping, see instrument.go

	// Global UI state
	activeMode    int // 0 = Files, 1 = Git
	sidebarW      float32
	newBranchOpen bool
	newBranchName string
	contentSeq    uint64
	fades         scrollFades

	// Mode 0: Files (Explorer) state
	openDirs      map[string]bool
	explorerList  ui.ListState
	explorerQuery string
	onlyChanged   bool
	selectedFile  string
	codeState     *editor.State
	codeSoftWrap  bool

	// Mode 1: Git (Source Control + Graph) state
	gitChangesH     float32
	changesList     ui.ListState
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
		gitChangesH:     200,
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
						btnBg := t.Surface.Alpha(0)
						btnFg := t.TextMuted
						btnScale := float32(1.0)
						if btn.Pressed() {
							btnBg = t.SurfacePressed
							btnFg = t.Text
							btnScale = 0.92
						} else if btn.Hovered() {
							btnBg = t.SurfacePressed
							btnFg = t.Text
						}
						btn.Background(btn.AnimateColor("bg", btnBg, microColorAnim)).
							TextColor(btn.AnimateColor("fg", btnFg, microColorAnim)).
							AnimatePose(ui.Pose{Scale: btnScale}, poseAnim)
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
	a.applyRepoSnapshot(snap, err)
}

func (a *app) reloadRepoAsync(dir string) {
	a.reload(dir) // live.go: queued with the other Git work
}

func isPreferredSourceFile(p string) bool {
	for _, ext := range []string{".go", ".md", ".py", ".ts", ".tsx", ".js", ".rs", ".toml", ".yaml", ".yml"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func (a *app) applyRepoSnapshot(snap *RepoSnapshot, err error) {
	defer a.startupReady(err)
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
			if strings.HasSuffix(f, "main.go") || strings.EqualFold(filepath.Base(f), "README.md") || isPreferredSourceFile(f) {
				pick = f
				break
			}
		}
		a.selectExplorerFile(pick)
	} else if a.selectedFile != "" {
		a.reselectExplorerFile(snap) // opens another file if this one is gone
	}

	// Ensure a diff target is ready in Git mode
	a.syncCommitSelection(snap)
	if a.diffKind == diffNone {
		if len(snap.Unstaged) > 0 {
			a.selectWorkingFile(snap.Unstaged[0])
		} else if len(snap.Staged) > 0 {
			a.selectWorkingFile(snap.Staged[0])
		} else if len(snap.Commits) > 0 {
			a.expandDefaultCommit()
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
				a.expandDefaultCommit()
			}
		}
	}
}

func (a *app) selectExplorerFile(relPath string) {
	if a.repo == nil || relPath == "" {
		return
	}
	if _, isDir := a.repo.TreeDirs[relPath]; isDir {
		return
	}
	a.selectedFile = relPath
	a.openExplorerFile(relPath) // content.go: reads the file in the background
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
	a.openWorkingDiff(st) // content.go: loads both sides in the background
}

func (a *app) toggleCommit(idx int) {
	if a.repo == nil || idx < 0 || idx >= len(a.repo.Commits) {
		return
	}
	if a.expandedCommit == a.repo.Commits[idx].Hash {
		a.expandedCommit = ""
		return
	}
	a.expandCommit(idx) // content.go: loads the files in the background
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
	a.openCommitDiff(a.selectedCommit.Hash, cf) // content.go
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
	t.Spacing = 4
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
		a.switchMode(modeFiles)
	}
	if c.Shortcut(ui.Cmd, ui.Key2) {
		a.switchMode(modeGit)
	}
	if c.Shortcut(ui.Cmd, ui.KeyR) {
		a.reloadRepoAsync(a.repoDir)
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		if a.activeMode == modeFiles {
			a.codeState.Run(editor.Find)
		} else {
			a.diffState.Run(editor.Find)
		}
	}

	a.sidebarW = min(max(a.sidebarW, 252), 540)
	ui.Split(c, &a.sidebarW, func() {
		a.sidebarView(c, t)
	}, func() {
		a.mainPaneView(c, t)
	}).Fill()
	a.sidebarW = min(max(a.sidebarW, 252), 540)

	a.modalsView(c, t)
	a.liveView(c)

	// Last, so a span armed by anything built above is painted in this frame.
	if a.paints.Pending() {
		ui.Box(c).Absolute().Width(1).Height(1).Draw(func(*ui.Painter, ui.Rect) { a.paints.Painted() })
	}
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

			gitBadge := 0
			if a.repo != nil {
				gitBadge = len(a.repo.Staged) + len(a.repo.Unstaged)
			}
			segmentedModeSwitcher(c, a.activeMode, gitBadge, a.switchMode)

			ui.Spacer(c)

			if a.busyText != "" {
				ui.Spinner(c).Label(a.busyText)
			}
			if iconBtnState(c, "btn-refresh", IconRefresh, "Refresh (⌘R)", a.busyText != "", a.writeBusy()) {
				a.reloadRepoAsync(a.repoDir)
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
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Clip().Children(func() {

			repoName := filepath.Base(a.repo.Root)
			if ghostBtn(c, "btn-repo", IconFolder, repoName, a.repo.Root+" (Click to switch repository)", false) {
				a.openRepoDialog()
			}

			ghostMenuBtn(c, "btn-branch", IconGitBranch, a.repo.Branch, "Switch or create branch ("+a.repo.Branch+")", func(m *ui.Menu) {
				if m.Item("+ New Branch…").Chosen() {
					a.newBranchName = ""
					a.newBranchOpen = true
				}
				m.Separator()
				const maxMenuBranches = 40
				shownCurrent := false
				limit := min(len(a.repo.Branches), maxMenuBranches)
				for _, br := range a.repo.Branches[:limit] {
					if br == a.repo.Branch {
						shownCurrent = true
					}
					if m.Item(br).Radio(br == a.repo.Branch).Chosen() && br != a.repo.Branch {
						target := br
						a.checkoutBranch(target, false)
					}
				}
				if !shownCurrent && a.repo.Branch != "" {
					m.Item(a.repo.Branch).Radio(true)
				}
			})

			ui.Spacer(c)

			pullTip := "Pull (git pull --rebase)"
			if a.repo.Behind > 0 {
				pullTip = fmt.Sprintf("Pull (%d behind)", a.repo.Behind)
			}
			if iconBtn(c, "btn-pull", IconArrowDown, pullTip, a.repo.Behind > 0) {
				a.runAsyncGit("Pull", GitPull)
			}
			pushTip := "Push (git push)"
			if a.repo.Ahead > 0 {
				pushTip = fmt.Sprintf("Push (%d ahead)", a.repo.Ahead)
			}
			if iconBtn(c, "btn-push", IconArrowUp, pushTip, a.repo.Ahead > 0) {
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

	a.trackSearch()
	q := strings.ToLower(strings.TrimSpace(a.explorerQuery))
	nodes := a.buildFlatTree(q)

	a.explorerList.Key = func(i int) any { return nodes[i].path }
	a.fades.tree.List(c, t, a.contentSeq, &a.explorerList, len(nodes), func(i int) {
		n := nodes[i]
		selected := !n.isDir && n.path == a.selectedFile
		row := ui.Row(c.Key("tree:"+n.path)).Height(24).
			Padding(0, 8, 0, float32(6+n.depth*14)).Gap(6).Radius(5).
			AlignItems(ui.Center).Cursor(ui.CursorPointer)

		hovered := row.Hovered()
		pressed := row.Pressed()

		targetBg := t.Surface.Alpha(0)
		targetBd := t.Accent.Alpha(0)
		if selected {
			targetBg = t.Accent.Alpha(0.09)
			targetBd = t.Accent.Alpha(0.20)
			if pressed {
				targetBg = t.Accent.Alpha(0.14)
			} else if hovered {
				targetBg = t.Accent.Alpha(0.12)
			}
		} else if pressed {
			targetBg = t.SurfacePressed
		} else if hovered {
			targetBg = t.SurfaceHover
		}
		rowScale := float32(1.0)
		if pressed {
			rowScale = 0.985
		}
		row.Background(row.AnimateColor("bg", targetBg, microColorAnim)).
			Border(1, row.AnimateColor("bd", targetBd, microColorAnim)).
			AnimatePose(ui.Pose{Scale: rowScale}, poseAnim)

		targetInd := float32(0)
		if selected {
			targetInd = 13
		}
		if indH := row.Spring("ind", targetInd, snappySpring); indH > 0.4 {
			accent := t.Accent
			row.DrawOver(func(p *ui.Painter, r ui.Rect) {
				p.Fill(ui.Rect{X: r.X + 1.5, Y: r.Y + (r.H-indH)/2, W: 2.5, H: indH}, accent.Alpha(min(indH/13, 1)), 1.2)
			})
		}

		if row.Clicked() {
			if n.isDir {
				a.openDirs[n.path] = !a.openDirs[n.path]
			} else {
				a.openFile(n.path)
			}
		}

		nudgeX := float32(0)
		if hovered && !selected {
			nudgeX = 2.0
		} else if selected {
			nudgeX = 1.0
		}

		row.Children(func() {
			if n.isDir {
				chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(t.TextMuted)
				rotTarget := float32(0)
				if n.open {
					rotTarget = 90
				}
				chev.Rotate(chev.Spring("rot", rotTarget, snappySpring))
				folderIc := IconFolder
				folderCol := t.TextMuted
				folderScale := float32(1.0)
				if n.open {
					folderIc = IconFolderOpen
					folderCol = t.Accent.Alpha(0.85)
				}
				if hovered {
					folderScale = 1.1
					folderCol = t.Accent
				}
				fic := ui.Icon(c, folderIc).Size(14, 14)
				fic.TextColor(fic.AnimateColor("fc", folderCol, microColorAnim)).
					AnimatePose(ui.Pose{X: nudgeX * 0.5, Scale: folderScale}, snappyPoseAnim)

				lbl := ui.Text(c, n.name).FontSize(12.5).SingleLine().Grow(1)
				lbl.Translate(lbl.Spring("x", nudgeX, microSpring), 0)
				if n.badge != "" {
					dot := ui.Box(c).Size(5.5, 5.5).Radius(3).Background(t.Warning)
					dotScale := float32(1.0)
					if hovered {
						dotScale = 1.25
					}
					dot.AnimatePose(ui.Pose{Scale: dotScale}, snappyPoseAnim)
				}
			} else {
				ui.Box(c).Width(12) // align with folder chevron
				renderFileIcon(c, n.path, 14, selected, hovered)
				lbl := ui.Text(c, n.name).FontSize(12.5).SingleLine().Grow(1)
				lbl.Translate(lbl.Spring("x", nudgeX, microSpring), 0)
				if selected {
					lbl.FontWeight(500)
				}
				renderStatusBadge(c, n.badge, selected || hovered)
			}
		})
	}).Grow(1).MinHeight(0).Gap(3).Padding(6, 8)
}

type gitRowKind int

const (
	gitRowCommit gitRowKind = iota
	gitRowCommitFile
)

type gitCommitRow struct {
	kind       gitRowKind
	key        string
	commitIdx  int
	commitFile CommitFile
	cfIndex    int
}

func (a *app) buildWorkingChangeRows() []FileStatus {
	rows := make([]FileStatus, 0, len(a.repo.Staged)+len(a.repo.Unstaged))
	rows = append(rows, a.repo.Staged...)
	rows = append(rows, a.repo.Unstaged...)
	return rows
}

func (a *app) buildCommitGraphRows() []gitCommitRow {
	rows := make([]gitCommitRow, 0, len(a.repo.Commits)+len(a.commitFiles))
	for i, cm := range a.repo.Commits {
		rows = append(rows, gitCommitRow{
			kind:      gitRowCommit,
			key:       "commit:" + cm.Hash,
			commitIdx: i,
		})
		if a.expandedCommit == cm.Hash {
			for cfIdx, cf := range a.commitFiles {
				rows = append(rows, gitCommitRow{
					kind:       gitRowCommitFile,
					key:        "cf:" + cm.Hash + ":" + cf.Path,
					commitIdx:  i,
					commitFile: cf,
					cfIndex:    cfIdx,
				})
			}
		}
	}
	return rows
}

func (a *app) gitSidebarBody(c *ui.Context, t *ui.Theme) {
	totalChanges := len(a.repo.Staged) + len(a.repo.Unstaged)

	// Compact Commit Bar
	ui.Row(c).Shrink(0).Padding(6, 8).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		sub := compactInput(c, "commit-msg", IconGitCommit, &a.commitMsg, "Commit message…")
		canCommit := strings.TrimSpace(a.commitMsg) != "" && totalChanges > 0
		if (primaryActionBtn(c, "btn-commit", IconCheck, "Commit", !canCommit) || sub) && canCommit {
			msg := strings.TrimSpace(a.commitMsg)
			needStage := len(a.repo.Staged) == 0 && len(a.repo.Unstaged) > 0
			a.commitMsg = ""
			a.runGitOp(perf.OpCommit, "Commit", func(root string) error {
				if needStage {
					if err := GitStageAll(root); err != nil {
						return err
					}
				}
				return GitCommit(root, msg, false)
			})
		}
	})

	if a.changesOpen && a.commitsOpen {
		ui.SplitVertical(c, &a.gitChangesH, func() {
			a.gitChangesSection(c, t, totalChanges)
		}, func() {
			a.gitCommitsSection(c, t, false)
		}).Grow(1).MinHeight(0)
		return
	}

	a.gitChangesSection(c, t, totalChanges)
	a.gitCommitsSection(c, t, true)
}

func (a *app) gitChangesSection(c *ui.Context, t *ui.Theme, totalChanges int) {
	a.renderChangesSectionHeader(c, t, totalChanges)
	if !a.changesOpen {
		return
	}
	changes := a.buildWorkingChangeRows()
	a.changesList.Key = func(i int) any {
		st := changes[i]
		if st.Staged {
			return "staged:" + st.Path
		}
		return "work:" + st.Path
	}
	a.fades.changes.List(c, t, a.contentSeq, &a.changesList, len(changes), func(i int) {
		a.renderWorkingFileRow(c, t, changes[i])
	}).Grow(1).MinHeight(0).PaddingY(2)
}

func (a *app) gitCommitsSection(c *ui.Context, t *ui.Theme, topBorder bool) {
	a.renderCommitsSectionHeader(c, t, topBorder)
	if !a.commitsOpen {
		return
	}
	rows := a.buildCommitGraphRows()
	a.gitList.Key = func(i int) any { return rows[i].key }
	a.fades.graph.List(c, t, a.contentSeq, &a.gitList, len(rows), func(i int) {
		r := rows[i]
		if r.kind == gitRowCommit {
			a.renderCommitGraphRow(c, t, r.commitIdx)
		} else {
			a.renderCommitFileRow(c, t, r.commitIdx, r.cfIndex, r.commitFile)
		}
	}).Grow(1).MinHeight(0).PaddingY(2)
}

func (a *app) renderChangesSectionHeader(c *ui.Context, t *ui.Theme, count int) {
	hdr := ui.Row(c.Key("hdr-changes")).Height(28).Shrink(0).Padding(0, 8).Gap(6).
		AlignItems(ui.Center).Cursor(ui.CursorPointer)
	fgTarget := t.TextMuted
	if hdr.Hovered() {
		fgTarget = t.Text
	}
	fg := hdr.AnimateColor("fg", fgTarget, microColorAnim)
	actionClicked := false
	hdr.Children(func() {
		chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(fg)
		rotTarget := float32(0)
		if a.changesOpen {
			rotTarget = 90
		}
		chev.Rotate(chev.Spring("rot", rotTarget, snappySpring))
		ui.Text(c, "CHANGES").FontSize(11).Bold().TextColor(fg)
		if count > 0 {
			ui.Row(c).Padding(0.5, 5).Radius(6).Background(t.Accent.Alpha(0.14)).Children(func() {
				ui.Textf(c, "%d", count).Key("chg-cnt").Font("monospace").FontSize(10).Bold().TextColor(t.Accent).
					ContentTransition(ui.ContentTransition{Numeric: true})
			})
		}
		ui.Spacer(c)
		if len(a.repo.Staged) > 0 {
			if rowActionBtn(c, "unstage-all", IconMinus, "Unstage All", actionToneWarning) {
				actionClicked = true
				a.runGitOp(perf.OpStage, "Unstage All", GitUnstageAll)
			}
		}
		if len(a.repo.Unstaged) > 0 {
			if rowActionBtn(c, "stage-all", IconPlus, "Stage All", actionToneSuccess) {
				actionClicked = true
				a.runGitOp(perf.OpStage, "Stage All", GitStageAll)
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
	pressed := row.Pressed()
	targetBg := t.Surface.Alpha(0)
	targetBd := t.Accent.Alpha(0)
	if selected {
		targetBg = t.Accent.Alpha(0.09)
		targetBd = t.Accent.Alpha(0.20)
		if pressed {
			targetBg = t.Accent.Alpha(0.14)
		} else if hovered {
			targetBg = t.Accent.Alpha(0.12)
		}
	} else if pressed {
		targetBg = t.SurfacePressed
	} else if hovered {
		targetBg = t.SurfaceHover
	}
	rowScale := float32(1.0)
	if pressed {
		rowScale = 0.985
	}
	row.Background(row.AnimateColor("bg", targetBg, microColorAnim)).
		Border(1, row.AnimateColor("bd", targetBd, microColorAnim)).
		AnimatePose(ui.Pose{Scale: rowScale}, poseAnim)

	targetInd := float32(0)
	if selected {
		targetInd = 13
	}
	if indH := row.Spring("ind", targetInd, snappySpring); indH > 0.4 {
		accent := t.Accent
		row.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + 1.5, Y: r.Y + (r.H-indH)/2, W: 2.5, H: indH}, accent.Alpha(min(indH/13, 1)), 1.2)
		})
	}

	nudgeX := float32(0)
	if hovered && !selected {
		nudgeX = 2.0
	} else if selected {
		nudgeX = 1.0
	}

	trayTarget := float32(0)
	if hovered {
		trayTarget = 1
	}
	trayK := row.Spring("tray", trayTarget, microSpring)

	actionClicked := false
	row.Children(func() {
		renderFileIcon(c, st.Path, 14, selected, hovered)
		lbl := ui.Text(c, filepath.Base(st.Path)).FontSize(12.5).SingleLine().Shrink(0)
		lbl.Translate(lbl.Spring("x", nudgeX, microSpring), 0)
		if selected {
			lbl.FontWeight(500)
		}
		dir := filepath.Dir(st.Path)
		if dir != "." && trayK < 0.5 {
			ui.Text(c, dir).FontSize(11).TextColor(t.TextMuted).Opacity(1 - trayK*1.6).SingleLine().Grow(1).Shrink(1).MinWidth(0).Clip()
		} else {
			ui.Spacer(c)
		}
		if st.Staged && trayK < 0.6 {
			ui.Row(c).Padding(1, 4).Radius(3).Background(t.Success.Alpha(0.15)).Opacity(1 - trayK*1.5).Children(func() {
				ui.Text(c, "staged").FontSize(9).TextColor(t.Success)
			})
		}
		if trayK > 0.02 {
			fullW := float32(42)
			if st.Staged {
				fullW = 20
			}
			tray := ui.Row(c).Width(fullW * trayK).Gap(2).AlignItems(ui.Center).Justify(ui.End).Clip().
				Opacity(trayK)
			tray.Translate((1-trayK)*8, 0)
			tray.Children(func() {
				if st.Staged {
					if rowActionBtn(c, "unstage:"+st.Path, IconMinus, "Unstage", actionToneWarning) {
						actionClicked = true
						p := st.Path
						a.runGitOp(perf.OpStage, "Unstage", func(root string) error { return GitUnstageFile(root, p) })
					}
				} else {
					if rowActionBtn(c, "discard:"+st.Path, IconUndo, "Discard changes", actionToneDanger) {
						actionClicked = true
						target := st
						a.runAsyncGit("Discard", func(root string) error { return GitDiscardFile(root, target) })
					}
					if rowActionBtn(c, "stage:"+st.Path, IconPlus, "Stage", actionToneSuccess) {
						actionClicked = true
						p := st.Path
						a.runGitOp(perf.OpStage, "Stage", func(root string) error { return GitStageFile(root, p) })
					}
				}
			})
		}
		renderStatusBadge(c, st.Badge(), selected || hovered)
	})

	if row.Clicked() && !actionClicked {
		a.clickWorkingDiff(st)
	}
}

func (a *app) renderCommitsSectionHeader(c *ui.Context, t *ui.Theme, topBorder bool) {
	hdr := ui.Row(c.Key("hdr-commits")).Height(28).Shrink(0).Padding(0, 8).Gap(6).
		AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if topBorder {
		hdr.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	}
	fgTarget := t.TextMuted
	if hdr.Hovered() {
		fgTarget = t.Text
	}
	fg := hdr.AnimateColor("fg", fgTarget, microColorAnim)
	hdr.Children(func() {
		chev := ui.Icon(c, IconChevronRight).Size(12, 12).TextColor(fg)
		rotTarget := float32(0)
		if a.commitsOpen {
			rotTarget = 90
		}
		chev.Rotate(chev.Spring("rot", rotTarget, microSpring))
		ui.Textf(c, "GRAPH (%d)", len(a.repo.Commits)).FontSize(11).Bold().TextColor(fg).Grow(1)
	})
	if hdr.Clicked() {
		a.commitsOpen = !a.commitsOpen
	}
}

func (a *app) renderCommitGraphRow(c *ui.Context, t *ui.Theme, cIdx int) {
	cm := a.repo.Commits[cIdx]
	gRow := a.repo.Graph[cIdx]
	expanded := a.expandedCommit == cm.Hash

	graphW := GraphCellWidth(gRow.MaxCols)

	row := ui.Row(c.Key("cm:"+cm.Hash)).Height(30).Padding(0, 8, 0, 2).Gap(5).
		Radius(5).Margin(0, 4).AlignItems(ui.Center).Clip().Cursor(ui.CursorPointer)
	hovered := row.Hovered()
	pressed := row.Pressed()

	targetBg := t.Surface.Alpha(0)
	if expanded {
		targetBg = t.SurfaceHover
	} else if pressed {
		targetBg = t.SurfacePressed
	} else if hovered {
		targetBg = t.SurfaceHover.Alpha(0.75)
	}
	rowScale := float32(1.0)
	if pressed {
		rowScale = 0.988
	}
	row.Background(row.AnimateColor("bg", targetBg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: rowScale}, poseAnim)

	hoverTarget := float32(0)
	if hovered {
		hoverTarget = 1
	}
	expandTarget := float32(0)
	if expanded {
		expandTarget = 1
	}
	hoverK := row.Spring("hk", hoverTarget, microSpring)
	expandK := row.Spring("ek", expandTarget, snappySpring)

	row.Children(func() {
		ui.Box(c).Width(graphW).Shrink(0).FillHeight().Clip().Draw(func(p *ui.Painter, r ui.Rect) {
			DrawGraphCell(p, r, gRow, t.Surface, hoverK, expandK)
		})

		if len(cm.Refs) > 0 {
			col := GraphColor(gRow.NodeColor)
			refLabel := cm.Refs[0]
			if after, ok := strings.CutPrefix(refLabel, "HEAD -> "); ok {
				refLabel = after
			}
			refScale := float32(1.0)
			if hovered || expanded {
				refScale = 1.05
			}
			ui.Row(c).MaxWidth(88).Shrink(0).Padding(1, 5).Radius(4).Clip().
				Background(col.Alpha(0.16+0.06*hoverK)).
				AnimatePose(ui.Pose{Scale: refScale}, snappyPoseAnim).Children(func() {
				ui.Text(c, refLabel).FontSize(10).Bold().TextColor(col).SingleLine().Shrink(1).MinWidth(0).Clip()
			})
		}

		subj := ui.Text(c, cm.Subject).FontSize(12).SingleLine().Grow(1).Shrink(1).MinWidth(0).Clip()
		subj.Translate(1.5*hoverK, 0)
		if expanded {
			subj.FontWeight(500)
		}

		if a.sidebarW-graphW >= 180 {
			hashColTarget := t.TextMuted
			if hovered || expanded {
				hashColTarget = t.Accent
			}
			hashTxt := ui.Text(c, cm.ShortHash).Font("monospace").FontSize(10).SingleLine().Shrink(0)
			hashTxt.TextColor(hashTxt.AnimateColor("hc", hashColTarget, microColorAnim))
		}
	})

	a.commitCard(c, t, row, cm)

	if row.Clicked() {
		a.clickCommit(cIdx)
	}
}

func (a *app) renderCommitFileRow(c *ui.Context, t *ui.Theme, cIdx, cfIdx int, cf CommitFile) {
	cm := a.repo.Commits[cIdx]
	selected := a.diffKind == diffCommitFile && a.selectedCommit != nil &&
		a.selectedCommit.Hash == cm.Hash && a.selectedCommitF == cfIdx

	row := ui.Row(c.Key(fmt.Sprintf("cfr:%s:%d", cm.Hash, cfIdx))).Height(24).
		Padding(0, 8, 0, 24).Gap(6).Radius(5).Margin(1.5, 8).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).
		Transition(ui.ElementTransition{
			Duration: 140 * time.Millisecond,
			Enter:    &ui.Motion{Opacity: 0, Y: -3},
		})
	hovered := row.Hovered()
	pressed := row.Pressed()
	targetBg := t.Surface.Alpha(0)
	targetBd := t.Accent.Alpha(0)
	if selected {
		targetBg = t.Accent.Alpha(0.09)
		targetBd = t.Accent.Alpha(0.20)
		if pressed {
			targetBg = t.Accent.Alpha(0.14)
		} else if hovered {
			targetBg = t.Accent.Alpha(0.12)
		}
	} else if pressed {
		targetBg = t.SurfacePressed
	} else if hovered {
		targetBg = t.SurfaceHover
	}
	rowScale := float32(1.0)
	if pressed {
		rowScale = 0.985
	}
	row.Background(row.AnimateColor("bg", targetBg, microColorAnim)).
		Border(1, row.AnimateColor("bd", targetBd, microColorAnim)).
		AnimatePose(ui.Pose{Scale: rowScale}, poseAnim)

	targetInd := float32(0)
	if selected {
		targetInd = 13
	}
	if indH := row.Spring("ind", targetInd, snappySpring); indH > 0.4 {
		accent := t.Accent
		row.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + 1.5, Y: r.Y + (r.H-indH)/2, W: 2.5, H: indH}, accent.Alpha(min(indH/13, 1)), 1.2)
		})
	}

	nudgeX := float32(0)
	if hovered && !selected {
		nudgeX = 2.0
	} else if selected {
		nudgeX = 1.0
	}

	row.Children(func() {
		renderFileIcon(c, cf.Path, 13, selected, hovered)
		lbl := ui.Text(c, filepath.Base(cf.Path)).FontSize(12).SingleLine().Shrink(0)
		lbl.Translate(lbl.Spring("x", nudgeX, microSpring), 0)
		if selected {
			lbl.FontWeight(500)
		}
		dir := filepath.Dir(cf.Path)
		if dir != "." {
			ui.Text(c, dir).FontSize(10).TextColor(t.TextMuted).SingleLine().Grow(1).Shrink(1).MinWidth(0).Clip()
		} else {
			ui.Spacer(c)
		}
		renderStatusBadge(c, cf.Status, selected || hovered)
	})

	if row.Clicked() {
		a.selectedCommit = &cm
		a.clickCommitDiff(cfIdx)
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

		renderFileIcon(c, a.selectedFile, 15, true, false)
		dir := filepath.Dir(a.selectedFile)
		if dir != "." {
			ui.Text(c, dir+" /").TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0).Clip()
		}
		ui.Text(c, filepath.Base(a.selectedFile)).Key("file-title").Bold().SingleLine().Shrink(0).
			ContentTransition(ui.ContentTransition{})

		badge := a.repo.StatusByRel[a.selectedFile]
		renderStatusBadge(c, badge, true)

		ui.Spacer(c)

		if badge != "" {
			if ghostBtn(c, "jump-to-diff", IconGitCompare, "View Diff", "Inspect changes in Git view", false) {
				for _, st := range a.repo.Unstaged {
					if st.Path == a.selectedFile {
						a.selectWorkingFile(st)
						a.activeMode = modeGit
						a.contentSeq++
						return
					}
				}
				for _, st := range a.repo.Staged {
					if st.Path == a.selectedFile {
						a.selectWorkingFile(st)
						a.activeMode = modeGit
						a.contentSeq++
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
	edWrap := ui.Column(c.Key("code-viewport")).Grow(1).FillWidth()
	k := edWrap.Keyframes("enter", []ui.Keyframe{
		{At: 0, Value: 0},
		{At: 140 * time.Millisecond, Value: 1, Ease: ui.EaseOut},
	}, ui.KeyframeOptions{Run: a.contentSeq})
	edWrap.Opacity(0.75+0.25*k).Translate(0, (1-k)*3)
	edWrap.Children(func() {
		a.fades.code.Editor(c, t, a.contentSeq, a.codeState).Grow(1).FillWidth()
	})
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

		renderFileIcon(c, a.diffTitle, 15, true, false)
		dir := filepath.Dir(a.diffTitle)
		if dir != "." {
			ui.Text(c, dir+" /").TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0).Clip()
		}
		ui.Text(c, filepath.Base(a.diffTitle)).Key("diff-title").Bold().SingleLine().Shrink(0).
			ContentTransition(ui.ContentTransition{})
		renderStatusBadge(c, a.diffBadge, true)

		// Context pill (Working Tree vs Commit Hash)
		if a.diffKind == diffCommitFile && a.selectedCommit != nil {
			ui.Row(c).Padding(2, 6).Radius(4).Gap(4).AlignItems(ui.Center).Shrink(1).MinWidth(0).Clip().
				Background(t.SurfaceHover).Children(func() {
				ui.Icon(c, IconGitCommit).Size(12, 12).Shrink(0).TextColor(t.Accent)
				ui.Text(c, a.selectedCommit.ShortHash).Font("monospace").FontSize(11).Shrink(0).TextColor(t.Accent)
				ui.Text(c, a.selectedCommit.Subject).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0).Clip()
			})
		}

		if added > 0 || deleted > 0 {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				if added > 0 {
					ui.Row(c).Padding(1.5, 5).Radius(4).Background(t.Success.Alpha(0.13)).Children(func() {
						ui.Textf(c, "+%d", added).Key("diff-add").Font("monospace").FontSize(10.5).Bold().TextColor(t.Success).
							ContentTransition(ui.ContentTransition{Numeric: true})
					})
				}
				if deleted > 0 {
					ui.Row(c).Padding(1.5, 5).Radius(4).Background(t.Danger.Alpha(0.13)).Children(func() {
						ui.Textf(c, "−%d", deleted).Key("diff-del").Font("monospace").FontSize(10.5).Bold().TextColor(t.Danger).
							ContentTransition(ui.ContentTransition{Numeric: true})
					})
				}
			})
		}

		ui.Spacer(c)

		if a.diffKind == diffWorking && a.diffFileStat != nil {
			st := *a.diffFileStat
			if st.Staged {
				if ghostBtn(c, "diff-unstage", IconMinus, "Unstage", "Unstage this file", false) {
					a.runGitOp(perf.OpStage, "Unstage", func(root string) error { return GitUnstageFile(root, st.Path) })
				}
			} else {
				if ghostBtn(c, "diff-stage", IconPlus, "Stage", "Stage this file", false) {
					a.runGitOp(perf.OpStage, "Stage", func(root string) error { return GitStageFile(root, st.Path) })
				}
			}
			if ghostBtn(c, "diff-open-file", IconFiles, "File", "Read full file in Explorer", false) {
				a.selectExplorerFile(st.Path)
				a.activeMode = modeFiles
				a.contentSeq++
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
	diffWrap := ui.Column(c.Key("diff-viewport")).Grow(1).FillWidth()
	k := diffWrap.Keyframes("enter", []ui.Keyframe{
		{At: 0, Value: 0},
		{At: 140 * time.Millisecond, Value: 1, Ease: ui.EaseOut},
	}, ui.KeyframeOptions{Run: a.contentSeq})
	diffWrap.Opacity(0.75+0.25*k).Translate(0, (1-k)*3)
	diffWrap.Children(func() {
		editor.DiffView(c, a.diffState).Grow(1).FillWidth()
	})
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
		ui.Column(c).Key("modal-new-branch").Width(340).Padding(16).Gap(12).Radius(10).
			Background(t.Surface).Border(1, t.Border).
			Transition(ui.ElementTransition{
				Duration: 150 * time.Millisecond,
				Enter:    &ui.Motion{Opacity: 0, Y: -6, Scale: 0.98},
				Exit:     &ui.Motion{Opacity: 0, Y: -4, Scale: 0.98},
			}).Children(func() {
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
					a.checkoutBranch(name, true)
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
				a.selectedFile = ""
				a.diffKind = diffNone
				a.openDirs = make(map[string]bool)
				a.reloadRepoAsync(picked)
			})
		}
	}()
}

func main() {
	beginStartup()
	startDir := ""
	if len(os.Args) > 1 {
		startDir = os.Args[1]
	}
	a := newApp(startDir)
	a.useUserServices()
	mygo.App.SetMenu(newAppMenu(a.cards.settings, func() {
		if a.win != nil {
			a.win.Invalidate()
		}
	}))

	mygo.App.WhenReady(func() {
		a.startupFirstFrame()
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
		a.attachWindow()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
