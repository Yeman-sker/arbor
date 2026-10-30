package main

import (
	"github.com/egoist/mygo/ui"
)

type GraphEdge struct {
	FromCol int
	ToCol   int
	Color   int
}

type GraphLane struct {
	Col   int
	Color int
}

type GraphRow struct {
	NodeCol   int
	NodeColor int
	IsMerge   bool
	TopLanes  []GraphLane // lanes entering from the row above
	BotLanes  []GraphLane // lanes continuing to the row below
	Edges     []GraphEdge // curves from this row's commit (or top lanes) to bottom lanes
	MaxCols   int
}

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
	graphMaxCols     = 8
	graphLaneSpacing = float32(11)
	graphLeftPad     = float32(7)
	graphRightPad    = float32(7)
)

func GraphCellWidth(maxCols int) float32 {
	if maxCols < 1 {
		maxCols = 1
	}
	if maxCols > graphMaxCols {
		maxCols = graphMaxCols
	}
	return graphLeftPad + float32(maxCols-1)*graphLaneSpacing + graphRightPad
}

type activeLane struct {
	hash  string
	color int
}

// ComputeGraph assigns each topological commit a column and computes the
// pass-through lanes and Bezier parent connections for each row.
func ComputeGraph(commits []Commit) []GraphRow {
	rows := make([]GraphRow, len(commits))
	var lanes []activeLane
	nextColor := 0

	firstParent := make(map[string]string, len(commits))
	for _, c := range commits {
		if len(c.Parents) > 0 {
			firstParent[c.Hash] = c.Parents[0]
		}
	}
	onFirstParentChain := func(startHash, targetHash string) bool {
		cur := startHash
		for steps := 0; cur != "" && steps < len(commits); steps++ {
			if cur == targetHash {
				return true
			}
			cur = firstParent[cur]
		}
		return false
	}

	for i, c := range commits {
		topLanes := make([]GraphLane, 0, len(lanes))
		nodeCol := -1
		for col, l := range lanes {
			if l.hash != "" {
				topLanes = append(topLanes, GraphLane{Col: col, Color: l.color})
				if l.hash == c.Hash && nodeCol == -1 {
					nodeCol = col
				}
			}
		}

		if nodeCol == -1 {
			// Allocate a free lane or append a new one (bounded by graphMaxCols)
			for col, l := range lanes {
				if l.hash == "" {
					nodeCol = col
					break
				}
			}
			if nodeCol == -1 {
				if len(lanes) < graphMaxCols {
					nodeCol = len(lanes)
					lanes = append(lanes, activeLane{})
				} else {
					nodeCol = graphMaxCols - 1
				}
			}
			lanes[nodeCol] = activeLane{hash: c.Hash, color: nextColor}
			nextColor++
		}
		nodeColor := lanes[nodeCol].color

		// Collapse duplicate lanes pointing to the same commit (converging branches)
		var edges []GraphEdge
		for col := range lanes {
			if col != nodeCol && lanes[col].hash == c.Hash {
				edges = append(edges, GraphEdge{FromCol: col, ToCol: nodeCol, Color: lanes[col].color})
				lanes[col] = activeLane{}
			}
		}

		// Clear current commit from its lane before placing parents
		lanes[nodeCol] = activeLane{}

		for pIdx, parentHash := range c.Parents {
			// Does an active lane already track this parent (or its first-parent trunk for merges)?
			existingCol := -1
			for col, l := range lanes {
				if l.hash == parentHash {
					existingCol = col
					break
				}
			}
			if existingCol == -1 && pIdx > 0 {
				for col, l := range lanes {
					if l.hash != "" && onFirstParentChain(l.hash, parentHash) {
						existingCol = col
						break
					}
				}
			}
			if existingCol != -1 && (pIdx > 0 || existingCol < nodeCol) {
				edges = append(edges, GraphEdge{FromCol: nodeCol, ToCol: existingCol, Color: lanes[existingCol].color})
				continue
			}

			// If first parent can stay straight in nodeCol, keep it there so branches converge at the parent row
			if pIdx == 0 && lanes[nodeCol].hash == "" {
				lanes[nodeCol] = activeLane{hash: parentHash, color: nodeColor}
				edges = append(edges, GraphEdge{FromCol: nodeCol, ToCol: nodeCol, Color: nodeColor})
				continue
			}

			// Place first parent in nodeCol if free; subsequent parents in first free slot
			targetCol := -1
			if pIdx == 0 && lanes[nodeCol].hash == "" {
				targetCol = nodeCol
			} else {
				for col, l := range lanes {
					if l.hash == "" {
						targetCol = col
						break
					}
				}
				if targetCol == -1 {
					if len(lanes) < graphMaxCols {
						targetCol = len(lanes)
						lanes = append(lanes, activeLane{})
					} else {
						targetCol = graphMaxCols - 1
					}
				}
			}

			pColor := nodeColor
			if pIdx > 0 {
				pColor = nextColor
				nextColor++
			}
			lanes[targetCol] = activeLane{hash: parentHash, color: pColor}
			edges = append(edges, GraphEdge{FromCol: nodeCol, ToCol: targetCol, Color: pColor})
		}

		// Trim trailing empty lanes
		for len(lanes) > 0 && lanes[len(lanes)-1].hash == "" {
			lanes = lanes[:len(lanes)-1]
		}

		var botLanes []GraphLane
		for col, l := range lanes {
			if l.hash != "" {
				botLanes = append(botLanes, GraphLane{Col: col, Color: l.color})
			}
		}

		maxCol := nodeCol + 1
		for _, l := range topLanes {
			if l.Col+1 > maxCol {
				maxCol = l.Col + 1
			}
		}
		for _, l := range botLanes {
			if l.Col+1 > maxCol {
				maxCol = l.Col + 1
			}
		}
		for _, e := range edges {
			if e.FromCol+1 > maxCol {
				maxCol = e.FromCol + 1
			}
			if e.ToCol+1 > maxCol {
				maxCol = e.ToCol + 1
			}
		}
		if maxCol > graphMaxCols {
			maxCol = graphMaxCols
		}

		rows[i] = GraphRow{
			NodeCol:   nodeCol,
			NodeColor: nodeColor,
			IsMerge:   len(c.Parents) > 1,
			TopLanes:  topLanes,
			BotLanes:  botLanes,
			Edges:     edges,
			MaxCols:   maxCol,
		}
	}
	return rows
}

func DrawGraphCell(p *ui.Painter, r ui.Rect, row GraphRow, surfaceBg ui.Color, hoverK, expandK float32) {
	midY := r.Y + r.H/2
	colX := func(col int) float32 {
		if col >= graphMaxCols {
			col = graphMaxCols - 1
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
