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
	tabExplorer = 0
	tabGit      = 1
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
	activeTab int // 0 = Explorer, 1 = Git & Graph
	statusMsg string

	// Modals
	newBranchOpen bool
	newBranchName string
	stashModal    bool
	stashMsg      string

	// View 1: Explorer state
	explorerSplitW  float32
	explorerOutline ui.OutlineState[string]
	explorerRow     int
	explorerFilter  ui.ListState
	explorerFiltRow int
	explorerQuery   string
	onlyChanged     bool
	selectedFile    string
	codeState       *editor.State
	codeSoftWrap    bool

	// View 2: Git & Graph state
	gitSplitW         float32
	gitLeftSplitPct   float32
	commitMsg         string
	commitAmend       bool
	changesList       ui.ListState
	changesRow        int
	graphList         ui.ListState
	graphRow          int
	graphQuery        string
	selectedCommitIdx int
	selectedCommit    *Commit
	commitBody        string
	commitFiles       []CommitFile
	selectedCommitF   int

	// Right pane Diff Viewer state (strictly read-only)
	diffKind      diffTargetKind
	diffTitle     string
	diffSub       string
	diffBadge     string
	diffFileStat  *FileStatus
	diffState     *editor.DiffState
	diffLayout    int // 0 = Split, 1 = Unified
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
		repoDir:           initialDir,
		activeTab:         tabExplorer,
		explorerSplitW:    270,
		explorerRow:       -1,
		explorerFiltRow:   -1,
		gitSplitW:         380,
		gitLeftSplitPct:   44,
		changesRow:        -1,
		graphRow:          -1,
		selectedCommitIdx: -1,
		selectedCommitF:   -1,
		codeState: &editor.State{
			Buffer:       codeBuf,
			ReadOnly:     true,
			NoAutoClose:  true,
			CursorShape:  editor.CursorBar,
			LineHeight:   1.55,
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

	// Expand root level folders by default on first load
	if a.explorerOutline.Open.Len() == 0 {
		for _, r := range snap.TreeRoots {
			if _, isDir := snap.TreeDirs[r]; isDir {
				a.explorerOutline.Open.Add(r)
				break
			}
		}
	}

	// Ensure a file is open in Explorer
	if a.selectedFile == "" && len(snap.AllFiles) > 0 {
		pick := snap.AllFiles[0]
		for _, f := range snap.AllFiles {
			if strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "README.md") {
				pick = f
				break
			}
		}
		a.selectExplorerFile(pick)
	} else if a.selectedFile != "" {
		a.selectExplorerFile(a.selectedFile)
	}

	// Ensure a diff target is loaded in Git view
	if a.diffKind == diffNone {
		if len(snap.Unstaged) > 0 {
			a.selectWorkingFile(snap.Unstaged[0])
		} else if len(snap.Staged) > 0 {
			a.selectWorkingFile(snap.Staged[0])
		} else if len(snap.Commits) > 0 {
			a.selectCommit(0)
		}
	} else if a.diffKind == diffWorking && a.diffFileStat != nil {
		// Refresh working diff buffer
		a.selectWorkingFile(*a.diffFileStat)
	}
}

func (a *app) runAsyncGit(label string, fn func(root string) error) {
	if a.repo == nil {
		return
	}
	root := a.repo.Root
	a.busyText = label
	if a.win == nil {
		err := fn(root)
		a.busyText = ""
		if err != nil {
			a.statusMsg = err.Error()
		} else {
			a.statusMsg = label + " completed"
		}
		a.refreshRepo()
		return
	}
	go func() {
		err := fn(root)
		a.win.Update(func() {
			a.busyText = ""
			if err != nil {
				a.statusMsg = err.Error()
			} else {
				a.statusMsg = label + " completed"
			}
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
	if st.Staged {
		a.diffSub = "Staged Change (HEAD → Index)"
	} else if st.Untrack {
		a.diffSub = "Untracked File"
	} else {
		a.diffSub = "Working Tree (Index → Working Tree)"
	}
	a.diffBadge = st.Badge()

	oldText, newText := LoadWorkingDiffTexts(a.repo.Root, st)
	a.setDiffBuffers(st.Path, oldText, newText)
}

func (a *app) selectCommit(idx int) {
	if a.repo == nil || idx < 0 || idx >= len(a.repo.Commits) {
		return
	}
	c := a.repo.Commits[idx]
	a.selectedCommitIdx = idx
	a.selectedCommit = &c
	a.graphRow = idx
	body, files, err := LoadCommitDetails(a.repo.Root, c.Hash)
	if err != nil {
		a.commitBody = c.Subject
		a.commitFiles = nil
		return
	}
	a.commitBody = body
	a.commitFiles = files
	if len(files) > 0 {
		a.selectCommitFile(0)
	} else {
		a.selectedCommitF = -1
		a.diffKind = diffCommitFile
		a.diffFileStat = nil
		a.diffTitle = c.ShortHash
		a.diffSub = c.Subject
		a.diffBadge = ""
		a.setDiffBuffers("", "", "")
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
	a.diffSub = fmt.Sprintf("Commit %s · %s", a.selectedCommit.ShortHash, a.selectedCommit.Subject)
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
		t.Surface = ui.Hex("#12141A")
		t.SurfaceHover = ui.Hex("#1A1D26")
		t.SurfacePressed = ui.Hex("#222632")
		t.Border = ui.Hex("#1F222B")
		t.Text = ui.Hex("#ECECEE")
		t.TextMuted = ui.Hex("#7E8494")
		t.Accent = ui.Hex("#5E6AD2")
		t.AccentHover = ui.Hex("#6E7AE2")
		t.AccentPressed = ui.Hex("#4E5AC0")
		t.AccentText = ui.Hex("#FFFFFF")
		t.Success = ui.Hex("#4CB782")
		t.Warning = ui.Hex("#F2994A")
		t.Danger = ui.Hex("#EB5757")
		t.Selection = ui.RGBA(94, 106, 210, 0.25)
		t.Focus = ui.Hex("#5E6AD2")
	} else {
		t.Background = ui.Hex("#FFFFFF")
		t.Surface = ui.Hex("#F6F7F9")
		t.SurfaceHover = ui.Hex("#ECEEF2")
		t.SurfacePressed = ui.Hex("#E2E5EC")
		t.Border = ui.Hex("#E4E6EB")
		t.Text = ui.Hex("#16181D")
		t.TextMuted = ui.Hex("#6B7080")
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

func renderStatusPill(c *ui.Context, badge string) {
	if badge == "" {
		return
	}
	t := c.Theme()
	col := statusColor(t, badge)
	ui.Row(c).Padding(1, 5).Radius(4).Background(col.Alpha(0.14)).Children(func() {
		ui.Text(c, badge).FontSize(10).Bold().TextColor(col)
	})
}

func (a *app) view(c *ui.Context) {
	t := applyLinearTheme(c)

	// Global keyboard shortcuts
	if c.Shortcut(ui.Cmd, ui.Key1) {
		a.activeTab = tabExplorer
	}
	if c.Shortcut(ui.Cmd, ui.Key2) {
		a.activeTab = tabGit
	}
	if c.Shortcut(ui.Cmd, ui.KeyR) {
		a.refreshRepo()
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		if a.activeTab == tabExplorer {
			a.codeState.Run(editor.Find)
		} else {
			a.diffState.Run(editor.Find)
		}
	}

	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.topBarView(c, t)
		if a.repo == nil {
			a.emptyRepoView(c, t)
			return
		}
		if a.activeTab == tabExplorer {
			a.explorerView(c, t)
		} else {
			a.gitWorkspaceView(c, t)
		}
		a.statusBarView(c, t)
	})

	a.modalsView(c, t)
}

func (a *app) topBarView(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	h := max(bar.Height, 40)
	leftPad := max(bar.Left+12, 14)
	rightPad := max(bar.Right+12, 14)

	ui.Row(c).Height(h).FillWidth().Padding(0, rightPad, 0, leftPad).Gap(10).
		Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().AlignItems(ui.Center).Children(func() {

		// Brand & Repository Switcher
		repoName := filepath.Base(a.repoDir)
		if a.repo != nil {
			repoName = filepath.Base(a.repo.Root)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(t.Accent)
			if ui.Button(c, repoName).Tooltip("Open another Git repository").Clicked() {
				a.openRepoDialog()
			}
		})

		// Branch Menu Button
		if a.repo != nil {
			branchLabel := "⎇ " + a.repo.Branch
			if a.repo.Ahead > 0 || a.repo.Behind > 0 {
				branchLabel += fmt.Sprintf("  ↑%d ↓%d", a.repo.Ahead, a.repo.Behind)
			}
			ui.PopoverMenuButton(c, branchLabel, func(m *ui.Menu) {
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
		}

		ui.Spacer(c)

		// Center View Mode Switcher: 1. Explorer  |  2. Git & Graph
		changedCount := 0
		if a.repo != nil {
			changedCount = len(a.repo.Staged) + len(a.repo.Unstaged)
		}
		gitTabLabel := "Git & Graph"
		if changedCount > 0 {
			gitTabLabel = fmt.Sprintf("Git & Graph (%d)", changedCount)
		}
		ui.Segmented(c, &a.activeTab, "Explorer", gitTabLabel).Label("Workspace Mode")

		ui.Spacer(c)

		// Right Daily Git Actions
		if a.repo != nil {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "Fetch").Disabled(a.busyText != "").Tooltip("git fetch --all --prune").Clicked() {
					a.runAsyncGit("Fetch", GitFetch)
				}
				if ui.Button(c, "Pull").Disabled(a.busyText != "").Tooltip("git pull --rebase").Clicked() {
					a.runAsyncGit("Pull", GitPull)
				}
				if ui.Button(c, "Push").Disabled(a.busyText != "").Tooltip("git push").Clicked() {
					a.runAsyncGit("Push", GitPush)
				}

				stashLabel := "Stash"
				if len(a.repo.Stashes) > 0 {
					stashLabel = fmt.Sprintf("Stash (%d)", len(a.repo.Stashes))
				}
				ui.PopoverMenuButton(c, stashLabel, func(m *ui.Menu) {
					if m.Item("Stash Changes…").Disabled(changedCount == 0).Chosen() {
						a.stashMsg = ""
						a.stashModal = true
					}
					if len(a.repo.Stashes) > 0 {
						if m.Item("Pop Latest Stash").Chosen() {
							a.runAsyncGit("Stash Pop", func(root string) error {
								return GitStashPop(root, a.repo.Stashes[0].Ref)
							})
						}
						m.Separator()
						for _, st := range a.repo.Stashes {
							ref, msg := st.Ref, st.Message
							m.Submenu(ref+": "+msg, func(sub *ui.Menu) {
								if sub.Item("Pop " + ref).Chosen() {
									a.runAsyncGit("Pop "+ref, func(root string) error {
										return GitStashPop(root, ref)
									})
								}
								if sub.Item("Drop " + ref).Chosen() {
									a.runAsyncGit("Drop "+ref, func(root string) error {
										return GitStashDrop(root, ref)
									})
								}
							})
						}
					}
				})

				if ui.Button(c, "↻").Tooltip("Refresh repository (⌘R)").Clicked() {
					a.refreshRepo()
				}
			})
		}
	})
}

func (a *app) emptyRepoView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Center().Gap(12).Padding(32).Children(func() {
		ui.Text(c, "No Git Repository Loaded").FontSize(18).Bold()
		if a.repoErr != "" {
			ui.Text(c, a.repoErr).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(10).Children(func() {
			if ui.PrimaryButton(c, "Open Repository Folder…").Clicked() {
				a.openRepoDialog()
			}
			if ui.Button(c, "Initialize Git Here (git init)").Clicked() {
				if _, err := runGit(a.repoDir, "init"); err == nil {
					a.refreshRepo()
				} else {
					a.repoErr = err.Error()
				}
			}
		})
	})
}

// ============================================================================
// Screen 1: Explorer (Left Directory Tree + Right Read-Only Code Viewer)
// ============================================================================

func (a *app) explorerView(c *ui.Context, t *ui.Theme) {
	ui.Split(c, &a.explorerSplitW, func() {
		a.explorerSidebar(c, t)
	}, func() {
		a.explorerCodePane(c, t)
	}).Grow(1).FillWidth()
}

func (a *app) explorerSidebar(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		// Filter & Search header
		ui.Column(c).Padding(10, 10, 8, 10).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "FILES").FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
				ui.Toggle(c, &a.onlyChanged, "Modified Only")
			})
			ui.SearchField(c, &a.explorerQuery).Placeholder("Filter files…").Label("Filter files").FillWidth()
		})

		query := strings.ToLower(strings.TrimSpace(a.explorerQuery))
		if query != "" || a.onlyChanged {
			var filtered []string
			for _, f := range a.repo.AllFiles {
				if a.onlyChanged && a.repo.StatusByRel[f] == "" {
					continue
				}
				if query != "" && !strings.Contains(strings.ToLower(f), query) {
					continue
				}
				filtered = append(filtered, f)
			}

			a.explorerFilter.Selected = &a.explorerFiltRow
			a.explorerFilter.Key = func(i int) any { return filtered[i] }
			list := ui.List(c, &a.explorerFilter, len(filtered), func(i int) {
				rel := filtered[i]
				badge := a.repo.StatusByRel[rel]
				dir := filepath.Dir(rel)
				if dir == "." {
					dir = ""
				}
				ui.Row(c).Padding(5, 10).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, filepath.Base(rel)).SingleLine().Grow(1)
					if dir != "" {
						ui.Text(c, dir).FontSize(11).TextColor(t.TextMuted).SingleLine()
					}
					renderStatusPill(c, badge)
				})
			}).Grow(1)
			if list.Changed() && a.explorerFiltRow >= 0 && a.explorerFiltRow < len(filtered) {
				a.selectExplorerFile(filtered[a.explorerFiltRow])
			}
			return
		}

		// Full virtualized directory tree (ui.Outline)
		a.explorerOutline.List.Selected = &a.explorerRow
		a.explorerOutline.Indent = 14
		outline := ui.Outline(c, &a.explorerOutline, a.repo.TreeRoots, func(path string) []string {
			return a.repo.TreeDirs[path]
		}, func(path string) {
			_, isDir := a.repo.TreeDirs[path]
			badge := a.repo.StatusByRel[path]
			ui.Row(c).Padding(4, 8).Gap(6).AlignItems(ui.Center).FillWidth().Children(func() {
				if isDir {
					ui.Text(c, filepath.Base(path)).Bold().SingleLine().Grow(1)
					if badge != "" {
						ui.Box(c).Size(6, 6).Radius(3).Background(t.Warning)
					}
				} else {
					ui.Text(c, filepath.Base(path)).SingleLine().Grow(1)
					renderStatusPill(c, badge)
				}
			})
		}).Grow(1)

		if outline.Changed() && a.explorerRow >= 0 && a.explorerRow < a.explorerOutline.Rows() {
			chosen := a.explorerOutline.Item(a.explorerRow)
			if _, isDir := a.repo.TreeDirs[chosen]; !isDir {
				a.selectExplorerFile(chosen)
			}
		}
	})
}

func (a *app) explorerCodePane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		if a.selectedFile == "" {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "Select a file from the tree to inspect").TextColor(t.TextMuted)
			})
			return
		}

		badge := a.repo.StatusByRel[a.selectedFile]
		// File Header Bar
		ui.Row(c).Height(38).Padding(0, 14).Gap(8).AlignItems(ui.Center).
			Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			dir := filepath.Dir(a.selectedFile)
			if dir != "." {
				ui.Text(c, dir+" /").TextColor(t.TextMuted).SingleLine()
			}
			ui.Text(c, filepath.Base(a.selectedFile)).Bold().SingleLine()
			renderStatusPill(c, badge)

			ui.Row(c).Padding(2, 6).Radius(4).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, "READ-ONLY").FontSize(10).Bold().TextColor(t.TextMuted)
			})

			ui.Spacer(c)

			if badge != "" {
				if ui.PrimaryButton(c, "View Diff →").Clicked() {
					// Find file in Unstaged or Staged and switch to Git view
					for _, st := range a.repo.Unstaged {
						if st.Path == a.selectedFile {
							a.selectWorkingFile(st)
							a.activeTab = tabGit
							return
						}
					}
					for _, st := range a.repo.Staged {
						if st.Path == a.selectedFile {
							a.selectWorkingFile(st)
							a.activeTab = tabGit
							return
						}
					}
				}
			}

			if ui.Toggle(c, &a.codeSoftWrap, "Wrap").Changed() {
				a.codeState.SoftWrap = a.codeSoftWrap
			}
			if ui.Button(c, "Find (⌘F)").Clicked() {
				a.codeState.Run(editor.Find)
			}
		})

		a.codeState.ReadOnly = true
		a.codeState.SoftWrap = a.codeSoftWrap
		editor.View(c, a.codeState).Grow(1).FillWidth()
	})
}

// ============================================================================
// Screen 2: Git & Graph (Left Changes + Commit Graph | Right Read-Only Diff)
// ============================================================================

func (a *app) gitWorkspaceView(c *ui.Context, t *ui.Theme) {
	ui.Split(c, &a.gitSplitW, func() {
		ui.SplitVerticalPercent(c, &a.gitLeftSplitPct, func() {
			a.gitChangesPane(c, t)
		}, func() {
			a.gitGraphListPane(c, t)
		}).Fill()
	}, func() {
		a.gitDiffPane(c, t)
	}).Grow(1).FillWidth()
}

type changeRowItem struct {
	isHeader bool
	title    string
	status   FileStatus
}

func (a *app) gitChangesPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Surface).BorderWidth(0, 1, 1, 0).BorderColor(t.Border).Children(func() {
		// Commit box & Stage/Unstage toolbar
		ui.Column(c).Padding(10).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "SOURCE CONTROL").FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
				if len(a.repo.Staged) > 0 {
					if ui.Button(c, "Unstage All").Clicked() {
						a.runAsyncGit("Unstage All", GitUnstageAll)
					}
				}
				if len(a.repo.Unstaged) > 0 {
					if ui.Button(c, "Stage All").Clicked() {
						a.runAsyncGit("Stage All", GitStageAll)
					}
				}
			})

			sub := ui.TextInput(c, &a.commitMsg).Placeholder("Commit message…").Label("Commit message").FillWidth().Submitted()
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Checkbox(c, &a.commitAmend, "Amend")
				ui.Spacer(c)
				canCommit := strings.TrimSpace(a.commitMsg) != "" && (len(a.repo.Staged) > 0 || len(a.repo.Unstaged) > 0 || a.commitAmend)
				if (ui.PrimaryButton(c, "Commit").Disabled(!canCommit).Clicked() || sub) && canCommit {
					msg := strings.TrimSpace(a.commitMsg)
					amend := a.commitAmend
					needAutoStage := len(a.repo.Staged) == 0 && len(a.repo.Unstaged) > 0
					a.commitMsg = ""
					a.commitAmend = false
					a.runAsyncGit("Commit", func(root string) error {
						if needAutoStage {
							if err := GitStageAll(root); err != nil {
								return err
							}
						}
						return GitCommit(root, msg, amend)
					})
				}
			})
		})

		// Build combined Staged + Working Tree rows
		var rows []changeRowItem
		if len(a.repo.Staged) > 0 {
			rows = append(rows, changeRowItem{
				isHeader: true,
				title:    fmt.Sprintf("STAGED CHANGES (%d)", len(a.repo.Staged)),
			})
			for _, st := range a.repo.Staged {
				rows = append(rows, changeRowItem{status: st})
			}
		}
		if len(a.repo.Unstaged) > 0 {
			rows = append(rows, changeRowItem{
				isHeader: true,
				title:    fmt.Sprintf("WORKING TREE CHANGES (%d)", len(a.repo.Unstaged)),
			})
			for _, st := range a.repo.Unstaged {
				rows = append(rows, changeRowItem{status: st})
			}
		}

		if len(rows) == 0 {
			ui.Column(c).Grow(1).Center().Padding(16).Children(func() {
				ui.Text(c, "Working tree clean").TextColor(t.TextMuted)
			})
			return
		}

		a.changesList.Selected = &a.changesRow
		list := ui.List(c, &a.changesList, len(rows), func(i int) {
			item := rows[i]
			if item.isHeader {
				ui.Row(c).Padding(6, 10, 4, 10).Background(t.Surface).Children(func() {
					ui.Text(c, item.title).FontSize(10).Bold().TextColor(t.TextMuted)
				})
				return
			}
			st := item.status
			row := ui.Row(c).Padding(4, 10).Gap(6).AlignItems(ui.Center)
			hovered := row.Hovered()
			row.Children(func() {
				ui.Text(c, filepath.Base(st.Path)).SingleLine().Grow(1)
				dir := filepath.Dir(st.Path)
				if dir != "." {
					ui.Text(c, dir).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
				if hovered {
					if st.Staged {
						if ui.Button(c, "−").Tooltip("Unstage file").Clicked() {
							p := st.Path
							a.runAsyncGit("Unstage "+filepath.Base(p), func(root string) error {
								return GitUnstageFile(root, p)
							})
						}
					} else {
						if ui.Button(c, "↺").Tooltip("Discard changes").Clicked() {
							target := st
							a.runAsyncGit("Discard "+filepath.Base(target.Path), func(root string) error {
								return GitDiscardFile(root, target)
							})
						}
						if ui.Button(c, "+").Tooltip("Stage file").Clicked() {
							p := st.Path
							a.runAsyncGit("Stage "+filepath.Base(p), func(root string) error {
								return GitStageFile(root, p)
							})
						}
					}
				}
				renderStatusPill(c, st.Badge())
			})
		}).Grow(1)

		if list.Changed() && a.changesRow >= 0 && a.changesRow < len(rows) {
			picked := rows[a.changesRow]
			if !picked.isHeader {
				a.selectWorkingFile(picked.status)
			}
		}
	})
}

func (a *app) gitGraphListPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		// Header + Search
		ui.Column(c).Padding(8, 10).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("COMMIT GRAPH (%d)", len(a.repo.Commits))).FontSize(11).Bold().TextColor(t.TextMuted).Grow(1)
			})
			ui.SearchField(c, &a.graphQuery).Placeholder("Search commits, hashes, authors…").Label("Search commits").FillWidth()
		})

		q := strings.ToLower(strings.TrimSpace(a.graphQuery))
		var indices []int
		for i, cm := range a.repo.Commits {
			if q != "" {
				match := strings.Contains(strings.ToLower(cm.Subject), q) ||
					strings.Contains(strings.ToLower(cm.ShortHash), q) ||
					strings.Contains(strings.ToLower(cm.Author), q)
				if !match {
					continue
				}
			}
			indices = append(indices, i)
		}

		a.graphList.Selected = &a.graphRow
		list := ui.List(c, &a.graphList, len(indices), func(rowIdx int) {
			cIdx := indices[rowIdx]
			cm := a.repo.Commits[cIdx]
			gRow := a.repo.Graph[cIdx]

			graphW := float32(max(gRow.MaxCols, 2)*14 + 14)
			if graphW > 110 {
				graphW = 110
			}

			ui.Row(c).Height(32).Padding(0, 10, 0, 0).Gap(6).AlignItems(ui.Center).Children(func() {
				// GPU Bezier Commit Graph Lane Renderer
				if q == "" {
					ui.Box(c).Width(graphW).FillHeight().Draw(func(p *ui.Painter, r ui.Rect) {
						DrawGraphCell(p, r, gRow, t.Surface)
					})
				} else {
					ui.Box(c).Width(12)
				}

				// Ref badges (HEAD, branches, tags)
				for _, ref := range cm.Refs {
					isHead := strings.Contains(ref, "HEAD")
					pillColor := GraphColor(gRow.NodeColor)
					if isHead {
						pillColor = t.Accent
					}
					ui.Row(c).Padding(1, 6).Radius(4).Background(pillColor.Alpha(0.18)).Border(1, pillColor.Alpha(0.45)).Children(func() {
						ui.Text(c, ref).FontSize(10).Bold().TextColor(pillColor).SingleLine()
					})
				}

				// Commit subject
				ui.Text(c, cm.Subject).SingleLine().Grow(1)

				// Short hash + date
				ui.Text(c, cm.ShortHash).Font("monospace").FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		}).Grow(1)

		if list.Changed() && a.graphRow >= 0 && a.graphRow < len(indices) {
			a.selectCommit(indices[a.graphRow])
		}
	})
}

func (a *app) gitDiffPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		// If inspecting a Commit, render Commit Inspector Banner + Changed Files Strip at the top
		if a.diffKind == diffCommitFile && a.selectedCommit != nil {
			cm := a.selectedCommit
			ui.Column(c).Padding(10, 14).Gap(8).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Row(c).Padding(2, 6).Radius(4).Background(t.Accent.Alpha(0.16)).Children(func() {
						ui.Text(c, cm.ShortHash).Font("monospace").FontSize(11).Bold().TextColor(t.Accent)
					})
					ui.Text(c, cm.Subject).Bold().SingleLine().Grow(1)
					ui.Text(c, cm.Author+" · "+cm.Date).FontSize(12).TextColor(t.TextMuted).SingleLine()
				})

				// Changed files strip for this commit (click any file to view its Diff)
				if len(a.commitFiles) > 0 {
					ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("Changed (%d):", len(a.commitFiles))).FontSize(11).TextColor(t.TextMuted)
						for i, cf := range a.commitFiles {
							idx := i
							active := idx == a.selectedCommitF
							pill := ui.Row(c.Key(cf.Path)).Padding(3, 8).Radius(5).Gap(5).AlignItems(ui.Center).Cursor(ui.CursorPointer)
							if active {
								pill.Background(t.Accent.Alpha(0.2)).Border(1, t.Accent)
							} else {
								pill.Background(t.SurfaceHover).Border(1, t.Border)
							}
							if pill.Clicked() {
								a.selectCommitFile(idx)
							}
							pill.Children(func() {
								renderStatusPill(c, cf.Status)
								ui.Text(c, cf.Path).FontSize(12).SingleLine()
							})
						}
					})
				}
			})
		}

		if a.diffKind == diffNone {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "Select a modified file or commit to inspect its Diff").TextColor(t.TextMuted)
			})
			return
		}

		// Diff Toolbar
		added, deleted := a.diffState.Stats()
		hunks := a.diffState.Hunks()
		curHunk, _ := a.diffState.CurrentHunk()

		ui.Row(c).Height(38).Padding(0, 14).Gap(8).AlignItems(ui.Center).
			Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			renderStatusPill(c, a.diffBadge)
			ui.Text(c, a.diffTitle).Bold().SingleLine()
			ui.Text(c, a.diffSub).FontSize(11).TextColor(t.TextMuted).SingleLine()

			if added > 0 || deleted > 0 {
				ui.Row(c).Gap(4).Padding(2, 6).Radius(4).Background(t.SurfaceHover).Children(func() {
					ui.Textf(c, "+%d", added).FontSize(11).Bold().TextColor(t.Success)
					ui.Textf(c, "−%d", deleted).FontSize(11).Bold().TextColor(t.Danger)
				})
			}

			ui.Spacer(c)

			// Stage / Unstage / Open in Explorer actions for working files
			if a.diffKind == diffWorking && a.diffFileStat != nil {
				st := *a.diffFileStat
				if st.Staged {
					if ui.Button(c, "Unstage File").Clicked() {
						a.runAsyncGit("Unstage "+filepath.Base(st.Path), func(root string) error {
							return GitUnstageFile(root, st.Path)
						})
					}
				} else {
					if ui.PrimaryButton(c, "Stage File").Clicked() {
						a.runAsyncGit("Stage "+filepath.Base(st.Path), func(root string) error {
							return GitStageFile(root, st.Path)
						})
					}
				}
				if ui.Button(c, "Open File").Tooltip("View full file in Explorer").Clicked() {
					a.selectExplorerFile(st.Path)
					a.activeTab = tabExplorer
				}
			}

			// Hunk navigation
			if len(hunks) > 0 {
				if ui.Button(c, "↑").Tooltip("Previous change").Clicked() {
					prev := curHunk - 1
					if prev < 0 {
						prev = len(hunks) - 1
					}
					a.diffState.GoToHunk(prev)
				}
				if ui.Button(c, "↓").Tooltip("Next change").Clicked() {
					next := curHunk + 1
					if next >= len(hunks) {
						next = 0
					}
					a.diffState.GoToHunk(next)
				}
			}

			if ui.Toggle(c, &a.diffExpandAll, "All Lines").Changed() {
				a.diffState.ExpandUnchanged = a.diffExpandAll
			}
			if ui.Toggle(c, &a.diffWrap, "Wrap").Changed() {
				a.diffState.Wrap = a.diffWrap
			}
			if ui.Segmented(c, &a.diffLayout, "Split", "Unified").Label("Diff layout").Changed() {
				if a.diffLayout == 0 {
					a.diffState.Layout = editor.DiffSplit
				} else {
					a.diffState.Layout = editor.DiffUnified
				}
			}
		})

		a.diffState.Editable = false
		a.diffState.Wrap = a.diffWrap
		a.diffState.ExpandUnchanged = a.diffExpandAll
		if a.diffLayout == 0 {
			a.diffState.Layout = editor.DiffSplit
		} else {
			a.diffState.Layout = editor.DiffUnified
		}
		editor.DiffView(c, a.diffState).Grow(1).FillWidth()
	})
}

func (a *app) statusBarView(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Height(24).FillWidth().Padding(0, 12).Gap(12).AlignItems(ui.Center).
		Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.repo != nil {
			ui.Text(c, a.repo.Root).FontSize(11).TextColor(t.TextMuted).SingleLine()
			ui.Textf(c, "%d files · %d commits", len(a.repo.AllFiles), len(a.repo.Commits)).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c)
		if a.busyText != "" {
			ui.Spinner(c)
			ui.Text(c, a.busyText+"…").FontSize(11).TextColor(t.Accent)
		} else if a.statusMsg != "" {
			ui.Text(c, a.statusMsg).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		ui.Text(c, "⌘1 Explorer   ⌘2 Git & Graph   ⌘F Find   ⌘R Refresh").FontSize(11).TextColor(t.TextMuted)
	})
}

func (a *app) modalsView(c *ui.Context, t *ui.Theme) {
	// Create Branch Modal
	ui.Modal(c, &a.newBranchOpen, func() {
		ui.Column(c).Width(360).Padding(18).Gap(12).Radius(10).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "Create & Checkout Branch").FontSize(15).Bold()
			sub := ui.TextInput(c, &a.newBranchName).Placeholder("feature/branch-name").Label("Branch name").AutoFocus().FillWidth().Submitted()
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.newBranchOpen = false
				}
				name := strings.TrimSpace(a.newBranchName)
				if (ui.PrimaryButton(c, "Create Branch").Disabled(name == "").Clicked() || sub) && name != "" {
					a.newBranchOpen = false
					a.runAsyncGit("Create branch "+name, func(root string) error {
						return GitCheckoutBranch(root, name, true)
					})
				}
			})
		})
	})

	// Stash Modal
	ui.Modal(c, &a.stashModal, func() {
		ui.Column(c).Width(360).Padding(18).Gap(12).Radius(10).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "Stash Working Tree Changes").FontSize(15).Bold()
			sub := ui.TextInput(c, &a.stashMsg).Placeholder("Optional stash description…").Label("Stash message").AutoFocus().FillWidth().Submitted()
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.stashModal = false
				}
				if ui.PrimaryButton(c, "Stash").Clicked() || sub {
					msg := a.stashMsg
					a.stashModal = false
					a.runAsyncGit("Stash push", func(root string) error {
						return GitStashPush(root, msg)
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
			Title:         "Arbor",
			Width:         1320,
			Height:        840,
			MinWidth:      860,
			MinHeight:     540,
			StateKey:      "arbor-main",
			TitleBarStyle: mygo.TitleBarHidden,
			Content:       ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
