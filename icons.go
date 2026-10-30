package main

import "github.com/egoist/mygo/ui"

func lucide(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

var (
	// Navigation & Modes
	IconFiles      = lucide(`<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/>`)
	IconGitBranch  = lucide(`<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`)
	IconGitCommit  = lucide(`<circle cx="12" cy="12" r="3"/><line x1="3" x2="9" y1="12" y2="12"/><line x1="15" x2="21" y1="12" y2="12"/>`)
	IconGitCompare = lucide(`<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 0 1 2 2v7"/><path d="M11 18H8a2 2 0 0 1-2-2V9"/>`)

	// Folders & Files
	IconFolder     = lucide(`<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`)
	IconFolderOpen = lucide(`<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>`)
	IconFileCode   = lucide(`<path d="M10 12.5 8 15l2 2.5"/><path d="m14 12.5 2 2.5-2 2.5"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7z"/>`)
	IconFileText   = lucide(`<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="M10 9H8"/><path d="M16 13H8"/><path d="M16 17H8"/>`)

	// Chevrons & Arrows
	IconChevronRight = lucide(`<path d="m9 18 6-6-6-6"/>`)
	IconChevronDown  = lucide(`<path d="m6 9 6 6 6-6"/>`)
	IconChevronUp    = lucide(`<path d="m18 15-6-6-6 6"/>`)
	IconArrowDown    = lucide(`<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>`)
	IconArrowUp      = lucide(`<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>`)

	// Actions & Controls
	IconSearch   = lucide(`<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`)
	IconRefresh  = lucide(`<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`)
	IconPlus     = lucide(`<path d="M5 12h14"/><path d="M12 5v14"/>`)
	IconMinus    = lucide(`<path d="M5 12h14"/>`)
	IconUndo     = lucide(`<path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-9-9 9 9 0 0 0-6 2.3L3 13"/>`)
	IconCheck    = lucide(`<path d="M20 6 9 17l-5-5"/>`)
	IconArchive  = lucide(`<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>`)
	IconColumns  = lucide(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M12 3v18"/>`)
	IconRows     = lucide(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M3 12h18"/>`)
	IconWrapText = lucide(`<line x1="3" x2="21" y1="6" y2="6"/><path d="M3 12h15a3 3 0 1 1 0 6h-4"/><polyline points="16 16 14 18 16 20"/><line x1="3" x2="10" y1="18" y2="18"/>`)
	IconFilter   = lucide(`<polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"/>`)
	IconExpand   = lucide(`<path d="m7 15 5 5 5-5"/><path d="m7 9 5-5 5 5"/>`)
)

func iconForFile(path string) *ui.SVG {
	ext := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			ext = path[i:]
			break
		}
		if path[i] == '/' {
			break
		}
	}
	switch ext {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".lock":
		return IconFileText
	default:
		return IconFileCode
	}
}
