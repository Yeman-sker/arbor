package main

import (
	"github.com/egoist/mygo/ui"
)

// iconBtn renders a 25x25 borderless Linear-style icon button.
func iconBtn(c *ui.Context, key string, ic *ui.SVG, tip string, active bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Size(25, 25).Radius(5).Center().
		Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)
	if active {
		btn.Background(t.Accent.Alpha(0.11)).TextColor(t.Accent)
	} else if btn.Hovered() {
		btn.Background(t.SurfaceHover).TextColor(t.Text)
	} else {
		btn.TextColor(t.TextMuted)
	}
	btn.Children(func() {
		ui.Icon(c, ic).Size(14, 14)
	})
	return btn.Clicked()
}

// rowIconBtn renders a compact 20x20 icon action button inside a list row.
func rowIconBtn(c *ui.Context, key string, ic *ui.SVG, tip string) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Size(20, 20).Radius(4).Center().
		Cursor(ui.CursorPointer).Label(tip).Tooltip(tip)
	if btn.Hovered() {
		btn.Background(t.SurfacePressed).TextColor(t.Text)
	} else {
		btn.TextColor(t.TextMuted)
	}
	btn.Children(func() {
		ui.Icon(c, ic).Size(12.5, 12.5)
	})
	return btn.Clicked()
}

// ghostBtn renders a refined borderless button with an icon and a short label.
func ghostBtn(c *ui.Context, key string, ic *ui.SVG, label, tip string, active bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(24).Padding(0, 7).Gap(5).Radius(4).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if tip != "" {
		btn.Tooltip(tip)
	}
	if active {
		btn.Background(t.Accent.Alpha(0.11)).TextColor(t.Accent)
	} else if btn.Hovered() {
		btn.Background(t.SurfaceHover).TextColor(t.Text)
	} else {
		btn.TextColor(t.TextMuted)
	}
	btn.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13)
		}
		txt := ui.Text(c, label).FontSize(12).SingleLine()
		if active {
			txt.FontWeight(500)
		}
	})
	return btn.Clicked()
}

// ghostMenuBtn renders a borderless Linear-style dropdown button with an icon, label, and chevron.
func ghostMenuBtn(c *ui.Context, key string, ic *ui.SVG, label, tip string, build func(m *ui.Menu)) {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 7).Gap(4).Radius(5).
		AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label)
	if tip != "" {
		btn.Tooltip(tip)
	}
	if btn.Hovered() {
		btn.Background(t.SurfaceHover).TextColor(t.Text)
	} else {
		btn.TextColor(t.TextMuted)
	}
	btn.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13)
		}
		ui.Text(c, label).FontSize(12).SingleLine()
		ui.Icon(c, IconChevronDown).Size(11, 11).TextColor(t.TextMuted)
	})
	btn.PopoverMenuButton(build)
}

// primaryActionBtn renders a compact Linear indigo action button with an icon.
func primaryActionBtn(c *ui.Context, key string, ic *ui.SVG, label string, disabled bool) bool {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 9).Gap(5).Radius(5).
		AlignItems(ui.Center).Label(label).Disabled(disabled)
	if disabled {
		btn.Background(t.SurfaceHover).TextColor(t.TextMuted).Opacity(0.5)
	} else {
		btn.Cursor(ui.CursorPointer)
		if btn.Pressed() {
			btn.Background(t.AccentPressed).TextColor(t.AccentText)
		} else if btn.Hovered() {
			btn.Background(t.AccentHover).TextColor(t.AccentText)
		} else {
			btn.Background(t.Accent).TextColor(t.AccentText)
		}
	}
	btn.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13)
		}
		ui.Text(c, label).FontSize(12).Bold().SingleLine()
	})
	return !disabled && btn.Clicked()
}

// compactInput renders a sleek 28px single-line input box with a leading icon.
func compactInput(c *ui.Context, key string, ic *ui.SVG, value *string, placeholder string) (submitted bool) {
	t := c.Theme()
	box := ui.Row(c.Key(key+"-box")).Height(28).Padding(0, 8).Gap(6).Radius(5).
		AlignItems(ui.Center).Grow(1).Background(t.Background.Alpha(0.65)).Border(1, t.Border)
	if box.FocusWithin() {
		box.BorderColor(t.Accent)
	}
	box.Children(func() {
		if ic != nil {
			ui.Icon(c, ic).Size(13, 13).TextColor(t.TextMuted)
		}
		in := ui.TextInputBase(c.Key(key), value).Placeholder(placeholder).Label(placeholder).
			FontSize(12).Grow(1)
		submitted = in.Submitted()
	})
	return submitted
}

// renderStatusBadge renders a clean single-letter Git status indicator.
func renderStatusBadge(c *ui.Context, badge string) {
	if badge == "" {
		return
	}
	t := c.Theme()
	col := statusColor(t, badge)
	ui.Text(c, badge).Font("monospace").FontSize(11).Bold().TextColor(col).SingleLine()
}
