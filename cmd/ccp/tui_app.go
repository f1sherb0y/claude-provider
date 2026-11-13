// Application state, drawing and input handling.
package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Sev int

const (
	SevInfo Sev = iota
	SevWarn
	SevErr
)

type toast struct {
	title, msg string
	sev        Sev
	until      time.Time
}

var footerKeys = []struct {
	key  rune
	desc string
}{
	{'a', "新增"},
	{'v', "剪贴板新增"},
	{'e', "编辑"},
	{'c', "复制"},
	{'d', "删除"},
	{'q', "退出"},
}

const editorSubtitle = " Ctrl+S 保存 · Esc 取消 · Ctrl+R 显示 Key "

func dictText(d *Dict) string {
	lines := make([]string, 0, d.Len())
	for _, k := range d.Keys() {
		v, _ := d.Get(k)
		lines = append(lines, k+"="+v)
	}
	return strings.Join(lines, "\n")
}

func parseEnvLines(text string) *Dict {
	env := NewOMap[string]()
	for _, line := range splitLines(text) {
		line = strip(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			env.Set(strip(k), strip(v))
		}
	}
	return env
}

// ------------------------------------------------------------------ editor

// widget is exactly one of the three.
type widget struct {
	input *Input
	area  *TextArea
	sel   *Select
}

func (w widget) height() int {
	if w.area != nil {
		return 3
	}
	return 1
}

type editor struct {
	title    string
	original int // index being edited, -1 for a new provider
	base     *Provider
	quick    *TextArea
	widgets  []widget
	// 0 = the quick-paste box, i + 1 = field i
	focus       int
	scroll      int
	followFocus bool
	// the name we filled in from a paste (still replaceable by a later paste)
	autoName    string
	hasAutoName bool
	labels      []string
}

type geom struct {
	x, y, dw, dh, cx, cw, vh, contentH int
	bar                                bool
}

func newEditor(b *Backend, p *Provider, title string, original int) *editor {
	ed := &editor{title: title, original: original, base: p, followFocus: true}
	nameField := 0
	for i, f := range b.Fields() {
		switch f.Kind {
		case KindSelect:
			ed.widgets = append(ed.widgets, widget{sel: NewSelect(f.Options, p.S(f.Name))})
		case KindDict:
			ed.widgets = append(ed.widgets, widget{area: NewTextArea(dictText(p.Dict(f.Name)), f.Placeholder, false)})
		default:
			ed.widgets = append(ed.widgets, widget{input: NewInput(p.S(f.Name), f.Kind == KindPassword, f.Placeholder)})
		}
		ed.labels = append(ed.labels, f.Label)
		if f.Name == "name" {
			nameField = i + 1
		}
	}
	ed.quick = NewTextArea("", "⚡ 粘贴 "+b.PasteHint()+"，自动识别", true)
	if original < 0 {
		ed.setFocus(0)
	} else {
		ed.setFocus(nameField)
	}
	return ed
}

func (ed *editor) input(b *Backend, name string) *Input {
	for i, f := range b.Fields() {
		if f.Name == name {
			return ed.widgets[i].input
		}
	}
	return nil
}

func (ed *editor) setFocus(to int) {
	if ed.focus > 0 {
		w := ed.widgets[ed.focus-1]
		if w.input != nil {
			w.input.Blur()
		}
		if w.sel != nil {
			w.sel.open = false
		}
	}
	ed.focus = to
	if to > 0 && ed.widgets[to-1].input != nil {
		ed.widgets[to-1].input.Focus()
	}
	ed.followFocus = true
}

func (ed *editor) openSelect() *Select {
	if ed.focus > 0 {
		if s := ed.widgets[ed.focus-1].sel; s != nil && s.open {
			return s
		}
	}
	return nil
}

// fill puts a parsed paste into the form; returns the "recognised" message.
func (ed *editor) fill(b *Backend, info *Info) string {
	for i, f := range b.Fields() {
		value, ok := info.Get(f.Name)
		if !ok {
			continue
		}
		w := ed.widgets[i]
		switch {
		case value.D != nil && w.area != nil && value.D.Len() > 0:
			w.area.SetText(dictText(value.D))
		case value.D == nil && value.S != "" && w.sel != nil:
			w.sel.Set(value.S)
		case value.D == nil && value.S != "" && w.input != nil:
			if f.Name != "name" {
				w.input.SetValue(value.S)
				continue
			}
			if cur := w.input.Value(); cur != "" && !(ed.hasAutoName && ed.autoName == cur) {
				continue
			}
			name := b.UniqueName(value.S, ed.original)
			w.input.SetValue(name)
			ed.autoName, ed.hasAutoName = name, true
		}
	}
	if info.Len() == 0 {
		return ""
	}
	return "已识别：" + strings.Join(info.Keys(), ", ")
}

func (ed *editor) quickChanged(b *Backend) string {
	text := ed.quick.Text()
	if strip(text) == "" {
		return ""
	}
	return ed.fill(b, b.Parse(text))
}

func (ed *editor) values(b *Backend) (*Provider, error) {
	p := ed.base.Clone()
	for i, f := range b.Fields() {
		switch w := ed.widgets[i]; {
		case w.area != nil:
			p.SetDict(f.Name, parseEnvLines(w.area.Text()))
		case w.sel != nil:
			p.Set(f.Name, w.sel.Value())
		default:
			p.Set(f.Name, strip(w.input.Value()))
		}
	}
	if p.Name() == "" && p.BaseURL() != "" {
		p.Set("name", infoStr(b.Parse(p.BaseURL()), "name"))
	}
	name := p.Name()
	if name == "" {
		return nil, fmt.Errorf("名称不能为空")
	}
	for i, q := range b.Providers {
		if q.Name() == name && i != ed.original {
			return nil, fmt.Errorf("名称 %s 已存在", name)
		}
	}
	return p, nil
}

// rows returns (content row, height) of widget i (0 = quick box).
func (ed *editor) rows() [][2]int {
	out := [][2]int{{0, 2}}
	y := 3
	for _, w := range ed.widgets {
		out = append(out, [2]int{y, w.height()})
		y += w.height()
	}
	return out
}

func (ed *editor) geom(w, h int) geom {
	dw := min(max(w*9/10, 30), 84, w)
	contentH := 3
	for _, wd := range ed.widgets {
		contentH += wd.height()
	}
	dh := min(contentH+2, h)
	x, y := (w-dw)/2, (h-dh)/2
	vh := sub(dh, 2)
	bar := contentH > vh
	pad := 4
	if bar {
		pad = 6
	}
	return geom{x: x, y: y, dw: dw, dh: dh, cx: x + 2, cw: sub(dw, pad), vh: vh, contentH: contentH, bar: bar}
}

func (ed *editor) render(c *Canvas, blink bool) {
	g := ed.geom(c.w, c.h)
	if g.dw < 6 || g.dh < 3 {
		return
	}
	// frame
	c.fill(g.x, g.y, g.dw, g.dh, cSurface)
	border := st(cAccent, cSurface)
	right, bottom := g.x+g.dw-1, g.y+g.dh-1
	for xx := g.x + 1; xx < right; xx++ {
		c.glyph(xx, g.y, '─', border)
		c.glyph(xx, bottom, '─', border)
	}
	for yy := g.y + 1; yy < bottom; yy++ {
		c.glyph(g.x, yy, '│', border)
		c.glyph(right, yy, '│', border)
	}
	c.glyph(g.x, g.y, '╭', border)
	c.glyph(right, g.y, '╮', border)
	c.glyph(g.x, bottom, '╰', border)
	c.glyph(right, bottom, '╯', border)
	c.text(g.x+2, g.y, truncate(" "+ed.title+" ", sub(g.dw, 4)), border.Bold(), right)
	subt := truncate(editorSubtitle, sub(g.dw, 4))
	c.text(right-1-width(subt), bottom, subt, st(cMuted, cSurface), right-1)

	// content, drawn off-screen then clipped into the viewport
	rows := ed.rows()
	if ed.followFocus {
		ry, rh := rows[ed.focus][0], rows[ed.focus][1]
		if ry < ed.scroll {
			ed.scroll = ry
		} else if ry+rh > ed.scroll+g.vh {
			ed.scroll = ry + rh - g.vh
		}
		ed.followFocus = false
	}
	ed.scroll = max(0, min(ed.scroll, sub(g.contentH, g.vh)))
	oc := NewCanvas(g.cw, g.contentH)
	oc.fill(0, 0, g.cw, g.contentH, cSurface)
	ed.quick.Render(oc, 0, 0, g.cw, 2, ed.focus == 0, blink)
	for i, w := range ed.widgets {
		ry, rh := rows[i+1][0], rows[i+1][1]
		focused := ed.focus == i+1
		open := w.sel != nil && w.sel.open
		label := st(cMuted, cSurface)
		if focused && !open {
			label = st(cAccent, cSurface).Bold()
		}
		oc.text(0, ry, ed.labels[i], label, 10)
		wx, ww := 10, sub(g.cw, 10)
		switch {
		case w.input != nil:
			w.input.Render(oc, wx, ry, ww, focused, blink)
		case w.area != nil:
			w.area.Render(oc, wx, ry, ww, rh, focused, blink)
		default:
			w.sel.Render(oc, wx, ry, ww, focused)
		}
	}
	c.blit(oc, ed.scroll, g.cx, g.y+1, g.vh)
	if g.bar {
		scrollbar(c, g.cx+g.cw, g.y+1, g.vh, g.contentH, g.vh, ed.scroll)
	}
	if s := ed.openSelect(); s != nil {
		if ry := rows[ed.focus][0]; ry >= ed.scroll {
			s.RenderOverlay(c, g.cx+10, g.y+1+ry-ed.scroll+1, sub(g.cw, 10))
		}
	}
}

// ------------------------------------------------------------------- app

type pendingDelete struct {
	idx  int
	name string
	at   time.Time
}

type hitBox struct{ x, y, w, h, id int }

type App struct {
	store       *Store
	tool        Tool
	cursor, top int
	active      int
	editor      *editor
	toasts      []toast
	pending     *pendingDelete
	quit        bool
	w, h        int
	lastInput   time.Time
	// headless renders never blink the cursor off
	steadyCursor bool
	footerHits   []struct {
		a, b int
		key  rune
	}
	toastHits []hitBox
}

func NewApp(store *Store, tool Tool, hasTool bool) *App {
	if !hasTool {
		tool, _ = toolFromID(store.LastTab)
	}
	app := &App{store: store, tool: tool, active: -1, w: 80, h: 24, lastInput: time.Now()}
	app.refresh(-1)
	app.cursor = max(app.active, 0)
	return app
}

func (a *App) backend() *Backend { return a.store.Get(a.tool) }

func (a *App) notify(title, msg string, sev Sev, secs float64) {
	until := time.Now().Add(time.Duration(secs * float64(time.Second)))
	// each paste recognition supersedes the previous one
	if strings.HasPrefix(msg, "已识别：") {
		a.toasts = slices.DeleteFunc(a.toasts, func(t toast) bool {
			return strings.HasPrefix(t.msg, "已识别：") && t.msg != msg
		})
	}
	// a repeat of a visible toast just refreshes it instead of stacking up
	for i := range a.toasts {
		if a.toasts[i].msg == msg && a.toasts[i].title == title {
			a.toasts[i].until = until
			return
		}
	}
	a.toasts = append(a.toasts, toast{title, msg, sev, until})
}

func (a *App) info(msg string)  { a.notify("", msg, SevInfo, 5) }
func (a *App) error(msg string) { a.notify("", msg, SevErr, 5) }

// refresh re-reads the active provider from disk; focus >= 0 moves the cursor there.
func (a *App) refresh(focus int) {
	active, err := a.backend().Active()
	a.active = active
	if err != nil {
		a.active = -1
		a.error(fmt.Sprintf("读取 %s 失败: %v", a.backend().Target(), err))
	}
	if focus >= 0 {
		a.cursor = focus
	}
	a.cursor = min(a.cursor, sub(len(a.backend().Providers), 1))
}

func (a *App) selected() int {
	if a.cursor < len(a.backend().Providers) {
		return a.cursor
	}
	return -1
}

func (a *App) switchTo(idx int) {
	note, err := a.store.Apply(a.tool, idx)
	if err != nil {
		a.error(fmt.Sprintf("写入失败: %v", err))
		return
	}
	a.refresh(idx)
	msg := fmt.Sprintf("%s → %s，新开会话生效", a.backend().Label(), a.backend().Providers[idx].Name())
	a.notify("✔ 切换成功", msg, SevInfo, 5)
	if note != "" {
		a.notify("", note, SevWarn, 10)
	}
}

func (a *App) setTab(tool Tool) {
	if tool == a.tool {
		return
	}
	a.tool = tool
	a.pending = nil
	a.store.LastTab = tool.ID()
	if err := a.store.Save(); err != nil {
		a.error(fmt.Sprintf("写入失败: %v", err))
	}
	a.cursor, a.top = 0, 0
	a.refresh(-1)
	a.cursor = max(a.active, 0)
}

func (a *App) cycleTab(delta int) {
	i := slices.Index(allTools, a.tool)
	n := len(allTools)
	a.setTab(allTools[((i+delta)%n+n)%n])
}

func (a *App) openEditor(p *Provider, title string, original int, paste string) {
	b := a.backend()
	ed := newEditor(b, p, title+" · "+b.Label(), original)
	msg := ""
	if paste != "" {
		ed.quick.SetText(paste)
		msg = ed.quickChanged(b)
	}
	a.editor = ed
	if msg != "" {
		a.notify("", msg, SevInfo, 2)
	}
}

func (a *App) action(c rune) {
	b := a.backend()
	switch {
	case c == 'a':
		a.openEditor(b.Blank(), "新增 Provider", -1, "")
	case c == 'v':
		text := readClipboard()
		if infoStr(b.Parse(text), "base_url") == "" {
			a.notify("", "剪贴板里没有识别到 Base URL", SevWarn, 5)
		} else {
			a.openEditor(b.Blank(), "从剪贴板新增", -1, text)
		}
	case c == 'e':
		if i := a.selected(); i >= 0 {
			p := b.Providers[i].Clone()
			a.openEditor(p, "编辑 "+p.Name(), i, "")
		}
	case c == 'c':
		if i := a.selected(); i >= 0 {
			p := b.Providers[i].Clone()
			title := "复制 " + p.Name()
			p.Set("name", b.UniqueName(p.Name(), -1))
			if p.Has("id") {
				p.Set("id", "")
			}
			a.openEditor(p, title, -1, "")
		}
	case c == 'd':
		a.delete()
	case c == 'J' || c == 'K':
		if i := a.selected(); i >= 0 {
			a.pending = nil
			delta := 1
			if c == 'K' {
				delta = -1
			}
			j, err := a.store.MoveBy(a.tool, i, delta)
			if err != nil {
				a.error(fmt.Sprintf("写入失败: %v", err))
			} else {
				a.refresh(j)
			}
		}
	case c == 'q':
		a.quit = true
	case c >= '1' && c <= '9':
		if i := int(c - '1'); i < len(b.Providers) {
			a.switchTo(i)
		}
	}
}

func (a *App) delete() {
	i := a.selected()
	if i < 0 {
		return
	}
	name := a.backend().Providers[i].Name()
	if p := a.pending; p != nil && p.idx == i && p.name == name && time.Since(p.at) < 3*time.Second {
		a.pending = nil
		if err := a.store.Remove(a.tool, i); err != nil {
			a.error(fmt.Sprintf("写入失败: %v", err))
			return
		}
		a.refresh(-1)
		a.info("已删除 " + name)
		return
	}
	a.pending = &pendingDelete{i, name, time.Now()}
	a.notify("", "再按一次 d 删除 "+name, SevWarn, 3)
}

// ---------------------------------------------------------------- input

func (a *App) Key(k Key) {
	a.lastInput = time.Now()
	if isCtrl(k, 'q') {
		a.quit = true
		return
	}
	if a.editor != nil {
		a.editorKey(k)
		return
	}
	if isCtrl(k, 'c') {
		a.quit = true
		return
	}
	n := len(a.backend().Providers)
	page := max(sub(a.h, 5), 1)
	switch k.Code {
	case KeyEnter:
		if i := a.selected(); i >= 0 {
			a.switchTo(i)
		}
	case KeyLeft:
		a.cycleTab(-1)
	case KeyRight:
		a.cycleTab(1)
	case KeyUp:
		a.cursor = sub(a.cursor, 1)
	case KeyDown:
		a.cursor = min(a.cursor+1, sub(n, 1))
	case KeyHome:
		a.cursor = 0
	case KeyEnd:
		a.cursor = sub(n, 1)
	case KeyPageUp:
		a.cursor = sub(a.cursor, page)
	case KeyPageDown:
		a.cursor = min(a.cursor+page, sub(n, 1))
	default:
		if c, ok := printable(k); ok {
			a.action(c)
		}
	}
}

func (a *App) editorKey(k Key) {
	b, ed := a.backend(), a.editor
	msg := ""
	switch {
	case k.Code == KeyEsc:
		if s := ed.openSelect(); s != nil {
			s.open = false
		} else {
			a.editor = nil
		}
		return
	case isCtrl(k, 's'):
		a.saveEditor()
		return
	case isCtrl(k, 'r'):
		if in := ed.input(b, "key"); in != nil {
			in.password = !in.password
		}
	case k.Code == KeyTab || k.Code == KeyBackTab:
		n := len(ed.widgets) + 1
		if k.Code == KeyBackTab || k.Shift {
			ed.setFocus((ed.focus + n - 1) % n)
		} else {
			ed.setFocus((ed.focus + 1) % n)
		}
	default:
		ed.followFocus = true
		if ed.focus == 0 {
			before := ed.quick.Text()
			ed.quick.Key(k)
			if ed.quick.Text() != before {
				msg = ed.quickChanged(b)
			}
		} else {
			switch w := ed.widgets[ed.focus-1]; {
			case w.input != nil:
				w.input.Key(k)
			case w.area != nil:
				w.area.Key(k)
			default:
				w.sel.Key(k)
			}
		}
	}
	if msg != "" {
		a.notify("", msg, SevInfo, 2)
	}
}

func (a *App) saveEditor() {
	ed := a.editor
	p, err := ed.values(a.backend())
	if err != nil {
		a.error(err.Error())
		return
	}
	a.editor = nil
	a.pending = nil
	name := p.Name()
	i, err := a.store.Upsert(a.tool, p, ed.original)
	if err != nil {
		a.error(fmt.Sprintf("写入失败: %v", err))
		return
	}
	a.refresh(i)
	a.info("已保存 " + name)
}

func (a *App) Paste(text string) {
	a.lastInput = time.Now()
	ed := a.editor
	if ed == nil {
		return
	}
	ed.followFocus = true
	if ed.focus == 0 {
		ed.quick.Insert(text)
		if msg := ed.quickChanged(a.backend()); msg != "" {
			a.notify("", msg, SevInfo, 2)
		}
		return
	}
	switch w := ed.widgets[ed.focus-1]; {
	case w.input != nil:
		w.input.Insert(text)
	case w.area != nil:
		w.area.Insert(text)
	}
}

// Scroll handles the mouse wheel.
func (a *App) Scroll(up bool) {
	if ed := a.editor; ed != nil {
		if up {
			ed.scroll = sub(ed.scroll, 1)
		} else {
			ed.scroll++
		}
		return
	}
	if up {
		a.Key(Key{Code: KeyUp})
	} else {
		a.Key(Key{Code: KeyDown})
	}
}

// Click handles a left button press.
func (a *App) Click(x, y int) {
	a.lastInput = time.Now()
	for _, t := range a.toastHits {
		if x >= t.x && x < t.x+t.w && y >= t.y && y < t.y+t.h {
			a.toasts = slices.Delete(a.toasts, t.id, t.id+1)
			return
		}
	}
	if a.editor != nil {
		a.editorClick(x, y)
		return
	}
	switch {
	case y == 1:
		if x < 13 {
			a.setTab(ToolClaude)
		} else if x < 20 {
			a.setTab(ToolCodex)
		}
	case y+1 == a.h:
		for _, f := range a.footerHits {
			if x >= f.a && x < f.b {
				a.action(f.key)
				return
			}
		}
	case y >= 4:
		if i := a.top + y - 4; i < len(a.backend().Providers) {
			a.cursor = i
			a.switchTo(i)
		}
	}
}

func (a *App) editorClick(x, y int) {
	ed := a.editor
	g := ed.geom(a.w, a.h)
	rows := ed.rows()
	// an open drop-down takes the click first
	if s := ed.openSelect(); s != nil {
		top := g.y + 1 + sub(rows[ed.focus][0], ed.scroll) + 1
		ox, ow := g.cx+10, sub(g.cw, 10)
		if x >= ox && x < ox+ow && y > top && y < top+s.OverlayRows()-1 {
			s.Pick(y - top - 1)
		} else {
			s.open = false
		}
		return
	}
	if x < g.cx || x >= g.cx+g.cw || y < g.y+1 || y >= g.y+1+g.vh {
		return
	}
	cy := y - g.y - 1 + ed.scroll
	i := slices.IndexFunc(rows, func(r [2]int) bool { return cy >= r[0] && cy < r[0]+r[1] })
	if i < 0 {
		return
	}
	ed.setFocus(i)
	ed.followFocus = false
	dy := cy - rows[i][0]
	if i == 0 {
		ed.quick.Click(sub(x-g.cx, 1), dy)
		return
	}
	dx := sub(x, g.cx+11)
	switch w := ed.widgets[i-1]; {
	case w.input != nil:
		w.input.Click(dx)
	case w.area != nil:
		w.area.Click(dx, dy)
	default:
		w.sel.Open()
	}
}

// Tick drops expired toasts; returns how long until something on screen changes.
func (a *App) Tick() time.Duration {
	now := time.Now()
	a.toasts = slices.DeleteFunc(a.toasts, func(t toast) bool { return !t.until.After(now) })
	next := time.Second
	for _, t := range a.toasts {
		next = min(next, t.until.Sub(now))
	}
	if a.editor != nil {
		ms := now.Sub(a.lastInput).Milliseconds() % 500
		next = min(next, time.Duration(500-ms)*time.Millisecond)
	}
	return next
}

// ---------------------------------------------------------------- render

func (a *App) Render(w, h int) *Canvas {
	a.w, a.h = w, h
	c := NewCanvas(w, h)
	if w <= 0 || h <= 0 {
		return c
	}
	a.renderHeader(c)
	a.renderTabs(c)
	a.renderTable(c)
	a.renderFooter(c)
	blink := a.steadyCursor || (time.Since(a.lastInput).Milliseconds()/500)%2 == 0
	if a.editor != nil {
		for i := range c.cells {
			c.cells[i].fg, c.cells[i].bg = backdrop(c.cells[i].fg), backdrop(c.cells[i].bg)
		}
		a.editor.render(c, blink)
	}
	a.renderToasts(c)
	return c
}

func (a *App) renderHeader(c *Canvas) {
	c.fill(0, 0, c.w, 1, cHeader)
	s := st(cFG, cHeader)
	c.text(1, 0, "⭘", s, c.w)
	title := "Claude Provider"
	x := 8 + sub(c.w, 18+width(title))/2
	c.text(x, 0, title, s, max(sub(c.w, 10), x))
}

func (a *App) renderTabs(c *Canvas) {
	c.fill(0, 1, c.w, 2, cScreen)
	x, from, to := 0, 0, 0
	for _, t := range allTools {
		label := a.store.Get(t).Label()
		lw := width(label)
		fg := cTabDim
		if t == a.tool {
			fg = cFG
			from, to = x+1, x+1+lw
		}
		c.text(x+1, 1, label, st(fg, cScreen), c.w)
		x += lw + 2
	}
	for xx := 0; xx < c.w; xx++ {
		c.glyph(xx, 2, '━', st(cRule, cScreen))
	}
	for xx := from; xx < to; xx++ {
		c.glyph(xx, 2, '━', st(cBlue, cScreen))
	}
	if from > 0 {
		c.glyph(from-1, 2, '╸', st(cRule, cScreen))
	}
	c.glyph(to, 2, '╺', st(cRule, cScreen))
}

func (a *App) renderTable(c *Canvas) {
	w, h := c.w, c.h
	if h < 5 {
		return
	}
	providers := a.backend().Providers
	visible := h - 5 // data rows below the header
	c.fill(0, 3, w, h-4, cRowOdd)

	longest := func(get func(*Provider) string, limit, floor int) int {
		for _, p := range providers {
			floor = max(floor, min(width(get(p)), limit))
		}
		return floor
	}
	showKey := w >= 80
	showModel := w >= 110 && slices.ContainsFunc(providers, func(p *Provider) bool { return p.Model() != "" })
	nameCap := 14
	if w >= 80 {
		nameCap = 24
	}
	nameW := longest((*Provider).Name, nameCap, 4)
	keyW, modelW := 12, longest((*Provider).Model, 18, 5)
	numW := len(strconv.Itoa(len(providers)))
	type col struct {
		label string
		w     int
	}
	fixed, nCols := 1+numW+nameW, 4
	if showKey {
		fixed, nCols = fixed+keyW, nCols+1
	}
	if showModel {
		fixed, nCols = fixed+modelW, nCols+1
	}
	urlW := max(sub(w, fixed+2*nCols+2), 8)
	cols := []col{{" ", 1}, {"#", numW}, {"名称", nameW}, {"Base URL", urlW}}
	if showKey {
		cols = append(cols, col{"Key", keyW})
	}
	if showModel {
		cols = append(cols, col{"Model", modelW})
	}

	// header
	c.fill(0, 3, w, 1, cTableHead)
	x := 0
	for _, col := range cols {
		c.text(x+1, 3, truncate(col.label, col.w), st(cFG, cTableHead).Bold(), w)
		x += col.w + 2
	}

	// keep the cursor row on screen
	if a.cursor < a.top {
		a.top = a.cursor
	} else if visible > 0 && a.cursor >= a.top+visible {
		a.top = a.cursor + 1 - visible
	}
	a.top = min(a.top, sub(len(providers), visible))

	for r := 0; r < visible && a.top+r < len(providers); r++ {
		i := a.top + r
		p := providers[i]
		y := 4 + r
		isCursor := i == a.cursor
		fg, bg := cFG, cRowOdd
		if isCursor {
			fg, bg = cCursorFG, cBlue
		} else if i%2 == 0 {
			bg = cRowEven
		}
		c.fill(0, y, w, 1, bg)
		base := Style{fg: fg, bg: bg, hasBg: true, bold: isCursor}
		faint := base
		faint.fg = dim(fg, bg)
		active := a.active == i
		type cellText struct {
			text  string
			style Style
		}
		mark := cellText{"", base}
		if active {
			mark = cellText{"●", base.Bold()}
			if !isCursor {
				mark.style.fg = cGreen
			}
		}
		name := cellText{p.Name(), base}
		name.style.bold = base.bold || active
		url := cellText{p.BaseURL(), base}
		if p.BaseURL() == "" {
			url = cellText{loginLabel(p), faint}
		}
		cells := []cellText{mark, {strconv.Itoa(i + 1), base}, name, url}
		if showKey {
			cells = append(cells, cellText{mask(p.Key()), base})
		}
		if showModel {
			if p.Model() == "" {
				cells = append(cells, cellText{"-", faint})
			} else {
				cells = append(cells, cellText{p.Model(), base})
			}
		}
		x := 0
		for j, cell := range cells {
			c.text(x+1, y, truncate(cell.text, cols[j].w), cell.style, w)
			x += cols[j].w + 2
		}
	}
	if len(providers)+1 > h-4 && w >= 2 {
		scrollbar(c, w-2, 3, h-4, len(providers)+1, h-4, a.top)
	}
}

// loginLabel describes a provider without an endpoint of its own.
func loginLabel(p *Provider) string {
	if p.Dict("extra_env").Len() > 0 {
		return "(官方登录 + 环境变量)"
	}
	return "(官方登录)"
}

func (a *App) renderFooter(c *Canvas) {
	y := c.h - 1
	c.fill(0, y, c.w, 1, cHeader)
	a.footerHits = a.footerHits[:0]
	x := 0
	for _, f := range footerKeys {
		start := x
		x = c.text(x, y, " "+string(f.key)+" ", st(cKey, cHeader).Bold(), c.w)
		x = c.text(x, y, f.desc+" ", st(cFG, cHeader), c.w)
		a.footerHits = append(a.footerHits, struct {
			a, b int
			key  rune
		}{start, x, f.key})
	}
}

func toastBar(sev Sev) Rgb {
	switch sev {
	case SevWarn:
		return cToastWarn
	case SevErr:
		return cToastErr
	}
	return cToastInfo
}

func (a *App) renderToasts(c *Canvas) {
	a.toastHits = a.toastHits[:0]
	tw := min(60, sub(c.w, 2)/2)
	if tw < 4 || c.h < 4 {
		return
	}
	x := c.w - 2 - tw
	bottom := c.h - 1 // exclusive
	for id := len(a.toasts) - 1; id >= 0; id-- {
		t := a.toasts[id]
		textW := tw - 3
		type line struct {
			text  string
			style Style
		}
		var lines []line
		if t.title != "" {
			color := cToastTitle
			if t.sev != SevInfo {
				color = toastBar(t.sev)
			}
			lines = append(lines, line{truncate(t.title, textW), st(color, cToast).Bold()})
		}
		chars := []rune(t.msg)
		for _, r := range wrap(chars, textW) {
			lines = append(lines, line{strings.TrimRightFunc(string(chars[r[0]:r[1]]), unicode.IsSpace), st(cFG, cToast)})
		}
		th := len(lines) + 2
		if bottom < th {
			break
		}
		top := bottom - th
		c.fill(x, top, tw, th, cToast)
		for yy := top; yy < bottom; yy++ {
			c.glyph(x, yy, '▌', st(toastBar(t.sev), cToast))
		}
		for i, l := range lines {
			c.text(x+2, top+1+i, l.text, l.style, x+tw-1)
		}
		a.toastHits = append(a.toastHits, hitBox{x, top, tw, th, id})
		if top == 0 {
			break
		}
		bottom = top - 1
	}
}
