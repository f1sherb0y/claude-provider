// Full-screen TUI on tcell: ←/→ switches between Claude Code and Codex.
package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

func keyFromTcell(ev *tcell.EventKey) Key {
	mod := ev.Modifiers()
	k := Key{Ctrl: mod&tcell.ModCtrl != 0, Shift: mod&tcell.ModShift != 0, Alt: mod&tcell.ModAlt != 0}
	switch ev.Key() {
	case tcell.KeyRune:
		k.Code, k.Ch = KeyChar, ev.Rune()
	case tcell.KeyEnter:
		k.Code = KeyEnter
	case tcell.KeyEscape:
		k.Code = KeyEsc
	case tcell.KeyTab:
		k.Code = KeyTab
	case tcell.KeyBacktab:
		k.Code = KeyBackTab
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		k.Code = KeyBackspace
	case tcell.KeyDelete:
		k.Code = KeyDelete
	case tcell.KeyUp:
		k.Code = KeyUp
	case tcell.KeyDown:
		k.Code = KeyDown
	case tcell.KeyLeft:
		k.Code = KeyLeft
	case tcell.KeyRight:
		k.Code = KeyRight
	case tcell.KeyHome:
		k.Code = KeyHome
	case tcell.KeyEnd:
		k.Code = KeyEnd
	case tcell.KeyPgUp:
		k.Code = KeyPageUp
	case tcell.KeyPgDn:
		k.Code = KeyPageDown
	default:
		if key := ev.Key(); key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ {
			k.Code, k.Ch, k.Ctrl = KeyChar, 'a'+rune(key-tcell.KeyCtrlA), true
		}
	}
	return k
}

// input turns terminal events into App calls.
type input struct {
	app     *App
	screen  tcell.Screen
	pasting bool
	paste   strings.Builder
	// The classic Windows console has no bracketed paste: a paste arrives as a burst
	// of key events (newlines as Enter). Text keys that arrive back to back are
	// collected and handed to App.Paste as one string; a lone key is a normal key press.
	burst    []Key
	leftDown bool
}

func texty(k Key) (rune, bool) {
	switch {
	case k.Ctrl || k.Alt:
	case k.Code == KeyChar:
		return k.Ch, true
	case k.Code == KeyEnter:
		return '\n', true
	case k.Code == KeyTab:
		return '\t', true
	}
	return 0, false
}

func (in *input) flush() {
	keys := in.burst
	in.burst = nil
	if len(keys) < 2 {
		for _, k := range keys {
			in.app.Key(k)
		}
		return
	}
	var text strings.Builder
	for _, k := range keys {
		c, _ := texty(k)
		text.WriteRune(c)
	}
	in.app.Paste(text.String())
}

func (in *input) handle(ev tcell.Event) {
	switch ev := ev.(type) {
	case *tcell.EventResize:
		in.screen.Sync()
	case *tcell.EventPaste:
		in.flush()
		if ev.Start() {
			in.pasting = true
			in.paste.Reset()
		} else if in.pasting {
			in.pasting = false
			in.app.Paste(in.paste.String())
		}
	case *tcell.EventKey:
		k := keyFromTcell(ev)
		if in.pasting {
			if c, ok := texty(k); ok {
				in.paste.WriteRune(c)
			}
			return
		}
		if k.Code == KeyNone {
			return
		}
		if _, ok := texty(k); ok && runtime.GOOS == "windows" {
			in.burst = append(in.burst, k)
			return
		}
		in.flush()
		in.app.Key(k)
	case *tcell.EventMouse:
		in.flush()
		x, y := ev.Position()
		btn := ev.Buttons()
		if btn&tcell.WheelUp != 0 {
			in.app.Scroll(true)
		}
		if btn&tcell.WheelDown != 0 {
			in.app.Scroll(false)
		}
		left := btn&tcell.Button1 != 0
		if left && !in.leftDown {
			in.app.Click(x, y)
		}
		in.leftDown = left
	}
}

func draw(screen tcell.Screen, app *App) {
	w, h := screen.Size()
	c := app.Render(w, h)
	color := func(c Rgb) tcell.Color { return tcell.NewRGBColor(int32(c.r), int32(c.g), int32(c.b)) }
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			cell := c.cell(x, y)
			if cell.ch == 0 {
				continue // right half of the wide character before it
			}
			style := tcell.StyleDefault.Foreground(color(cell.fg)).Background(color(cell.bg)).Bold(cell.bold)
			screen.SetContent(x, y, cell.ch, nil, style)
		}
	}
	screen.Show()
}

func runTUI(store *Store, tool Tool, hasTool bool) (err error) {
	app := NewApp(store, tool, hasTool)
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	quit := make(chan struct{})
	defer func() {
		close(quit)
		screen.Fini()
		if r := recover(); r != nil {
			panic(r)
		}
	}()
	screen.EnableMouse(tcell.MouseButtonEvents)
	screen.EnablePaste()
	screen.HideCursor()
	events := make(chan tcell.Event, 256)
	go screen.ChannelEvents(events, quit)

	in := &input{app: app, screen: screen}
	for !app.quit {
		draw(screen, app)
		timer := time.NewTimer(max(app.Tick(), 10*time.Millisecond))
		select {
		case <-timer.C:
			continue
		case ev, ok := <-events:
			timer.Stop()
			// drain everything that is already queued before redrawing
			for ok && ev != nil {
				in.handle(ev)
				if app.quit {
					break
				}
				if len(in.burst) > 0 {
					// wait a moment for the rest of a paste
					select {
					case ev, ok = <-events:
					case <-time.After(8 * time.Millisecond):
						ev = nil
					}
				} else {
					select {
					case ev, ok = <-events:
					default:
						ev = nil
					}
				}
			}
			if !ok {
				return nil
			}
			in.flush()
			app.Tick()
		}
	}
	return nil
}

// ---------------------------------------------------------------- headless

// parseKey turns "ctrl+s", "shift+tab", "down", "J" ... into a key event.
func parseKey(spec string) (Key, bool) {
	var k Key
	parts := strings.Split(spec, "+")
	name := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	if strings.HasSuffix(spec, "++") || spec == "+" {
		name, mods = "+", parts[:max(len(parts)-2, 0)]
	}
	for _, m := range mods {
		switch m {
		case "ctrl":
			k.Ctrl = true
		case "shift":
			k.Shift = true
		case "alt":
			k.Alt = true
		default:
			return k, false
		}
	}
	codes := map[string]KeyCode{
		"enter": KeyEnter, "escape": KeyEsc, "esc": KeyEsc, "tab": KeyTab, "backspace": KeyBackspace,
		"delete": KeyDelete, "up": KeyUp, "down": KeyDown, "left": KeyLeft, "right": KeyRight,
		"home": KeyHome, "end": KeyEnd, "pageup": KeyPageUp, "pagedown": KeyPageDown,
	}
	switch code, ok := codes[name]; {
	case ok:
		k.Code = code
		if code == KeyTab && k.Shift {
			k.Code = KeyBackTab
		}
	case name == "space":
		k.Code, k.Ch = KeyChar, ' '
	case len([]rune(name)) == 1:
		k.Code, k.Ch = KeyChar, []rune(name)[0]
	default:
		return k, false
	}
	return k, true
}

// headless drives the app with scripted input (`text:…`, `paste:…`, `resize:WxH`,
// `click:X,Y`, key names) and returns the final screen.
func headless(store *Store, tool Tool, hasTool bool, w, h int, keys []string) *Canvas {
	app := NewApp(store, tool, hasTool)
	app.steadyCursor = true
	app.Render(w, h)
	pair := func(s, sep string) (int, int, bool) {
		a, b, ok := strings.Cut(s, sep)
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		return x, y, ok && err1 == nil && err2 == nil
	}
	for _, k := range keys {
		if s, ok := strings.CutPrefix(k, "resize:"); ok {
			if a, b, ok := pair(s, "x"); ok {
				w, h = a, b
			}
		} else if s, ok := strings.CutPrefix(k, "click:"); ok {
			if x, y, ok := pair(s, ","); ok {
				app.Click(x, y)
			}
		} else if s, ok := strings.CutPrefix(k, "paste:"); ok {
			app.Paste(s)
		} else if s, ok := strings.CutPrefix(k, "text:"); ok {
			for _, c := range s {
				app.Key(Key{Code: KeyChar, Ch: c})
				app.Render(w, h)
			}
		} else if ev, ok := parseKey(k); ok {
			app.Key(ev)
		}
		app.Render(w, h)
	}
	return app.Render(w, h)
}

// renderCommand is the hidden `ccp __render WxH [keys...]`: print the screen a scripted session ends on.
func renderCommand(args []string, codex, claude bool) error {
	w, h := 100, 30
	if len(args) > 0 {
		if a, b, ok := strings.Cut(args[0], "x"); ok {
			if ww, err := strconv.Atoi(a); err == nil {
				if hh, err := strconv.Atoi(b); err == nil {
					w, h, args = ww, hh, args[1:]
				}
			}
		}
	}
	store, err := LoadStore()
	if err != nil {
		return err
	}
	tool := ToolClaude
	if codex {
		tool = ToolCodex
	}
	fmt.Print(headless(store, tool, codex || claude, w, h, args).String())
	return nil
}
