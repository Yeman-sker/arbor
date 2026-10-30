package main

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

func mustLucide(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

func mustCatppuccin(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.15" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

// UI & Git Chrome Icons (Lucide 24x24)
var (
	IconFiles = mustLucide(`<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/>`)

	IconFolder     = mustLucide(`<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/>`)
	IconFolderOpen = mustLucide(`<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>`)

	IconGitBranch  = mustLucide(`<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`)
	IconGitCommit  = mustLucide(`<circle cx="12" cy="12" r="3"/><line x1="3" x2="9" y1="12" y2="12"/><line x1="15" x2="21" y1="12" y2="12"/>`)
	IconGitCompare = mustLucide(`<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 0 1 2 2v7"/><path d="M11 18H8a2 2 0 0 1-2-2V9"/>`)

	IconRefresh     = mustLucide(`<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/>`)
	IconPlus        = mustLucide(`<path d="M5 12h14"/><path d="M12 5v14"/>`)
	IconMinus       = mustLucide(`<path d="M5 12h14"/>`)
	IconUndo        = mustLucide(`<path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-9-9 9 9 0 0 0-6 2.3L3 13"/>`)
	IconCheck       = mustLucide(`<path d="M20 6 9 17l-5-5"/>`)
	IconSearch      = mustLucide(`<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`)
	IconFilter      = mustLucide(`<polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"/>`)
	IconColumns     = mustLucide(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M12 3v18"/>`)
	IconRows        = mustLucide(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M3 12h18"/>`)
	IconWrapText    = mustLucide(`<line x1="3" x2="21" y1="6" y2="6"/><path d="M3 12h15a3 3 0 1 1 0 6h-4"/><polyline points="16 16 14 18 16 20"/><line x1="3" x2="10" y1="18" y2="18"/>`)
	IconExpand      = mustLucide(`<path d="m7 15 5 5 5-5"/><path d="m7 9 5-5 5 5"/>`)
	IconChevronUp   = mustLucide(`<path d="m18 15-6-6-6 6"/>`)
	IconChevronDown = mustLucide(`<path d="m6 9 6 6 6-6"/>`)
	IconChevronRight = mustLucide(`<path d="m9 18 6-6-6-6"/>`)
	IconArrowDown   = mustLucide(`<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>`)
	IconArrowUp     = mustLucide(`<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>`)
)

// Dedicated Language & File Icons (Catppuccin VSC Icons 16x16)
var (
	IconFile = mustCatppuccin(`<path d="M13.5 6.5v6a2 2 0 01-2 2h-7a2 2 0 01-2-2v-9c0-1.1.9-2 2-2h4.01m-.01 0 5 5h-4a1 1 0 01-1-1z"/>`)

	IconLangGo    = mustCatppuccin(`<path d="m15.48 8.06-4.85.48m4.85-.48a4.98 4.98 0 01-4.54 5.42 5 5 0 112.95-8.66l-1.7 1.84a2.5 2.5 0 00-4.18 2.06c.05.57.3 1.1.69 1.51.25.27 1 .83 1.78.82.8-.02 1.58-.25 2.07-.81 0 0 .8-.96.68-1.88M2.5 8.5l-2 .01m1.5 2h1.5m-2-3.99 2-.02"/>`)
	IconLangGoMod = mustCatppuccin(`<path d="m2.5 8.51-2 .01m1.5 2h1.5m-2-4h1m6.73-2.19a1.5 1.5 0 10-2.16.58m2.16-.58 2.78-.74.75 2.78m.57 2.16.75 2.79-7.73 2.07-2.07-7.73 2.78-.75m6.28 3.63a1.5 1.5 0 10-.58-2.17"/>`)

	IconLangTS = mustCatppuccin(`<path d="M4 1.5h8A2.5 2.5 0 0114.5 4v8a2.5 2.5 0 01-2.5 2.5H4A2.5 2.5 0 011.5 12V4A2.5 2.5 0 014 1.5"/><path d="M12.5 8.75c0-.69-.54-1.25-1.2-1.25h-.6c-.66 0-1.2.56-1.2 1.25S10.04 10 10.7 10h.6c.66 0 1.2.56 1.2 1.25s-.54 1.25-1.2 1.25h-.6c-.66 0-1.2-.56-1.2-1.25m-3-3.75v5M5 7.5h3"/>`)
	IconLangJS = mustCatppuccin(`<path d="M4.5 11c0 .83.67 1.5 1.5 1.5s1.5-.67 1.5-1.5V7.5M12.5 8.75c0-.69-.54-1.25-1.2-1.25h-.6c-.66 0-1.2.56-1.2 1.25S10.04 10 10.7 10h.6c.66 0 1.2.56 1.2 1.25s-.54 1.25-1.2 1.25h-.6c-.66 0-1.2-.56-1.2-1.25"/><path d="M4 1.5h8A2.5 2.5 0 0114.5 4v8a2.5 2.5 0 01-2.5 2.5H4A2.5 2.5 0 011.5 12V4A2.5 2.5 0 014 1.5Z"/>`)
	IconLangReact = mustCatppuccin(`<path d="M8 10.8c4.14 0 7.5-1.25 7.5-2.8S12.14 5.2 8 5.2.5 6.45.5 8s3.36 2.8 7.5 2.8"/><path d="M5.52 9.4c2.07 3.5 4.86 5.72 6.23 4.95 1.37-.78.8-4.24-1.27-7.75C8.41 3.1 5.62.88 4.25 1.65c-1.37.78-.8 4.24 1.27 7.75"/><path d="M5.52 6.6c-2.07 3.5-2.64 6.97-1.27 7.75 1.37.77 4.16-1.45 6.23-4.95s2.64-6.97 1.27-7.75C10.38.88 7.59 3.1 5.52 6.6"/><path d="M8.5 8a.5.5 0 01-.5.5.5.5 0 01-.5-.5.5.5 0 01.5-.5.5.5 0 01.5.5"/>`)

	IconLangPython = mustCatppuccin(`<path d="M8.5 5.5h-3m6 0V3c0-.8-.7-1.5-1.5-1.5H7c-.8 0-1.5.7-1.5 1.5v2.5H3c-.8 0-1.5.7-1.5 1.5v2c0 .8.7 1.5 1.48 1.5"/><path d="M10.5 10.5h-3m-3 0V13c0 .8.7 1.5 1.5 1.5h3c.8 0 1.5-.7 1.5-1.5v-2.5H13c.8 0 1.5-.7 1.5-1.5V7c0-.8-.7-1.5-1.48-1.5H11.5c0 1.5 0 2-1 2h-2"/><path d="M2.98 10.5H4.5c0-1.5 0-2 1-2h2M7.5 3.5v0m1 9v0"/>`)
	IconLangRust   = mustCatppuccin(`<path d="M15.5 9.5Q8 13.505.5 9.5l1-1-1-2 2-.5V4.5h2l.5-2 1.5 1 1.5-2 1.5 2 1.5-1 .5 2h2V6l2 .5-1 2z"/><path d="M6.5 7.5a1 1 0 01-1 1 1 1 0 01-1-1 1 1 0 011-1 1 1 0 011 1m5 0a1 1 0 01-1 1 1 1 0 01-1-1 1 1 0 011-1 1 1 0 011 1M4 11.02c-.67.37-1.5.98-1.5 2.23s1.22 1.22 2 1.25v-2M12 11c.67.37 1.5 1 1.5 2.25s-1.22 1.22-2 1.25v-2"/>`)

	IconLangC      = mustCatppuccin(`<path d="M4.06 12.95c2.74 2.73 7.2 2.73 9.94 0l-1.79-1.78a4.51 4.51 0 01-6.36 0 4.47 4.47 0 010-6.34 4.51 4.51 0 016.36 0l1.79-1.78c-2.74-2.73-7.2-2.73-9.94 0a6.97 6.97 0 000 9.9z"/>`)
	IconLangCPP    = mustCatppuccin(`<path d="M2.56 12.95c2.74 2.73 7.2 2.73 9.94 0l-1.79-1.78a4.51 4.51 0 01-6.36 0 4.47 4.47 0 010-6.34 4.51 4.51 0 016.36 0l1.79-1.78c-2.74-2.73-7.2-2.73-9.94 0a6.97 6.97 0 000 9.9z"/><path d="M7.5 6v4M5.51 8h4M13.49 6v4M11.5 8h4"/>`)
	IconLangCSharp = mustCatppuccin(`<path d="M6.67 1.01c.54.09.91.61.82 1.15L7.18 4h2.97l.36-2.17a1 1 0 111.97.33L12.18 4H14a1 1 0 110 2h-2.15l-.67 4H13a1 1 0 110 2h-2.15l-.36 2.16a1 1 0 11-1.97-.33L8.82 12H5.85l-.36 2.17a1 1 0 11-1.97-.33L3.82 12H2a1 1 0 110-2h2.15l.67-4H3a1 1 0 110-2h2.15l.36-2.17a1 1 0 011.16-.82zM6.85 6l-.67 4h2.97l.67-4z"/>`)

	IconLangJava   = mustCatppuccin(`<path d="M10.73 8.41c.57 3 1.59 5.83 2.77 7.09-6.63-3.45-9.76-1.75-10.5 0-.66-3.4-.54-5.74.09-7.78"/><path d="M8.5 7c.63.34 1.82 1.07 2.24 1.41-.54-2.9-.64-5.96-.74-7.91-2.13.58-5.73 1.98-6.9 7.22.52-.69 1.72-1.05 2.4-1.22"/><path d="M5.5 7A1.5 1.5 0 007 8.5 1.5 1.5 0 008.5 7 1.5 1.5 0 007 5.5 1.5 1.5 0 005.5 7"/>`)
	IconLangKotlin = mustCatppuccin(`<path d="M2.5 13.5h11L8 8m.03-5.5h5.47l-8 8m-3 3V2.5H8L2.5 8"/>`)
	IconLangSwift  = mustCatppuccin(`<path d="M14.34 10.2c.34-1.08 1.1-5.07-4.45-8.62a.48.48 0 00-.6.07.44.44 0 00-.02.6c.03.02 2.07 2.5 1.34 5.34-1.26-.86-6.24-4.81-6.24-4.81L7.25 7.5 1.9 4.05S5.68 8.7 8 10.45c-1.12.4-3.56.82-6.78-1.18a.48.48 0 00-.58.06.44.44 0 00-.08.56c.11.18 2.7 4.36 8.14 4.36 1.5 0 2.37-.42 3.08-.77.43-.2.77-.37 1.14-.37.93 0 1.54.92 1.54.93.1.14.27.22.44.21a.46.46 0 00.4-.28c.67-1.55-.49-3.2-.96-3.78Z"/>`)
	IconLangDart   = mustCatppuccin(`<path d="M7 14.5h4.5v-3h3V7L9.17 1.64c-.28-.29-.8-.47-1.17-.29L3.5 3.5 1.35 8c-.18.37 0 .88.3 1.17z"/><path d="M3.5 11V3.5H11m-7.5 0 8 8"/>`)
	IconLangZig    = mustCatppuccin(`<path d="M10 3.5H6l-1.5 2h4l-7 9 4.5-2h4l1.5-2h-4l7-9z"/>`)
	IconLangRuby   = mustCatppuccin(`<path d="M1.5 9.06v2.5c.02.86.36 1.61.9 2.15 1.76 1.76 5.71.65 8.84-2.47 3.12-3.13 4.23-7.08 2.47-8.84a3.1 3.1 0 00-2.15-.9h-2.5M14.5 4l-.25 10.25L4 14.5m4.39-6.11c2.34-2.35 3.29-5.2 2.12-6.37S6.49 1.8 4.14 4.14C1.8 6.5.85 9.34 2.02 10.51s4.02.22 6.37-2.12M5.5 14.5l.25-3.75L11 11l-.25-5.25 3.75-.25"/>`)
	IconLangPHP    = mustCatppuccin(`<path d="M.5 12.5v.74c0 .76.77 1.26 1.5 1.26.94 0 1.5-.5 1.5-1.26V6c0-1.71 1.49-3.48 3.65-3.5C9.49 2.5 11 4.06 11 5.5c.17 2.99-1.42 4.14-3.5 5v4h8V9c.04-.64-.56-1.84-1.37-2.5-.94-.69-2.07-1-3.13-1M11.5 14.5v-3M6 6.5a.5.5 0 100-1 .5.5 0 000 1Z"/>`)
	IconLangLua    = mustCatppuccin(`<path d="M10.5 7A1.5 1.5 0 019 8.5 1.5 1.5 0 017.5 7 1.5 1.5 0 019 5.5 1.5 1.5 0 0110.5 7"/><path d="M7 2.5a6.5 6.5 0 100 13 6.5 6.5 0 000-13m7-2a1.5 1.5 0 100 3 1.5 1.5 0 000-3"/>`)
	IconLangElixir = mustCatppuccin(`<path d="M8.03 14.5C5 14.5 3.5 12.49 3.5 10.01c0-2.82 2.25-7.02 4.62-8.48a.24.24 0 01.24 0c.08.04.12.12.11.2-.13 1.25.22 2.5.98 3.54.3.43.63.8 1.02 1.27.54.66.94 1.03 1.52 2.08l.01.02c.33.56.5 1.2.5 1.84 0 2.03-1.69 4.02-4.47 4.02"/>`)
	IconLangScala  = mustCatppuccin(`<path d="m2.5 2.48 11-.98v3.04l-11 1zm0 5 11-.98v3.04l-11 1zm0 5 11-.98v3.04l-11 1z"/>`)

	IconLangHTML     = mustCatppuccin(`<path d="M1.5 1.5h13L13 13l-5 2-5-2z"/><path d="M11 4.5H5l.25 3h5.5l-.25 3-2.5 1-2.5-1-.08-1"/>`)
	IconLangCSS      = mustCatppuccin(`<path d="M4 1.5h8A2.5 2.5 0 0114.5 4v8a2.5 2.5 0 01-2.5 2.5H4A2.5 2.5 0 011.5 12V4A2.5 2.5 0 014 1.5z"/><path d="M10.24 11.53c0 .58.44 1.04.96 1.04h.45c.52 0 .95-.45.95-1.03 0-.58-.43-1.07-.95-1.07h-.45c-.52 0-.95-.49-.95-1.07 0-.58.43-1.04.95-1.04h.45c.52 0 .95.46.95 1.04M6.8 11.53c0 .58.44 1.04.96 1.04h.46c.52 0 .94-.45.94-1.03 0-.58-.41-1.07-.93-1.07h-.47c-.52 0-.95-.49-.95-1.07 0-.58.43-1.04.95-1.04h.45c.52 0 .96.46.96 1.04M3.41 11.53c0 .58.44 1.05.96 1.05h.45c.52 0 .95-.46.95-1.04m.01-2.13c0-.58-.44-1.04-.96-1.04h-.45c-.52 0-.96.47-.96 1.05v2.12"/>`)
	IconLangSass     = mustCatppuccin(`<path d="M6.75 6.38c1.85 1.07 3.35.74 4.83-.2 1.5-.95 2.7-2.78 1.3-4.15-.7-.68-3.25-.8-5.62.19-2.36.99-4.59 3.02-4.74 4.11-.31 2.19 3.15 2.88 3.64 4.23s.28 1.98-.2 2.83c-.5.85-1.96 1.62-2.8.68-.83-.95 1.67-2.75 2.98-3.25 1.3-.5 3.1-.4 3.69.25.58.64-.07 1.79-.03 1.79"/>`)
	IconLangTailwind = mustCatppuccin(`<path d="M8 2.5q-3 0-3.75 3.33C5 4.73 5.88 4.31 6.87 4.58c.58.16.98.62 1.43 1.13.74.83 1.6 1.79 3.45 1.79q3 0 3.75-3.33c-.75 1.1-1.63 1.52-2.63 1.25-.57-.16-.97-.62-1.42-1.13C10.7 3.46 9.85 2.5 8 2.5m-3.75 6q-3 0-3.75 3.33c.75-1.1 1.63-1.52 2.63-1.25.57.16.97.62 1.42 1.13.74.83 1.6 1.79 3.45 1.79q3 0 3.75-3.33c-.75 1.1-1.63 1.52-2.62 1.25-.58-.16-.98-.62-1.43-1.13-.74-.83-1.6-1.79-3.45-1.79"/>`)
	IconLangVue      = mustCatppuccin(`<path d="M1 1.5h5.44L8 4.56 9.56 1.5H15l-6.99 13z"/><path d="M12.05 1.73 8 9.28 3.95 1.73"/>`)
	IconLangSvelte   = mustCatppuccin(`<path d="M12.86 6.72s1.39-1.98.08-3.87C11.29.76 9.44 1.6 9.44 1.6S6.15 3.35 4.33 4.59c-1.4 1-2.24 2.26-1.03 4.37 1.22 2.1 4.58 1.21 4.58 1.21"/><path d="M3.14 9.28s-1.39 1.98-.08 3.87c1.31 1.9 3.5 1.24 3.5 1.24s3.29-1.74 5.11-2.98c1.4-1 2.24-2.26 1.03-4.37-1.22-2.1-4.58-1.21-4.58-1.21M6.3 6.96l4.14-2.56m-4.92 7.25L9.66 9.1"/>`)
	IconLangAstro    = mustCatppuccin(`<path d="M10.56 1.61 13.5 10.5c-1-.52-2.4-1.3-3.5-1.49L8 3.5 6 9.01c-1.11.19-2.5.97-3.5 1.49L5.66 1.61C5.9.86 6.2.5 7.16.5h1.9c.96 0 1.26.36 1.5 1.11Z"/><path d="M10.41 11.27c-.43.36-1.29.61-2.28.61-1.22 0-2.24-.37-2.51-.88-.1.29-.12.62-.12.83 0 0-.06 1.04.67 1.76 0-.37.31-.68.68-.68.65 0 .65.56.65 1.02v.04c0 .69.43 1.28 1.03 1.53-.09-.19-.14-.4-.14-.61 0-.66.39-.9.84-1.19.36-.23.76-.48 1.04-.98.15-.27.23-.58.23-.89 0-.19-.03-.38-.09-.56Z"/>`)

	IconLangJSON    = mustCatppuccin(`<path d="M4.5 2.5H4c-.75 0-1.5.75-1.5 1.5v2c0 1.1-1 2-1.83 2 .83 0 1.83.9 1.83 2v2c0 .75.75 1.5 1.5 1.5h.5m7-11h.5c.75 0 1.5.75 1.5 1.5v2c0 1.1 1 2 1.83 2-.83 0-1.83.9-1.83 2v2c0 .74-.75 1.5-1.5 1.5h-.5m-6.5-3a.5.5 0 100-1 .5.5 0 000 1m3 0a.5.5 0 100-1 .5.5 0 000 1m3 0a.5.5 0 100-1 .5.5 0 000 1"/>`)
	IconLangYAML    = mustCatppuccin(`<path d="M2.5 1.5h3l3 4 3-4h3l-9 13h-3L7 8z"/>`)
	IconLangTOML    = mustCatppuccin(`<path d="M3.5 1.5h-2v13h2m9-13h2v13h-2m-8-11h7v3h-2v6h-3v-6h-2z"/>`)
	IconLangXML     = mustCatppuccin(`<path d="M4.5 4.5 1 8l3.5 3.5m7-7L15 8l-3.5 3.5M9.5 2l-3 12"/>`)
	IconLangCSV     = mustCatppuccin(`<path d="M1.5 3.5c0-.54.48-1 1.08-1H6.5l1.54 1h5.38c.6 0 1.08.44 1.08.98l-.09 9.04c0 .54-.48.98-1.08.98H2.58c-.6 0-1.08-.44-1.08-.98zm2 4v4m3-4v4m3-4v4m3-4v4m-9 0h9m-9-2h9m-9-2h9"/>`)
	IconLangGraphQL = mustCatppuccin(`<path d="M9 1.5a1 1 0 11-2 0 1 1 0 012 0m-5.5 3a1 1 0 11-2 0 1 1 0 012 0m0 7a1 1 0 11-2 0 1 1 0 012 0m11 0a1 1 0 11-2 0 1 1 0 012 0m-5.5 3a1 1 0 11-2 0 1 1 0 012 0m5.5-10a1 1 0 11-2 0 1 1 0 012 0m-12 1v5m11-5v5m-10 1h9m-6 2.5-3-1.5m6 1.5 3-1.5m-9-2 4-8m5 8-4-8m-5 1 3-1.5m3 0 3 1.5"/>`)
	IconLangProto   = mustCatppuccin(`<path d="m.5 8.5 3-6h3l-3 6m3 5h-3l-3-5 1.36-2.73zm12-6-3 6h-3l3-6m-3-5h3l3 5-1.36 2.73z"/>`)
	IconLangPrisma  = mustCatppuccin(`<path d="m8 .5 6.5 12.05-10 2.95-3-5zm-3.5 15L8 .5"/>`)
	IconLangSQL     = mustCatppuccin(`<path d="M8 6.5c3.59 0 6.5-1.4 6.5-2.68S11.59 1.5 8 1.5 1.5 2.54 1.5 3.82 4.41 6.5 8 6.5M14.5 8c0 .83-1.24 1.79-3.25 2.2s-4.49.41-6.5 0S1.5 8.83 1.5 8m13 4.18c0 .83-1.24 1.6-3.25 2-2.01.42-4.49.42-6.5 0-2.01-.4-3.25-1.17-3.25-2m0-8.3v8.3m13-8.3v8.3"/>`)

	IconLangMarkdown = mustCatppuccin(`<path d="m9.25 8.25 2.25 2.25 2.25-2.25M3.5 11V5.5l2.04 3 1.96-3V11m4-.5V5M1.65 2.5h12.7c.59 0 1.15.49 1.15 1v9c0 .51-.56 1-1.15 1H1.65c-.59 0-1.15-.49-1.15-1V3.58c0-.5.56-1.08 1.15-1.08"/>`)
	IconLangReadme   = mustCatppuccin(`<path d="M8 14.5a6.5 6.5 0 100-13 6.5 6.5 0 000 13"/><path d="M7 7.5h2v5H7Zm2-3a1 1 0 11-2 0 1 1 0 012 0"/>`)
	IconLangLicense  = mustCatppuccin(`<path d="M4.5 13.5h7M8.01 1v12.06M1.5 3.5h3l1.5-1h4l1.5 1h3M.5 10 3 4.48 5.5 10C4 11 2 11 .5 10m10 0L13 4.48 15.5 10c-1.5 1-3.5 1-5 0"/>`)
	IconLangPDF      = mustCatppuccin(`<path d="M2.8 14.34c1.81-1.25 3.02-3.16 3.91-5.5.9-2.33 1.86-4.33 1.44-6.63-.06-.36-.57-.73-.83-.7-1.02.06-.95 1.21-.85 1.9.24 1.71 1.56 3.7 2.84 5.56 1.27 1.87 2.32 2.16 3.78 2.26.5.03 1.25-.14 1.37-.58.77-2.8-9.02-.54-12.28 2.08-.4.33-.86 1-.6 1.46.2.36.87.4 1.23.15Z"/>`)

	IconLangDocker    = mustCatppuccin(`<path d="M.5 8.5H11l.75-.5a5.35 5.35 0 010-3.5c1 .6 1 1.88 1.74 2 .77-.09 1.23.01 2 .52 0 0-.97 1.77-2.5 1.98-1.93 3.65-4.5 5.5-6.98 5.5C0 14.5.5 8.5.5 8.5m1 0v-2m0 0h8m-6 2v-4m0 0h4m-2-2h2m-2 6v-6m2 6v-6m2 6v-2"/>`)
	IconLangTerraform = mustCatppuccin(`<path d="m1.5 6 8 4.25 4-2.25m-12-2V1.5l8 4.25 4-2.25V8m-4-2.25v8.75M5.53 3.82 5.5 12.5l4 2"/>`)
	IconLangNix       = mustCatppuccin(`<path d="M.5 7.5H4m1.39-2L2.05 11M4 1.5 5.5 4m3.5.5H2.55M12 1.5l-1.5 3m1.01 2.6L8.5 1.5M15.5 8.52 12 8.5m-1.38 2L14 5m-1.5 9.5-2.5-3m-2.97.02 6.48-.02M4 14.5l1.5-3M4.53 9l2.97 5.5"/>`)
	IconLangBash      = mustCatppuccin(`<path d="M2 15.5c-.7 0-1.5-.8-1.5-1.5V5c0-.7.8-1.5 1.5-1.5h9c.7 0 1.5.8 1.5 1.5v9c0 .7-.8 1.5-1.5 1.5z"/><path d="m1.2 3.8 3.04-2.5S5.17.5 5.7.5h8.4c.66 0 1.4.73 1.4 1.4v7.73a2.7 2.7 0 01-.7 1.75l-2.68 3.51"/><path d="M6 8.75c0-.69-.54-1.25-1.2-1.25h-.6c-.66 0-1.2.56-1.2 1.25S3.54 10 4.2 10h.6c.66 0 1.2.56 1.2 1.25s-.54 1.25-1.2 1.25h-.6c-.66 0-1.2-.56-1.2-1.25M4.5 6.5v1m0 5v1"/>`)
	IconLangMakefile  = mustCatppuccin(`<path d="m9.24 8.47-5.28 5.58c-.56.6-1.47.6-2.04 0a1.57 1.57 0 010-2.14L7.2 6.32"/><path d="m13.74 8.03-.86-.93a2.46 2.46 0 01-.64-1.68v-.65l-1.89-2.04A3.7 3.7 0 007.63 1.5H5.5l.64.61A4.72 4.72 0 017.5 5.57v1.16l1.46 1.5h1.71l1.57 1.42m-.74.85 3-3"/>`)
	IconLangCMake     = mustCatppuccin(`<path d="m6 11.5-3.5 3H13Zm3-10 5.5 11-4.5-2zm-7.5 11 6-5-.5-6Z"/>`)

	IconLangGit     = mustCatppuccin(`<path d="M8.5 10.5a1 1 0 11-2 0 1 1 0 012 0m0-6a1 1 0 11-2 0 1 1 0 012 0m3 3a1 1 0 11-2 0 1 1 0 012 0m-4-2v4m-1-6-1-1m4 4-1-1"/><path d="m9.06 1.06 5.88 5.88a1.5 1.5 0 010 2.12l-5.88 5.88a1.5 1.5 0 01-2.12 0L1.06 9.06a1.5 1.5 0 010-2.12l5.88-5.88a1.5 1.5 0 012.12 0"/>`)
	IconLangLock    = mustCatppuccin(`<path d="M12.36 7.1c.48 0 .87.39.87.87v5.23c0 .48-.39.87-.87.87H3.64a.87.87 0 01-.87-.87V7.97c0-.48.39-.87.87-.87zm-6.98 0V4.49a2.62 2.62 0 015.24 0V7.1"/>`)
	IconLangEnv     = mustCatppuccin(`<path d="M5.5 8.5V12m0-6.5V4m0 4.5a1.5 1.5 0 100-3 1.5 1.5 0 000 3m5 3.5v-1.5m0-3V4m0 6.5a1.5 1.5 0 100-3 1.5 1.5 0 000 3M4 1.5h8A2.5 2.5 0 0114.5 4v8a2.5 2.5 0 01-2.5 2.5H4A2.5 2.5 0 011.5 12V4A2.5 2.5 0 014 1.5"/>`)
	IconLangConfig  = mustCatppuccin(`<path d="M8 9.69a1.73 1.7 0 100-3.39 1.73 1.7 0 000 3.39m3.02-6.78 3.02 5.09-3.02 5.08H4.98L1.96 8l3.02-5.09z"/>`)
	IconLangSVG     = mustCatppuccin(`<path d="m4.54 10 6.92-4m-6.92 4a1.5 1.5 0 10-2.6 1.5 1.5 1.5 0 002.6-1.5M8 4v8m0-8a1.5 1.5 0 100-3 1.5 1.5 0 000 3M4.54 6l6.92 4M4.54 6a1.5 1.5 0 10-2.6-1.5A1.5 1.5 0 004.54 6M8 12a1.5 1.5 0 100 3 1.5 1.5 0 000-3m3.46-2a1.5 1.5 0 102.6 1.5 1.5 1.5 0 00-2.6-1.5m0-4a1.5 1.5 0 102.6-1.5 1.5 1.5 0 00-2.6 1.5"/>`)
	IconLangImage   = mustCatppuccin(`<path d="M11.5 6A1.5 1.5 0 118.5 6a1.5 1.5 0 013 0M7.5 13.5 11 10c.5-.5 1.5-.5 2 0l1.5 1.5m-13-2 2-2c.5-.5 1.5-.5 2 0l4 4M3 2.5h10c.83 0 1.5.67 1.5 1.5v8c0 .83-.67 1.5-1.5 1.5H3A1.5 1.5 0 011.5 12V4c0-.83.67-1.5 1.5-1.5"/>`)
	IconLangArchive = mustCatppuccin(`<path d="M5.5 10v1m1-2v1m-1-2v1m1-2v1m-1-2v1m1-2v1m-1-2v1m0-3v1m1 0v1m7 2.5v6a2 2 0 01-2 2h-7a2 2 0 01-2-2v-9c0-1.1.9-2 2-2h4.01m-.01 0 5 5h-4a1 1 0 01-1-1z"/>`)
)

func pickLangColor(dark bool, lightHex, darkHex string) ui.Color {
	if dark {
		return ui.Hex(darkHex)
	}
	return ui.Hex(lightHex)
}

// iconAndColorForFile returns the Catppuccin language icon and its tuned Light/Dark color.
func iconAndColorForFile(path string, t *ui.Theme) (*ui.SVG, ui.Color) {
	dark := t.Dark
	cyan := pickLangColor(dark, "#04A5E5", "#7DC4E4")   // Go, React, Dart, Tailwind
	blue := pickLangColor(dark, "#1E66F5", "#8AADF4")   // TS, Python, C/C++, Docker, PHP, Lua
	yellow := pickLangColor(dark, "#DF8E1D", "#EED49F") // JS, JSON, Zig, SQL, Env
	peach := pickLangColor(dark, "#FE640B", "#F5A97F")  // Rust, Swift, HTML, Svelte, Git, XML, SVG
	mauve := pickLangColor(dark, "#8839EF", "#C6A0F6")  // CSS, Kotlin, Elixir, Terraform, Astro
	red := pickLangColor(dark, "#D20F39", "#ED8796")    // Ruby, Java, Scala, YAML, TOML, PDF
	green := pickLangColor(dark, "#40A02B", "#A6DA95")  // Vue, Bash, CSV, CMake
	teal := pickLangColor(dark, "#179299", "#8BD5CA")   // Markdown, Readme, Prisma
	pink := pickLangColor(dark, "#EA76CB", "#F5BDE6")   // Sass, GraphQL
	slate := pickLangColor(dark, "#6C6F85", "#939AB7")  // Lock, Config, Archive

	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod", "go.work":
		return IconLangGoMod, cyan
	case "go.sum", "go.work.sum", "cargo.lock", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "uv.lock", "poetry.lock", "gemfile.lock", "composer.lock":
		return IconLangLock, slate
	case ".gitignore", ".gitattributes", ".gitmodules", ".gitkeep":
		return IconLangGit, peach
	case "dockerfile", "containerfile", "docker-compose.yml", "docker-compose.yaml", ".dockerignore":
		return IconLangDocker, blue
	case "makefile", "gnumakefile", "justfile":
		return IconLangMakefile, peach
	case "cmakelists.txt":
		return IconLangCMake, green
	case "readme", "readme.md", "readme.txt":
		return IconLangReadme, teal
	case "license", "license.md", "license.txt", "copying":
		return IconLangLicense, peach
	}
	if strings.HasPrefix(base, ".env") {
		return IconLangEnv, yellow
	}
	if strings.HasPrefix(base, "tailwind.config.") {
		return IconLangTailwind, cyan
	}
	if strings.HasPrefix(base, "dockerfile.") {
		return IconLangDocker, blue
	}

	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".go":
		return IconLangGo, cyan
	case ".ts", ".mts", ".cts":
		return IconLangTS, blue
	case ".tsx":
		return IconLangReact, blue
	case ".js", ".mjs", ".cjs":
		return IconLangJS, yellow
	case ".jsx":
		return IconLangReact, cyan
	case ".py", ".pyi", ".pyw":
		return IconLangPython, blue
	case ".rs":
		return IconLangRust, peach
	case ".c", ".h":
		return IconLangC, blue
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return IconLangCPP, blue
	case ".cs":
		return IconLangCSharp, mauve
	case ".java", ".jar", ".gradle":
		return IconLangJava, red
	case ".kt", ".kts":
		return IconLangKotlin, mauve
	case ".swift":
		return IconLangSwift, peach
	case ".dart":
		return IconLangDart, cyan
	case ".zig":
		return IconLangZig, yellow
	case ".rb", ".erb", ".gemspec":
		return IconLangRuby, red
	case ".php":
		return IconLangPHP, blue
	case ".lua":
		return IconLangLua, blue
	case ".ex", ".exs":
		return IconLangElixir, mauve
	case ".scala", ".sc":
		return IconLangScala, red
	case ".html", ".htm":
		return IconLangHTML, peach
	case ".css", ".less":
		return IconLangCSS, mauve
	case ".scss", ".sass":
		return IconLangSass, pink
	case ".vue":
		return IconLangVue, green
	case ".svelte":
		return IconLangSvelte, peach
	case ".astro":
		return IconLangAstro, mauve
	case ".json", ".jsonc", ".json5":
		return IconLangJSON, yellow
	case ".yaml", ".yml":
		return IconLangYAML, red
	case ".toml":
		return IconLangTOML, red
	case ".xml", ".plist", ".xhtml":
		return IconLangXML, peach
	case ".csv", ".tsv":
		return IconLangCSV, green
	case ".graphql", ".gql":
		return IconLangGraphQL, pink
	case ".proto":
		return IconLangProto, teal
	case ".prisma":
		return IconLangPrisma, teal
	case ".sql", ".sqlite", ".db":
		return IconLangSQL, yellow
	case ".md", ".mdx", ".markdown", ".rst":
		return IconLangMarkdown, teal
	case ".pdf":
		return IconLangPDF, red
	case ".tf", ".tfvars", ".hcl":
		return IconLangTerraform, mauve
	case ".nix":
		return IconLangNix, cyan
	case ".sh", ".bash", ".zsh", ".fish":
		return IconLangBash, green
	case ".mk", ".mak":
		return IconLangMakefile, peach
	case ".cmake":
		return IconLangCMake, green
	case ".svg":
		return IconLangSVG, peach
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".bmp":
		return IconLangImage, teal
	case ".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar":
		return IconLangArchive, slate
	case ".lock":
		return IconLangLock, slate
	case ".conf", ".ini", ".cfg", ".editorconfig":
		return IconLangConfig, slate
	default:
		return IconFile, t.TextMuted
	}
}

// renderFileIcon renders a file's language icon with its Catppuccin color and hover/select spring pop.
func renderFileIcon(c *ui.Context, path string, size float32, selected, hovered bool) {
	t := c.Theme()
	ic, col := iconAndColorForFile(path, t)
	scale := float32(1.0)
	nudgeX := float32(0)
	if selected && hovered {
		scale = 1.14
		nudgeX = 1.2
	} else if hovered {
		scale = 1.12
		nudgeX = 1.2
	} else if selected {
		scale = 1.07
	} else {
		col = col.Alpha(0.84)
	}
	el := ui.Icon(c, ic).Size(size, size).Shrink(0)
	el.TextColor(el.AnimateColor("ic", col, microColorAnim)).
		AnimatePose(ui.Pose{X: nudgeX, Scale: scale}, snappyPoseAnim)
}
