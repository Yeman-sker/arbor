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
			// Allocate a free lane or append a new one
			for col, l := range lanes {
				if l.hash == "" {
					nodeCol = col
					break
				}
			}
			if nodeCol == -1 {
				nodeCol = len(lanes)
				lanes = append(lanes, activeLane{})
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
			// If first parent can stay straight in nodeCol, keep it there so branches converge at the parent row
			if pIdx == 0 && lanes[nodeCol].hash == "" {
				lanes[nodeCol] = activeLane{hash: parentHash, color: nodeColor}
				edges = append(edges, GraphEdge{FromCol: nodeCol, ToCol: nodeCol, Color: nodeColor})
				continue
			}

			// Does a lane already track this parent?
			existingCol := -1
			for col, l := range lanes {
				if l.hash == parentHash {
					existingCol = col
					break
				}
			}
			if existingCol != -1 {
				edges = append(edges, GraphEdge{FromCol: nodeCol, ToCol: existingCol, Color: lanes[existingCol].color})
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
					targetCol = len(lanes)
					lanes = append(lanes, activeLane{})
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
		if maxCol > 8 {
			maxCol = 8
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

func DrawGraphCell(p *ui.Painter, r ui.Rect, row GraphRow, surfaceBg ui.Color) {
	const laneSpacing = float32(14)
	const leftPad = float32(10)
	midY := r.Y + r.H/2
	colX := func(col int) float32 {
		if col > 7 {
			col = 7
		}
		return r.X + leftPad + float32(col)*laneSpacing
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
				p.StrokePath(&path, 1.75, GraphColor(l.Color))
				break
			}
		}
		if !converges {
			p.Line(x, r.Y, x, midY, 1.75, GraphColor(l.Color))
		}
	}

	// 2. Bottom half: continuing lanes that aren't the commit node itself
	for _, l := range row.BotLanes {
		isFromNode := false
		for _, e := range row.Edges {
			if e.FromCol == row.NodeCol && e.ToCol == l.Col {
				isFromNode = true
				break
			}
		}
		if !isFromNode {
			x := colX(l.Col)
			p.Line(x, midY, x, r.Y+r.H, 1.75, GraphColor(l.Color))
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
			p.Line(nx, midY, nx, r.Y+r.H, 1.75, col)
		} else {
			var path ui.Path
			path.MoveTo(nx, midY).CubeTo(nx, midY+r.H*0.28, tx, midY+r.H*0.22, tx, r.Y+r.H)
			p.StrokePath(&path, 1.75, col)
		}
	}

	// 4. Commit node dot
	nodeColor := GraphColor(row.NodeColor)
	radius := float32(4.2)
	if row.IsMerge {
		radius = 4.8
	}
	dotRect := ui.Rect{X: nx - radius, Y: midY - radius, W: radius * 2, H: radius * 2}
	// Outer halo for clean separation from crossing lines
	haloRect := ui.Rect{X: nx - radius - 1.5, Y: midY - radius - 1.5, W: (radius + 1.5) * 2, H: (radius + 1.5) * 2}
	p.Fill(haloRect, surfaceBg, radius+1.5)
	if row.IsMerge {
		p.Fill(dotRect, surfaceBg, radius)
		p.Stroke(dotRect, nodeColor, radius, 2.0)
	} else {
		p.Fill(dotRect, nodeColor, radius)
	}
}
