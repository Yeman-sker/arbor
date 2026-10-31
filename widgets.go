package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

var (
	// Fast, restrained color crossfade (120ms ease-out) for hover/active states.
	microColorAnim = ui.Animation{Duration: 120 * time.Millisecond, Ease: ui.EaseOut}
	// Critically damped spring (zero bounce) for hover glides, widths, and focus rings.
	microSpring = ui.SpringOptions{Duration: 180 * time.Millisecond, Bounce: 0}
	poseAnim    = ui.Animation{Spring: microSpring}
	// Snappy tactile spring (subtle bounce) for segmented pills, selection indicators, and icon pops.
	snappySpring   = ui.SpringOptions{Duration: 220 * time.Millisecond, Bounce: 0.16}
	snappyPoseAnim = ui.Animation{Spring: snappySpring}
)

type actionTone int

const (
	actionToneDefault actionTone = iota
	actionToneSuccess
	actionToneWarning
	actionToneDanger
)

// segmentedModeSwitcher renders a tactile sliding-pill segmented control for Files | Git.
func segmentedModeSwitcher(c *ui.Context, activeMode int, gitBadge int, onSelect func(mode int)) {
	t := c.Theme()
	trackBg := t.Background.Alpha(0.55)
	if t.Dark {
		trackBg = ui.RGBA(0, 0, 0, 0.28)
	}
	seg := ui.Row(c.Key("mode-seg")).Height(26).Padding(2).Gap(2).Radius(6).Shrink(0).
		Background(trackBg).Border(1, t.Border).AlignItems(ui.Center)

	targetSlide := float32(0)
	if activeMode == modeGit {
		targetSlide = 1
	}
	slide := seg.Spring("slide", targetSlide, snappySpring)

	// Draw physical sliding thumb behind the active segment
	thumbBg := t.Surface
	if t.Dark {
		thumbBg = ui.RGBA(255, 255, 255, 0.10)
	} else {
		thumbBg = ui.Hex("#FFFFFF")
	}
	thumbBorder := t.Border
	if t.Dark {
		thumbBorder = ui.RGBA(255, 255, 255, 0.14)
	}
	seg.Draw(func(p *ui.Painter, r ui.Rect) {
		innerW := r.W - 4
		halfW := (innerW - 2) * 0.5
		x := r.X + 2 + slide*(halfW+2)
		thumbRect := ui.Rect{X: x, Y: r.Y + 2, W: halfW, H: r.H - 4}
		p.Fill(thumbRect, thumbBg, 4.5)
		p.Stroke(thumbRect, thumbBorder, 4.5, 1)
	})

	seg.Children(func() {
		renderSegTab(c, "seg-files", IconFiles, "Files", 0, "Explorer (⌘1)", activeMode == modeFiles, func() {
			onSelect(modeFiles)
		})
		renderSegTab(c, "seg-git", IconGitBranch, "Git", gitBadge, "Source Control & Graph (⌘2)", activeMode == modeGit, func() {
			onSelect(modeGit)
		})
	})
}

func renderSegTab(c *ui.Context, key string, ic *ui.SVG, label string, badge int, tip string, active bool, onClick func()) {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Width(62).FillHeight().Padding(0, 5).Gap(3.5).Radius(4.5).Shrink(0).
		Center().Cursor(ui.CursorPointer).Label(label).Tooltip(tip)

	targetFg := t.TextMuted
	if active {
		targetFg = t.Text
	} else if btn.Hovered() {
		targetFg = t.Text.Alpha(0.85)
	}
	scale := float32(1.0)
	if btn.Pressed() {
		scale = 0.95
	}
	btn.TextColor(btn.AnimateColor("fg", targetFg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, poseAnim)

	icScale := float32(1.0)
	icColor := targetFg
	if active {
		icScale = 1.08
		icColor = t.Accent
	} else if btn.Hovered() {
		icScale = 1.05
	}

	btn.Children(func() {
		icEl := ui.Icon(c, ic).Size(12.5, 12.5).Shrink(0)
		icEl.TextColor(icEl.AnimateColor("ic", icColor, microColorAnim)).
			AnimatePose(ui.Pose{Scale: icScale}, snappyPoseAnim)

		txt := ui.Text(c, label).FontSize(11.5).SingleLine()
		if active {
			txt.FontWeight(500)
		}
		if badge > 0 {
			badgeBg := t.Accent.Alpha(0.16)
			badgeFg := t.Accent
			if !active {
				badgeBg = t.SurfaceHover
				badgeFg = t.TextMuted
			}
			pill := ui.Row(c).Padding(0.5, 4).Radius(6).AlignItems(ui.Center)
			pill.Background(pill.AnimateColor("bbg", badgeBg, microColorAnim))
			pill.Children(func() {
				ui.Text(c, fmt.Sprintf("%d", badge)).Key(key + "-cnt").Font("monospace").FontSize(9.5).Bold().
					TextColor(badgeFg).ContentTransition(ui.ContentTransition{Numeric: true})
			})
		}
	})
	if btn.Clicked() {
		onClick()
	}
}

// iconBtn renders a 25x25 borderless Linear-style icon button with contextual icon micro-animations.
func iconBtn(c *ui.Context, key string, ic *ui.SVG, tip string, active bool) bool {
	return iconBtnState(c, key, ic, tip, active, false)
}

// iconBtnState is iconBtn that can also be disabled: dimmed, without hover or
// press feedback, and never clicked.
func iconBtnState(c *ui.Context, key string, ic *ui.SVG, tip string, active, disabled bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Size(25, 25).Radius(5).Shrink(0).Center().
		Cursor(ui.CursorPointer).Label(tip).Tooltip(tip).Disabled(disabled)
	if disabled {
		btn.Opacity(0.4)
	}

	hovered := btn.Hovered() && !disabled
	pressed := btn.Pressed() && !disabled

	targetBg := t.Surface.Alpha(0)
	targetFg := t.TextMuted
	targetBd := t.Accent.Alpha(0)
	if active {
		targetBg = t.Accent.Alpha(0.12)
		targetFg = t.Accent
		targetBd = t.Accent.Alpha(0.28)
		if hovered {
			targetBg = t.Accent.Alpha(0.18)
		}
	} else if pressed {
		targetBg = t.SurfacePressed
		targetFg = t.Text
	} else if hovered {
		targetBg = t.SurfaceHover
		targetFg = t.Text
	}

	scale := float32(1.0)
	if pressed {
		scale = 0.91
	} else if hovered {
		scale = 1.04
	}
	btn.Background(btn.AnimateColor("bg", targetBg, microColorAnim)).
		Border(1, btn.AnimateColor("bd", targetBd, microColorAnim)).
		TextColor(btn.AnimateColor("fg", targetFg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, poseAnim)

	// Contextual icon pose on hover/active
	var icPose ui.Pose
	icPose.Scale = 1.0
	if hovered {
		switch ic {
		case IconRefresh:
			icPose.Rotate = 50
			icPose.Scale = 1.08
		case IconArrowDown, IconChevronDown:
			icPose.Y = 1.6
		case IconArrowUp, IconChevronUp:
			icPose.Y = -1.6
		case IconExpand:
			icPose.Scale = 1.14
		case IconSearch:
			icPose.Rotate = -10
			icPose.Scale = 1.08
		case IconFilter:
			icPose.Y = 0.8
			icPose.Scale = 1.08
		default:
			icPose.Scale = 1.08
		}
	} else if active {
		icPose.Scale = 1.06
	}

	btn.Children(func() {
		ui.Icon(c, ic).Size(14, 14).AnimatePose(icPose, snappyPoseAnim)
	})
	return !disabled && btn.Clicked()
}

// rowIconBtn renders a default-tone 20x20 icon button inside a list row.
func rowIconBtn(c *ui.Context, key string, ic *ui.SVG, tip string) bool {
	return rowActionBtn(c, key, ic, tip, actionToneDefault)
}

// rowActionBtn renders a 20x20 icon action button with semantic color tint and icon motion on hover.
func rowActionBtn(c *ui.Context, key string, ic *ui.SVG, tip string, tone actionTone) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Size(20, 20).Radius(4).Shrink(0).Center().
		Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)

	hovered := btn.Hovered()
	pressed := btn.Pressed()

	accentCol := t.Text
	switch tone {
	case actionToneSuccess:
		accentCol = t.Success
	case actionToneWarning:
		accentCol = t.Warning
	case actionToneDanger:
		accentCol = t.Danger
	}

	targetBg := t.Surface.Alpha(0)
	targetFg := t.TextMuted
	if pressed {
		if tone == actionToneDefault {
			targetBg = t.SurfacePressed
		} else {
			targetBg = accentCol.Alpha(0.24)
		}
		targetFg = accentCol
	} else if hovered {
		if tone == actionToneDefault {
			targetBg = t.SurfacePressed.Alpha(0.8)
		} else {
			targetBg = accentCol.Alpha(0.15)
		}
		targetFg = accentCol
	}

	scale := float32(1.0)
	if pressed {
		scale = 0.88
	} else if hovered {
		scale = 1.08
	}
	btn.Background(btn.AnimateColor("bg", targetBg, microColorAnim)).
		TextColor(btn.AnimateColor("fg", targetFg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, snappyPoseAnim)

	var icPose ui.Pose
	icPose.Scale = 1.0
	if hovered {
		switch ic {
		case IconPlus:
			icPose.Rotate = 90
			icPose.Scale = 1.1
		case IconUndo:
			icPose.Rotate = -28
			icPose.Scale = 1.08
		case IconMinus:
			icPose.Scale = 1.1
		}
	}

	btn.Children(func() {
		ui.Icon(c, ic).Size(12.5, 12.5).AnimatePose(icPose, snappyPoseAnim)
	})
	return btn.Clicked()
}

// ghostBtn renders a refined borderless button with an icon, short label, and fluid hover nudge.
func ghostBtn(c *ui.Context, key string, ic *ui.SVG, label, tip string, active bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(24).Padding(0, 6).Gap(4.5).Radius(4.5).
		Shrink(1).MinWidth(0).Clip().
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if tip != "" {
		btn.Tooltip(tip)
	}

	hovered := btn.Hovered()
	pressed := btn.Pressed()

	targetBg := t.Surface.Alpha(0)
	targetFg := t.TextMuted
	targetBd := t.Accent.Alpha(0)
	if active {
		targetBg = t.Accent.Alpha(0.11)
		targetFg = t.Accent
		targetBd = t.Accent.Alpha(0.24)
	} else if pressed {
		targetBg = t.SurfacePressed
		targetFg = t.Text
	} else if hovered {
		targetBg = t.SurfaceHover
		targetFg = t.Text
	}

	scale := float32(1.0)
	if pressed {
		scale = 0.95
	}
	btn.Background(btn.AnimateColor("bg", targetBg, microColorAnim)).
		Border(1, btn.AnimateColor("bd", targetBd, microColorAnim)).
		TextColor(btn.AnimateColor("fg", targetFg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, poseAnim)

	icScale := float32(1.0)
	if hovered {
		icScale = 1.1
	}

	btn.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13).Shrink(0).AnimatePose(ui.Pose{Scale: icScale}, snappyPoseAnim)
		}
		txt := ui.Text(c, label).FontSize(12).SingleLine().Shrink(1).MinWidth(0).Clip()
		if active {
			txt.FontWeight(500)
		}
	})
	return btn.Clicked()
}

// ghostDropdownBtn renders a borderless Linear-style dropdown button with an
// icon, label, and animated chevron. It returns the button, for a popover to
// attach to, and whether it was clicked. A maxWidth of 0 lets the button
// shrink to the room it gets; any other keeps it at that width at most.
func ghostDropdownBtn(c *ui.Context, key string, ic *ui.SVG, label, tip string, maxWidth float32) (ui.Element, bool) {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 6).Gap(3.5).Radius(5).Clip().
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if maxWidth > 0 {
		btn.Shrink(0).MaxWidth(maxWidth)
	} else {
		btn.Shrink(1).MinWidth(0)
	}
	if tip != "" {
		btn.Tooltip(tip)
	}

	hovered := btn.Hovered()
	pressed := btn.Pressed()

	targetBg := t.Surface.Alpha(0)
	targetFg := t.TextMuted
	if pressed {
		targetBg = t.SurfacePressed
		targetFg = t.Text
	} else if hovered {
		targetBg = t.SurfaceHover
		targetFg = t.Text
	}

	scale := float32(1.0)
	if pressed {
		scale = 0.96
	}
	btn.Background(btn.AnimateColor("bg", targetBg, microColorAnim)).
		TextColor(btn.AnimateColor("fg", targetFg, microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, poseAnim)

	chevY := float32(0)
	icScale := float32(1.0)
	if hovered {
		chevY = 1.4
		icScale = 1.08
	}

	btn.Children(func() {
		if ic != nil {
			icEl := ui.Icon(c, ic).Size(13, 13).Shrink(0)
			if hovered {
				icEl.TextColor(t.Accent)
			}
			icEl.AnimatePose(ui.Pose{Scale: icScale}, snappyPoseAnim)
		}
		ui.Text(c, label).FontSize(12).SingleLine().Shrink(1).MinWidth(0).Clip()
		ch := ui.Icon(c, IconChevronDown).Size(11, 11).Shrink(0).TextColor(t.TextMuted)
		ch.Translate(0, ch.Spring("y", chevY, snappySpring))
	})
	return btn, btn.Clicked()
}

// primaryActionBtn renders a compact Linear indigo action button with hover lift, glow, and press compression.
func primaryActionBtn(c *ui.Context, key string, ic *ui.SVG, label string, disabled bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 9).Gap(5).Radius(5).Shrink(0).
		AlignItems(ui.Center).Label(label).Disabled(disabled)
	if disabled {
		btn.Background(btn.AnimateColor("bg", t.SurfaceHover, microColorAnim)).
			TextColor(t.TextMuted).
			Opacity(btn.AnimateTo("op", 0.48, microColorAnim)).
			AnimatePose(ui.Pose{Scale: 0.98}, poseAnim)
	} else {
		btn.Cursor(ui.CursorPointer)
		hovered := btn.Hovered()
		pressed := btn.Pressed()

		targetBg := t.Accent
		pose := ui.Pose{Scale: 1.0}
		glowTarget := float32(0)
		if pressed {
			targetBg = t.AccentPressed
			pose = ui.Pose{Y: 0.5, Scale: 0.95}
		} else if hovered {
			targetBg = t.AccentHover
			pose = ui.Pose{Y: -0.8, Scale: 1.02}
			glowTarget = 1
		}
		if glowK := btn.Spring("glow", glowTarget, microSpring); glowK > 0.02 {
			glowCol := t.Accent.Alpha(0.24 * glowK)
			btn.Draw(func(p *ui.Painter, r ui.Rect) {
				spread := 2.0 * glowK
				p.Stroke(ui.Rect{X: r.X - spread, Y: r.Y - spread, W: r.W + spread*2, H: r.H + spread*2}, glowCol, 5+spread, 1.5)
			})
		}
		btn.Background(btn.AnimateColor("bg", targetBg, microColorAnim)).
			TextColor(t.AccentText).
			Opacity(btn.AnimateTo("op", 1.0, microColorAnim)).
			AnimatePose(pose, snappyPoseAnim)
	}
	btn.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13)
		}
		ui.Text(c, label).FontSize(12).Bold().SingleLine()
	})
	return !disabled && btn.Clicked()
}

// compactInput renders a sleek 28px single-line input box with a blooming focus ring and icon pop.
func compactInput(c *ui.Context, key string, ic *ui.SVG, value *string, placeholder string) (submitted bool) {
	t := c.Theme()
	box := ui.Row(c.Key(key+"-box")).Height(28).Padding(0, 8).Gap(6).Radius(5).
		AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Clip()

	focused := box.FocusWithin()
	hovered := box.Hovered()

	targetBorder := t.Border
	targetBg := t.Background.Alpha(0.6)
	targetIcon := t.TextMuted
	focusTarget := float32(0)
	icScale := float32(1.0)
	if focused {
		targetBorder = t.Accent
		targetBg = t.Background.Alpha(0.95)
		targetIcon = t.Accent
		focusTarget = 1
		icScale = 1.12
	} else if hovered {
		targetBg = t.Background.Alpha(0.78)
		targetBorder = t.TextMuted.Alpha(0.28)
		icScale = 1.05
	}

	if focusK := box.Spring("ring", focusTarget, microSpring); focusK > 0.02 {
		ringCol := t.Accent.Alpha(0.18 * focusK)
		box.DrawOver(func(p *ui.Painter, r ui.Rect) {
			spread := 2.0 * focusK
			p.Stroke(ui.Rect{X: r.X - spread, Y: r.Y - spread, W: r.W + spread*2, H: r.H + spread*2}, ringCol, 5+spread, 2)
		})
	}

	box.Background(box.AnimateColor("bg", targetBg, microColorAnim)).
		Border(1, box.AnimateColor("bd", targetBorder, microColorAnim))

	box.Children(func() {
		if ic != nil {
			icEl := ui.Icon(c, ic).Size(13, 13).Shrink(0)
			icEl.TextColor(icEl.AnimateColor("ic", targetIcon, microColorAnim)).
				AnimatePose(ui.Pose{Scale: icScale}, snappyPoseAnim)
		}
		in := ui.TextInputBase(c.Key(key), value).Placeholder(placeholder).Label(placeholder).
			FontSize(12).Grow(1).Shrink(1).MinWidth(0)
		submitted = in.Submitted()
	})
	return submitted
}

// renderStatusBadge renders a tinted micro-pill Git status indicator with hover/select scale pop.
func renderStatusBadge(c *ui.Context, badge string, active bool) {
	if badge == "" {
		return
	}
	t := c.Theme()
	col := statusColor(t, badge)
	bgAlpha := float32(0.12)
	scale := float32(1.0)
	if active {
		bgAlpha = 0.20
		scale = 1.08
	}
	pill := ui.Row(c).Padding(1, 4.5).Radius(3.5).AlignItems(ui.Center).Shrink(0)
	pill.Background(pill.AnimateColor("sbg", col.Alpha(bgAlpha), microColorAnim)).
		AnimatePose(ui.Pose{Scale: scale}, snappyPoseAnim)
	pill.Children(func() {
		ui.Text(c, badge).Font("monospace").FontSize(10).Bold().TextColor(col).SingleLine()
	})
}
