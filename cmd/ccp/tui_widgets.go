// Key events and the Input / TextArea / Select widgets, modelled on the Textual widgets they replace.
package main

import (
	"slices"
	"strings"
	"unicode"
)

type KeyCode int

const (
	KeyNone KeyCode = iota
	KeyChar
	KeyEnter
	KeyEsc
	KeyTab
	KeyBackTab
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
)

// Key is one key press, independent of the terminal library.
type Key struct {
	Code             KeyCode
	Ch               rune
	Ctrl, Shift, Alt bool
}

// printable is the character a key press types, if it is a plain printable key.
func printable(k Key) (rune, bool) {
	if k.Code == KeyChar && !k.Ctrl && !k.Alt {
		return k.Ch, true
	}
	return 0, false
}

func isCtrl(k Key, c rune) bool { return k.Code == KeyChar && k.Ctrl && k.Ch == c }

func wordLeft(s []rune, i int) int {
	for i > 0 && unicode.IsSpace(s[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(s[i-1]) {
		i--
	}
	return i
}

func wordRight(s []rune, i int) int {
	for i < len(s) && unicode.IsSpace(s[i]) {
		i++
	}
	for i < len(s) && !unicode.IsSpace(s[i]) {
		i++
	}
	return i
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// ------------------------------------------------------------------ Input

type Input struct {
	chars       []rune
	pos         int
	anchor      int // selection anchor, -1 = none
	password    bool
	placeholder string
	scroll      int
}

func NewInput(value string, password bool, placeholder string) *Input {
	chars := []rune(value)
	return &Input{chars: chars, pos: len(chars), anchor: -1, password: password, placeholder: placeholder}
}

func (in *Input) Value() string { return string(in.chars) }

func (in *Input) SetValue(v string) {
	in.chars = []rune(v)
	in.pos = len(in.chars)
	in.anchor = -1
}

// Focus selects the whole value, like Textual.
func (in *Input) Focus() {
	in.pos = len(in.chars)
	in.anchor = -1
	if len(in.chars) > 0 {
		in.anchor = 0
	}
}

func (in *Input) Blur() { in.anchor = -1 }

func (in *Input) selection() (int, int, bool) {
	if in.anchor < 0 || in.anchor == in.pos {
		return 0, 0, false
	}
	return min(in.anchor, in.pos), max(in.anchor, in.pos), true
}

func (in *Input) delete(a, b int) {
	in.chars = slices.Delete(in.chars, a, b)
	in.pos = a
	in.anchor = -1
}

func (in *Input) deleteSelection() bool {
	a, b, ok := in.selection()
	if ok {
		in.delete(a, b)
	}
	return ok
}

// Insert adds typed or pasted text (only the first line of a paste).
func (in *Input) Insert(s string) {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	in.deleteSelection()
	in.anchor = -1
	for _, c := range s {
		if !unicode.IsControl(c) {
			in.chars = slices.Insert(in.chars, in.pos, c)
			in.pos++
		}
	}
}

func (in *Input) goTo(to int, sel bool) {
	if !sel {
		in.anchor = -1
	} else if in.anchor < 0 {
		in.anchor = in.pos
	}
	in.pos = max(0, min(to, len(in.chars)))
}

func (in *Input) Key(k Key) bool {
	if c, ok := printable(k); ok {
		in.Insert(string(c))
		return true
	}
	n := len(in.chars)
	deleteRight := func() {
		if !in.deleteSelection() && in.pos < n {
			in.delete(in.pos, in.pos+1)
		}
	}
	switch {
	case k.Code == KeyLeft && k.Ctrl:
		in.goTo(wordLeft(in.chars, in.pos), k.Shift)
	case k.Code == KeyRight && k.Ctrl:
		in.goTo(wordRight(in.chars, in.pos), k.Shift)
	case k.Code == KeyLeft:
		if a, _, ok := in.selection(); ok && !k.Shift {
			in.goTo(a, false)
		} else {
			in.goTo(in.pos-1, k.Shift)
		}
	case k.Code == KeyRight:
		if _, b, ok := in.selection(); ok && !k.Shift {
			in.goTo(b, false)
		} else {
			in.goTo(in.pos+1, k.Shift)
		}
	case k.Code == KeyHome:
		in.goTo(0, k.Shift)
	case k.Code == KeyEnd:
		in.goTo(n, k.Shift)
	case isCtrl(k, 'a'):
		in.goTo(0, false)
	case isCtrl(k, 'e'):
		in.goTo(n, false)
	case k.Code == KeyBackspace:
		if !in.deleteSelection() && in.pos > 0 {
			in.delete(in.pos-1, in.pos)
		}
	case k.Code == KeyDelete, isCtrl(k, 'd'):
		deleteRight()
	case isCtrl(k, 'w'):
		if !in.deleteSelection() {
			in.delete(wordLeft(in.chars, in.pos), in.pos)
		}
	case isCtrl(k, 'u'):
		in.delete(0, in.pos)
	case isCtrl(k, 'k'):
		in.delete(in.pos, n)
	case k.Code == KeyEnter:
	default:
		return false
	}
	return true
}

func (in *Input) shown() []rune {
	if !in.password {
		return in.chars
	}
	return []rune(strings.Repeat("•", len(in.chars)))
}

// Click places the cursor at a click dx cells into the text.
func (in *Input) Click(dx int) {
	shown := in.shown()
	target := in.scroll + dx
	at, i := 0, 0
	for i < len(shown) && at+cw(shown[i]) <= target {
		at += cw(shown[i])
		i++
	}
	in.goTo(i, false)
}

func (in *Input) Render(c *Canvas, x, y, w int, focused, blink bool) {
	bg := cBoost
	if focused {
		bg = cFocus
	}
	c.fill(x, y, w, 1, bg)
	tx, end := x+1, x+w
	window := sub(w, 2)
	cursor := focused && blink
	if len(in.chars) == 0 {
		in.scroll = 0
		ph := cPlaceholder
		if focused {
			ph = cPlaceholderFoc
		}
		c.text(tx, y, in.placeholder, st(ph, bg), end)
		if cursor {
			c.restyle(tx, y, func(cell *Cell) { cell.fg, cell.bg = cCursorText, cCursor })
		}
		return
	}
	shown := in.shown()
	atEnd := 0
	if in.pos == len(shown) {
		atEnd = 1
	}
	total := runesWidth(shown)
	off := runesWidth(shown[:in.pos]) + atEnd
	if off < in.scroll {
		in.scroll = sub(off, atEnd)
	} else if off > in.scroll+window {
		in.scroll = off - window
	}
	in.scroll = min(in.scroll, sub(total+1, window))
	selA, selB, hasSel := in.selection()
	hasSel = hasSel && focused
	at, px := 0, tx
	for i, ch := range shown {
		n := cw(ch)
		if at < in.scroll {
			at += n
			continue
		}
		style := st(cFG, bg)
		if hasSel && selA <= i && i < selB {
			style.bg = cSelection
		}
		if cursor && i == in.pos {
			style = st(cCursorText, cCursor)
		}
		px = c.text(px, y, string(ch), style, end)
		if px >= end {
			return
		}
	}
	if cursor && in.pos == len(shown) {
		c.glyph(px, y, ' ', st(cCursorText, cCursor))
	}
}

// --------------------------------------------------------------- TextArea

type TextArea struct {
	lines       [][]rune
	row, col    int
	scroll      int
	placeholder string
	quick       bool
	wrapW       int
}

// vis is one screen line: a slice start..end of document line row.
type vis struct{ row, start, end int }

func NewTextArea(text, placeholder string, quick bool) *TextArea {
	t := &TextArea{placeholder: placeholder, quick: quick, wrapW: 60}
	t.SetText(text)
	t.row, t.col = 0, 0
	return t
}

func (t *TextArea) Text() string {
	lines := make([]string, len(t.lines))
	for i, l := range t.lines {
		lines[i] = string(l)
	}
	return strings.Join(lines, "\n")
}

// SetText replaces the content and puts the cursor at the end.
func (t *TextArea) SetText(text string) {
	t.lines = nil
	for _, l := range strings.Split(normalizeNewlines(text), "\n") {
		t.lines = append(t.lines, []rune(l))
	}
	t.row = len(t.lines) - 1
	t.col = len(t.lines[t.row])
}

func (t *TextArea) Insert(s string) {
	parts := strings.Split(normalizeNewlines(s), "\n")
	rest := slices.Clone(t.lines[t.row][t.col:])
	t.lines[t.row] = append(t.lines[t.row][:t.col:t.col], []rune(parts[0])...)
	t.col = len(t.lines[t.row])
	for _, part := range parts[1:] {
		t.row++
		t.lines = slices.Insert(t.lines, t.row, []rune(part))
		t.col = len(t.lines[t.row])
	}
	t.lines[t.row] = append(t.lines[t.row], rest...)
}

func (t *TextArea) layout(w int) []vis {
	var out []vis
	for row, line := range t.lines {
		for _, r := range wrap(line, w) {
			out = append(out, vis{row, r[0], r[1]})
		}
	}
	return out
}

func (t *TextArea) cursorVis(vs []vis) int {
	best := 0
	for i, v := range vs {
		if v.row == t.row && v.start <= t.col {
			best = i
		}
	}
	return best
}

func (t *TextArea) vertical(delta int) {
	vs := t.layout(t.wrapW)
	cur := t.cursorVis(vs)
	v := vs[cur]
	dx := runesWidth(t.lines[v.row][v.start:t.col])
	target := cur + delta
	if target < 0 {
		t.row, t.col = 0, 0
		return
	}
	if target >= len(vs) {
		t.row = len(t.lines) - 1
		t.col = len(t.lines[t.row])
		return
	}
	tv := vs[target]
	line := t.lines[tv.row]
	last := tv.end == len(line)
	col, at := tv.start, 0
	for col < tv.end && at+cw(line[col]) <= dx && (last || col+1 < tv.end) {
		at += cw(line[col])
		col++
	}
	t.row, t.col = tv.row, col
}

func (t *TextArea) deleteRight() {
	if t.col < len(t.lines[t.row]) {
		t.lines[t.row] = slices.Delete(t.lines[t.row], t.col, t.col+1)
	} else if t.row+1 < len(t.lines) {
		t.lines[t.row] = append(t.lines[t.row], t.lines[t.row+1]...)
		t.lines = slices.Delete(t.lines, t.row+1, t.row+2)
	}
}

func (t *TextArea) Key(k Key) bool {
	if c, ok := printable(k); ok {
		t.Insert(string(c))
		return true
	}
	n := len(t.lines[t.row])
	switch {
	case k.Code == KeyEnter:
		t.Insert("\n")
	case k.Code == KeyLeft:
		if t.col > 0 {
			t.col--
		} else if t.row > 0 {
			t.row--
			t.col = len(t.lines[t.row])
		}
	case k.Code == KeyRight:
		if t.col < n {
			t.col++
		} else if t.row+1 < len(t.lines) {
			t.row++
			t.col = 0
		}
	case k.Code == KeyUp:
		t.vertical(-1)
	case k.Code == KeyDown:
		t.vertical(1)
	case k.Code == KeyPageUp:
		t.vertical(-3)
	case k.Code == KeyPageDown:
		t.vertical(3)
	case k.Code == KeyHome, isCtrl(k, 'a'):
		t.col = 0
	case k.Code == KeyEnd, isCtrl(k, 'e'):
		t.col = n
	case k.Code == KeyBackspace:
		if t.col > 0 {
			t.col--
			t.lines[t.row] = slices.Delete(t.lines[t.row], t.col, t.col+1)
		} else if t.row > 0 {
			line := t.lines[t.row]
			t.lines = slices.Delete(t.lines, t.row, t.row+1)
			t.row--
			t.col = len(t.lines[t.row])
			t.lines[t.row] = append(t.lines[t.row], line...)
		}
	case k.Code == KeyDelete, isCtrl(k, 'd'):
		t.deleteRight()
	case isCtrl(k, 'w'):
		to := wordLeft(t.lines[t.row], t.col)
		t.lines[t.row] = slices.Delete(t.lines[t.row], to, t.col)
		t.col = to
	case isCtrl(k, 'u'):
		t.lines[t.row] = slices.Delete(t.lines[t.row], 0, t.col)
		t.col = 0
	case isCtrl(k, 'k'):
		if t.col == n && t.row+1 < len(t.lines) {
			t.deleteRight()
		} else {
			t.lines[t.row] = t.lines[t.row][:t.col]
		}
	default:
		return false
	}
	return true
}

func (t *TextArea) Click(dx, dy int) {
	vs := t.layout(t.wrapW)
	if len(vs) == 0 {
		return
	}
	v := vs[min(t.scroll+dy, len(vs)-1)]
	col, at := v.start, 0
	for col < v.end && at+cw(t.lines[v.row][col]) <= dx {
		at += cw(t.lines[v.row][col])
		col++
	}
	t.row, t.col = v.row, col
}

func (t *TextArea) IsEmpty() bool { return len(t.lines) == 1 && len(t.lines[0]) == 0 }

func (t *TextArea) Render(c *Canvas, x, y, w, h int, focused, blink bool) {
	var bg, lineBg, ph Rgb
	switch {
	case t.quick && !focused:
		bg, lineBg, ph = cQuick, cQuickLine, cQuickPH
	case t.quick:
		bg, lineBg, ph = cQuickFocus, cQuickLineFocus, cQuickPHFocus
	case !focused:
		bg, lineBg, ph = cBoost, cAreaLine, cAreaPH
	default:
		bg, lineBg, ph = cAreaFocus, cAreaLineFocus, cAreaPH
	}
	c.fill(x, y, w, h, bg)
	tx := x + 1
	tw := sub(w, 2)
	cursor := focused && blink
	if t.IsEmpty() {
		t.wrapW = tw
		t.scroll = 0
		chars := []rune(t.placeholder)
		for i, r := range wrap(chars, tw) {
			if i >= h {
				break
			}
			c.text(tx, y+i, strings.TrimRightFunc(string(chars[r[0]:r[1]]), unicode.IsSpace), st(ph, bg), tx+tw)
		}
		if cursor {
			c.restyle(tx, y, func(cell *Cell) { cell.fg, cell.bg = cCursorText, cCursor })
		}
		return
	}
	vs := t.layout(tw)
	bar := len(vs) > h
	if bar {
		tw = sub(tw, 2)
		vs = t.layout(tw)
	}
	t.wrapW = tw
	cur := t.cursorVis(vs)
	if cur < t.scroll {
		t.scroll = cur
	} else if cur >= t.scroll+h {
		t.scroll = cur + 1 - h
	}
	t.scroll = min(t.scroll, sub(len(vs), h))
	for i := 0; i < h && t.scroll+i < len(vs); i++ {
		v := vs[t.scroll+i]
		yy := y + i
		lbg := bg
		if t.scroll+i == cur {
			lbg = lineBg
		}
		c.fill(tx, yy, tw, 1, lbg)
		px := tx
		for col := v.start; col < v.end; col++ {
			style := st(cFG, lbg)
			if cursor && v.row == t.row && col == t.col {
				style = st(cCursorText, cCursor)
			}
			px = c.text(px, yy, string(t.lines[v.row][col]), style, tx+tw)
		}
		if cursor && t.scroll+i == cur && t.col == v.end && t.col == len(t.lines[v.row]) {
			c.glyph(px, yy, ' ', st(cCursorText, cCursor))
		}
	}
	if bar {
		scrollbar(c, tx+tw, y, h, len(vs), h, t.scroll)
	}
}

// ----------------------------------------------------------------- Select

type Select struct {
	options   []string
	value     int
	open      bool
	highlight int
}

func NewSelect(options []string, value string) *Select {
	i := max(slices.Index(options, value), 0)
	return &Select{options: options, value: i, highlight: i}
}

func (s *Select) Value() string {
	if s.value < len(s.options) {
		return s.options[s.value]
	}
	return ""
}

func (s *Select) Set(v string) {
	if i := slices.Index(s.options, v); i >= 0 {
		s.value = i
	}
}

func (s *Select) Open() {
	s.open = true
	s.highlight = s.value
}

func (s *Select) Pick(i int) {
	if i >= 0 && i < len(s.options) {
		s.value = i
	}
	s.open = false
}

func (s *Select) Key(k Key) bool {
	n := len(s.options)
	space := k.Code == KeyChar && k.Ch == ' '
	if !s.open {
		if k.Code == KeyEnter || k.Code == KeyDown || k.Code == KeyUp || space {
			s.Open()
			return true
		}
		return false
	}
	switch {
	case k.Code == KeyUp:
		s.highlight = sub(s.highlight, 1)
	case k.Code == KeyDown:
		s.highlight = min(s.highlight+1, sub(n, 1))
	case k.Code == KeyHome, k.Code == KeyPageUp:
		s.highlight = 0
	case k.Code == KeyEnd, k.Code == KeyPageDown:
		s.highlight = sub(n, 1)
	case k.Code == KeyEnter, space:
		s.Pick(s.highlight)
	case k.Code == KeyEsc:
		s.open = false
	}
	return true
}

func (s *Select) Render(c *Canvas, x, y, w int, focused bool) {
	focused = focused && !s.open
	bg, arrow := cBoost, cArrow
	if focused {
		bg, arrow = cFocus, cArrowFocus
	}
	c.fill(x, y, w, 1, bg)
	arrowX := sub(x+w, 2)
	c.text(x+1, y, s.Value(), st(cFG, bg), sub(arrowX, 1))
	ch := '▼'
	if s.open {
		ch = '▲'
	}
	c.glyph(arrowX, y, ch, st(arrow, bg))
}

// RenderOverlay draws the drop-down list on top of everything below the select.
func (s *Select) RenderOverlay(c *Canvas, x, y, w int) {
	if !s.open || w < 4 {
		return
	}
	rows := s.OverlayRows()
	for i := 0; i < rows; i++ {
		yy := y + i
		c.glyph(x, yy, '▊', st(cSurface, cBlue))
		c.glyph(x+w-1, yy, '▎', st(cBlue, cSurface))
		if i == 0 || i == rows-1 {
			edge := '▔'
			if i > 0 {
				edge = '▁'
			}
			for xx := x + 1; xx < x+w-1; xx++ {
				c.glyph(xx, yy, edge, st(cBlue, cOptions))
			}
			continue
		}
		o := i - 1
		c.fill(x+1, yy, w-2, 1, cOptions)
		style := st(cFG, cOptions)
		if o == s.highlight {
			style = st(cCursorFG, cBlue).Bold()
		}
		c.fill(x+2, yy, w-4, 1, style.bg)
		c.text(x+2, yy, " "+s.options[o], style, x+w-2)
	}
}

func (s *Select) OverlayRows() int { return len(s.options) + 2 }
