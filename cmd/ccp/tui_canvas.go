// A plain cell grid we paint the whole UI into, the Textual-dark palette, and
// cell-width helpers with rich's semantics (truncate with "…", word wrap).
package main

import (
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// ---------------------------------------------------------------- text

// cw is the number of terminal cells a character takes.
func cw(c rune) int {
	if c == '\t' {
		return 1
	}
	if c < 0x7f {
		if c < 0x20 {
			return 0
		}
		return 1
	}
	return uniseg.StringWidth(string(c))
}

func width(s string) int {
	n := 0
	for _, c := range s {
		n += cw(c)
	}
	return n
}

func runesWidth(rs []rune) int {
	n := 0
	for _, c := range rs {
		n += cw(c)
	}
	return n
}

// sub is a - b, but never below zero.
func sub(a, b int) int { return max(a-b, 0) }

// setCellSize crops or pads to exactly w cells (a split wide char becomes a space).
func setCellSize(s string, w int) string {
	var out strings.Builder
	used := 0
	for _, c := range s {
		n := cw(c)
		if used+n > w {
			break
		}
		out.WriteRune(c)
		used += n
	}
	out.WriteString(strings.Repeat(" ", w-used))
	return out.String()
}

// truncate is rich's `Text.truncate(w, overflow="ellipsis")`.
func truncate(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	return setCellSize(s, w-1) + "…"
}

// wrap returns the rune ranges of the lines rich / Textual would wrap s into
// (words keep their trailing spaces).
func wrap(chars []rune, w int) [][2]int {
	w = max(w, 1)
	n := len(chars)
	// words: leading whitespace (only at the start), non-space run, trailing whitespace
	var words [][2]int
	for i := 0; i < n; {
		start := i
		for i < n && unicode.IsSpace(chars[i]) {
			i++
		}
		for i < n && !unicode.IsSpace(chars[i]) {
			i++
		}
		for i < n && unicode.IsSpace(chars[i]) {
			i++
		}
		words = append(words, [2]int{start, i})
	}
	var breaks []int
	offset := 0
	for _, word := range words {
		start, end := word[0], word[1]
		trimmed := end
		for trimmed > start && unicode.IsSpace(chars[trimmed-1]) {
			trimmed--
		}
		wordLen, fullLen := runesWidth(chars[start:trimmed]), runesWidth(chars[start:end])
		switch {
		case w >= offset+wordLen:
			offset += fullLen
		case wordLen > w:
			// fold the word across lines
			lineW, lineStart := 0, start
			for pos := start; pos < end; pos++ {
				n := cw(chars[pos])
				if lineW+n > w && pos > lineStart {
					if lineStart > 0 {
						breaks = append(breaks, lineStart)
					}
					lineStart, lineW = pos, 0
				}
				lineW += n
			}
			if lineStart > 0 {
				breaks = append(breaks, lineStart)
			}
			offset = lineW
		case offset > 0 && start > 0:
			breaks = append(breaks, start)
			offset = fullLen
		}
	}
	breaks = slices.Compact(breaks)
	var out [][2]int
	prev := 0
	for _, b := range breaks {
		if b > prev {
			out = append(out, [2]int{prev, b})
			prev = b
		}
	}
	return append(out, [2]int{prev, n})
}

// ---------------------------------------------------------------- palette

type Rgb struct{ r, g, b uint8 }

func hex(v uint32) Rgb { return Rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)} }

// Textual "textual-dark", resolved to the cells it renders.
var (
	cFG             = hex(0xe0e0e0)
	cScreen         = hex(0x121212)
	cHeader         = hex(0x242f38)
	cTabDim         = hex(0x797979)
	cRule           = hex(0x262626)
	cBlue           = hex(0x0178d4)
	cTableHead      = hex(0x2d3740)
	cRowEven        = hex(0x1c1c1c)
	cRowOdd         = hex(0x272727)
	cCursorFG       = hex(0xddedf9)
	cGreen          = hex(0x98e024)
	cKey            = hex(0xffa62b)
	cToast          = hex(0x343f49)
	cToastInfo      = hex(0x4ebf71)
	cToastTitle     = hex(0x8ad4a1)
	cToastWarn      = hex(0xfea62b)
	cToastErr       = hex(0xb93c5b)
	cSurface        = hex(0x1e1e1e)
	cAccent         = hex(0xfea62b)
	cMuted          = hex(0xa5a5a5)
	cBoost          = hex(0x262626)
	cFocus          = hex(0x554023)
	cSelection      = hex(0x485d72)
	cCursor         = hex(0xe0e0e0)
	cCursorText     = hex(0x121212)
	cPlaceholder    = hex(0x787878)
	cPlaceholderFoc = hex(0x958876)
	cArrow          = hex(0x838383)
	cArrowFocus     = hex(0x9a9081)
	cQuick          = hex(0x233127)
	cQuickFocus     = hex(0x2a4632)
	cQuickLine      = hex(0x2b392f)
	cQuickLineFocus = hex(0x324d3a)
	cQuickPH        = hex(0x7b837d)
	cQuickPHFocus   = hex(0x7f9084)
	cAreaLine       = hex(0x2e2e2e)
	cAreaFocus      = hex(0x564021)
	cAreaLineFocus  = hex(0x5c4729)
	cAreaPH         = hex(0x7c7c7c)
	cOptions        = hex(0x272727)
	cBar            = hex(0x003054)
	cBarBack        = hex(0x000000)
)

func mix(from, to Rgb, t float64) Rgb {
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return Rgb{f(from.r, to.r), f(from.g, to.g), f(from.b, to.b)}
}

// dim is rich's "dim": the colour 66% of the way from the background to fg.
func dim(fg, bg Rgb) Rgb { return mix(bg, fg, 0.66) }

// backdrop is the modal backdrop: every colour 60% of the way towards the screen background.
func backdrop(c Rgb) Rgb { return mix(c, cScreen, 0.6) }

// ---------------------------------------------------------------- canvas

// Cell is one screen cell; ch == 0 marks the right half of a wide character.
type Cell struct {
	ch     rune
	fg, bg Rgb
	bold   bool
}

// Style without hasBg keeps the background that is already there.
type Style struct {
	fg, bg Rgb
	hasBg  bool
	bold   bool
}

func st(fg, bg Rgb) Style { return Style{fg: fg, bg: bg, hasBg: true} }

func (s Style) Bold() Style {
	s.bold = true
	return s
}

type Canvas struct {
	w, h  int
	cells []Cell
}

func NewCanvas(w, h int) *Canvas {
	c := &Canvas{w: w, h: h, cells: make([]Cell, w*h)}
	for i := range c.cells {
		c.cells[i] = Cell{ch: ' ', fg: cFG, bg: cScreen}
	}
	return c
}

func (c *Canvas) cell(x, y int) *Cell { return &c.cells[y*c.w+x] }

// put writes one cell, blanking any wide character it cuts in half.
func (c *Canvas) put(x, y int, cell Cell) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	i := y*c.w + x
	old := c.cells[i].ch
	if old == 0 && x > 0 {
		c.cells[i-1].ch = ' '
	}
	if old != 0 && cw(old) == 2 && x+1 < c.w && c.cells[i+1].ch == 0 {
		c.cells[i+1].ch = ' '
	}
	c.cells[i] = cell
}

func (c *Canvas) fill(x, y, w, h int, bg Rgb) {
	for yy := y; yy < min(y+h, c.h); yy++ {
		for xx := x; xx < min(x+w, c.w); xx++ {
			c.put(xx, yy, Cell{ch: ' ', fg: cFG, bg: bg})
		}
	}
}

// text draws s from x, never past column limit; returns the column after the text.
func (c *Canvas) text(x, y int, s string, style Style, limit int) int {
	limit = min(limit, c.w)
	for _, ch := range s {
		n := cw(ch)
		if n == 0 {
			continue
		}
		if x+n > limit {
			if x < limit {
				c.glyph(x, y, ' ', style)
				x++
			}
			break
		}
		c.glyph(x, y, ch, style)
		if n == 2 {
			c.glyph(x+1, y, 0, style)
		}
		x += n
	}
	return x
}

func (c *Canvas) glyph(x, y int, ch rune, style Style) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	bg := style.bg
	if !style.hasBg {
		bg = c.cells[y*c.w+x].bg
	}
	c.put(x, y, Cell{ch: ch, fg: style.fg, bg: bg, bold: style.bold})
}

// restyle changes a single cell (and its other half, for wide characters).
func (c *Canvas) restyle(x, y int, f func(*Cell)) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	i := y*c.w + x
	f(&c.cells[i])
	if ch := c.cells[i].ch; ch != 0 && cw(ch) == 2 && x+1 < c.w {
		f(&c.cells[i+1])
	}
}

// blit copies rows rows of src, starting at its row sy, to (dx, dy).
func (c *Canvas) blit(src *Canvas, sy, dx, dy, rows int) {
	for r := 0; r < min(rows, sub(src.h, sy)); r++ {
		for x := 0; x < src.w; x++ {
			if dx+x < c.w && dy+r < c.h {
				c.put(dx+x, dy+r, *src.cell(x, sy+r))
			}
		}
	}
}

// String is the screen as plain text (for tests and the headless renderer).
func (c *Canvas) String() string {
	var out strings.Builder
	for y := 0; y < c.h; y++ {
		var line strings.Builder
		for x := 0; x < c.w; x++ {
			if ch := c.cell(x, y).ch; ch != 0 {
				line.WriteRune(ch)
			}
		}
		out.WriteString(strings.TrimRight(line.String(), " "))
		out.WriteByte('\n')
	}
	return out.String()
}

// scrollbar is Textual's `ScrollBar.render_bar` (vertical, 2 cells thick).
func scrollbar(c *Canvas, x, y, size, virt, window, pos int) {
	bars := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', ' '}
	type bc struct {
		ch     rune
		fg, bg Rgb
	}
	cells := make([]bc, max(size, 0))
	for i := range cells {
		cells[i] = bc{' ', cFG, cBarBack}
	}
	if size > 0 && virt > 0 && window > 0 && size != virt {
		thumb := math.Max(float64(window)/(float64(virt)/float64(size)), 1)
		ratio := 0.0
		if virt > window {
			ratio = float64(pos) / float64(virt-window)
		}
		position := (float64(size) - thumb) * ratio
		start := int(position * 8)
		end := start + int(math.Ceil(thumb*8))
		si, sb := start/8, start%8
		ei, eb := end/8, end%8
		for i := si; i < min(ei, size); i++ {
			cells[i] = bc{' ', cFG, cBar}
		}
		if si < size && bars[7-sb] != ' ' {
			cells[si] = bc{bars[7-sb], cBar, cBarBack}
		}
		if ei < size && bars[7-eb] != ' ' {
			cells[ei] = bc{bars[7-eb], cBarBack, cBar}
		}
	}
	for i, b := range cells {
		for dx := 0; dx < 2; dx++ {
			c.glyph(x+dx, y+i, b.ch, st(b.fg, b.bg))
		}
	}
}
