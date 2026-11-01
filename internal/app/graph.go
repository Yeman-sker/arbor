package app

import (
	"arbor/internal/git"
	"github.com/egoist/mygo/ui"
)

var graphPalette = []ui.Color{
	ui.Hex("#5E6AD2"), // Linear Indigo
	ui.Hex("#26B5CE"), // Cyan
	ui.Hex("#4CB782"), // Emerald
	ui.Hex("#F2994A"), // Amber
	ui.Hex("#EB5757"), // Coral
	ui.Hex("#BB6BD9"), // Violet
}

func GraphColor(idx int) ui.Color {
	if idx < 0 {
		idx = 0
	}
	return graphPalette[idx%len(graphPalette)]
}

const (
	graphLaneSpacing = float32(11)
	graphLeftPad     = float32(7)
	graphRightPad    = float32(7)
)

func GraphCellWidth(maxCols int) float32 {
	if maxCols < 1 {
		maxCols = 1
	}
	if maxCols > git.GraphMaxCols {
		maxCols = git.GraphMaxCols
	}
	return graphLeftPad + float32(maxCols-1)*graphLaneSpacing + graphRightPad
}

func DrawGraphCell(p *ui.Painter, r ui.Rect, row git.GraphRow, surfaceBg ui.Color, hoverK, expandK float32) {
	midY := r.Y + r.H/2
	colX := func(col int) float32 {
		if col >= git.GraphMaxCols {
			col = git.GraphMaxCols - 1
		}
		return r.X + graphLeftPad + float32(col)*graphLaneSpacing
	}

	// 1. Top half: incoming lanes from previous row to midY
	for _, l := range row.TopLanes {
		x := colX(l.Col)
		// If this lane converges into NodeCol in this row, curve it from top to NodeCol at midY
		converges := false
		for _, e := range row.Edges {
			if e.FromCol == l.Col && e.ToCol == row.NodeCol && l.Col != row.NodeCol {
				converges = true
				var path ui.Path
				nx := colX(row.NodeCol)
				path.MoveTo(x, r.Y).CubeTo(x, r.Y+r.H*0.28, nx, r.Y+r.H*0.22, nx, midY)
				p.StrokePath(&path, 1.65, GraphColor(l.Color))
				break
			}
		}
		if !converges {
			p.Line(x, r.Y, x, midY, 1.65, GraphColor(l.Color))
		}
	}

	// 2. Bottom half: continuing lanes that aren't newly spawned from the commit node itself
	for _, l := range row.BotLanes {
		if l.Col == row.NodeCol {
			continue
		}
		inTop := false
		for _, tl := range row.TopLanes {
			if tl.Col == l.Col {
				inTop = true
				break
			}
		}
		isFromNode := false
		for _, e := range row.Edges {
			if e.FromCol == row.NodeCol && e.ToCol == l.Col {
				isFromNode = true
				break
			}
		}
		if inTop || !isFromNode {
			x := colX(l.Col)
			p.Line(x, midY, x, r.Y+r.H, 1.65, GraphColor(l.Color))
		}
	}

	// 3. Edges from current commit node to parent lanes in bottom half
	nx := colX(row.NodeCol)
	for _, e := range row.Edges {
		if e.FromCol != row.NodeCol {
			continue
		}
		tx := colX(e.ToCol)
		col := GraphColor(e.Color)
		if e.ToCol == row.NodeCol {
			p.Line(nx, midY, nx, r.Y+r.H, 1.65, col)
		} else {
			var path ui.Path
			path.MoveTo(nx, midY).CubeTo(nx, midY+r.H*0.28, tx, midY+r.H*0.22, tx, r.Y+r.H)
			p.StrokePath(&path, 1.65, col)
		}
	}

	// 4. Commit node dot with spring-driven hover/expand glow halo
	nodeColor := GraphColor(row.NodeColor)
	activeK := max(hoverK, expandK)
	radius := float32(3.6) + 0.8*activeK
	if row.IsMerge {
		radius = 4.1 + 0.7*activeK
	}
	// Soft blooming outer glow halo when hovered or expanded
	if activeK > 0.02 {
		glowR := radius + 3.0*activeK
		glowRect := ui.Rect{X: nx - glowR, Y: midY - glowR, W: glowR * 2, H: glowR * 2}
		p.Fill(glowRect, nodeColor.Alpha(0.22*activeK), glowR)
	}
	dotRect := ui.Rect{X: nx - radius, Y: midY - radius, W: radius * 2, H: radius * 2}
	// Inner cutout halo for clean separation from crossing lines
	haloRect := ui.Rect{X: nx - radius - 1.3, Y: midY - radius - 1.3, W: (radius + 1.3) * 2, H: (radius + 1.3) * 2}
	p.Fill(haloRect, surfaceBg, radius+1.3)
	if row.IsMerge || expandK > 0.3 {
		p.Fill(dotRect, surfaceBg, radius)
		p.Stroke(dotRect, nodeColor, radius, 1.8)
		if expandK > 0.3 {
			innerR := 1.6 * expandK
			p.Fill(ui.Rect{X: nx - innerR, Y: midY - innerR, W: innerR * 2, H: innerR * 2}, nodeColor, innerR)
		}
	} else {
		p.Fill(dotRect, nodeColor, radius)
	}
}
