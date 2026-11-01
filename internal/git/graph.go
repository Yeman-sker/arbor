package git

// GraphMaxCols is how many lanes the commit graph draws; the rest share the last.
const GraphMaxCols = 8

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
			// Allocate a free lane or append a new one (bounded by GraphMaxCols)
			for col, l := range lanes {
				if l.hash == "" {
					nodeCol = col
					break
				}
			}
			if nodeCol == -1 {
				if len(lanes) < GraphMaxCols {
					nodeCol = len(lanes)
					lanes = append(lanes, activeLane{})
				} else {
					nodeCol = GraphMaxCols - 1
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
					if len(lanes) < GraphMaxCols {
						targetCol = len(lanes)
						lanes = append(lanes, activeLane{})
					} else {
						targetCol = GraphMaxCols - 1
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
		if maxCol > GraphMaxCols {
			maxCol = GraphMaxCols
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
