//! Full-screen TUI: ←/→ switches between Claude Code and Codex.
mod app;
mod canvas;
mod text;
mod widgets;

use std::io::{Write, stdout};
use std::time::Duration;

#[cfg(not(windows))]
use crossterm::event::{DisableBracketedPaste, EnableBracketedPaste};
use crossterm::event::{
    self, DisableMouseCapture, EnableMouseCapture, Event, KeyCode, KeyEvent, KeyEventKind,
    KeyModifiers,
};
use crossterm::terminal::{self, EnterAlternateScreen, LeaveAlternateScreen};
use crossterm::{cursor, execute};
use ratatui::Terminal;
use ratatui::backend::CrosstermBackend;
use ratatui::style::{Color, Modifier};

use crate::backend::Tool;
use crate::store::Store;
pub use app::App;
pub use canvas::Canvas;

fn restore_terminal() {
    let _ = terminal::disable_raw_mode();
    #[cfg(not(windows))]
    let _ = execute!(stdout(), DisableBracketedPaste);
    let _ = execute!(
        stdout(),
        DisableMouseCapture,
        LeaveAlternateScreen,
        cursor::Show
    );
}

pub fn run(store: Store, tool: Option<Tool>) -> Result<(), String> {
    let mut app = App::new(store, tool);
    terminal::enable_raw_mode().map_err(|e| e.to_string())?;
    let default_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        restore_terminal();
        default_hook(info);
    }));
    let result = event_loop(&mut app);
    restore_terminal();
    result.map_err(|e| e.to_string())
}

fn event_loop(app: &mut App) -> std::io::Result<()> {
    let mut out = stdout();
    execute!(out, EnterAlternateScreen, EnableMouseCapture, cursor::Hide)?;
    // crossterm can't read bracketed paste from the Windows console (see Burst)
    #[cfg(not(windows))]
    execute!(out, EnableBracketedPaste)?;
    let mut term = Terminal::new(CrosstermBackend::new(out))?;
    term.clear()?;
    loop {
        term.draw(|f| {
            let area = f.area();
            let canvas = app.render(area.width as usize, area.height as usize);
            let buf = f.buffer_mut();
            for y in 0..canvas.h {
                for x in 0..canvas.w {
                    let c = canvas.cell(x, y);
                    let cell = &mut buf[(x as u16, y as u16)];
                    let mut tmp = [0u8; 4];
                    let sym = if c.ch == '\0' {
                        " "
                    } else {
                        c.ch.encode_utf8(&mut tmp)
                    };
                    cell.set_symbol(sym);
                    cell.set_fg(Color::Rgb(c.fg.0, c.fg.1, c.fg.2));
                    cell.set_bg(Color::Rgb(c.bg.0, c.bg.1, c.bg.2));
                    cell.modifier = if c.bold {
                        Modifier::BOLD
                    } else {
                        Modifier::empty()
                    };
                }
            }
        })?;
        if app.quit {
            break;
        }
        let wait = app.tick().max(Duration::from_millis(10));
        if !event::poll(wait)? {
            continue;
        }
        // drain everything that is already queued before redrawing
        let mut burst = Burst::default();
        loop {
            let ev = event::read()?;
            if !burst.push(&ev) {
                burst.flush(app);
                match ev {
                    Event::Key(k) if k.kind != KeyEventKind::Release => app.key(k),
                    Event::Mouse(m) => app.mouse(m),
                    Event::Paste(s) => app.paste(&s),
                    _ => {}
                }
            }
            if app.quit || !event::poll(burst.linger())? {
                break;
            }
        }
        burst.flush(app);
        app.tick();
    }
    term.backend_mut().flush()
}

/// The Windows console has no bracketed paste: a terminal paste arrives as a burst of
/// key events (newlines as Enter). Text keys that arrive back to back are collected
/// and handed to `App::paste` as one string; a lone key is still a normal key press.
#[derive(Default)]
struct Burst {
    keys: Vec<KeyEvent>,
}

impl Burst {
    /// Swallow `ev` if it may be part of a paste.
    fn push(&mut self, ev: &Event) -> bool {
        if !cfg!(windows) {
            return false;
        }
        let Event::Key(k) = ev else { return false };
        if k.kind == KeyEventKind::Release {
            return !self.keys.is_empty();
        }
        let plain = !k
            .modifiers
            .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT);
        let texty = plain && matches!(k.code, KeyCode::Char(_) | KeyCode::Enter | KeyCode::Tab);
        if texty {
            self.keys.push(*k);
        }
        texty
    }

    /// How long to wait for the rest of a paste before handling what we have.
    fn linger(&self) -> Duration {
        if self.keys.is_empty() {
            Duration::ZERO
        } else {
            Duration::from_millis(8)
        }
    }

    fn flush(&mut self, app: &mut App) {
        let keys = std::mem::take(&mut self.keys);
        if keys.len() < 2 {
            keys.into_iter().for_each(|k| app.key(k));
            return;
        }
        let text: String = keys
            .iter()
            .map(|k| match k.code {
                KeyCode::Char(c) => c,
                KeyCode::Tab => '\t',
                _ => '\n',
            })
            .collect();
        app.paste(&text);
    }
}

// ---------------------------------------------------------------- headless
/// "ctrl+s", "shift+tab", "down", "J" ... -> a key event.
pub fn parse_key(spec: &str) -> Option<KeyEvent> {
    let mut mods = KeyModifiers::NONE;
    let mut parts: Vec<&str> = spec.split('+').collect();
    let name = if spec.ends_with("++") {
        "+"
    } else {
        parts.pop()?
    };
    for m in parts {
        mods |= match m {
            "ctrl" => KeyModifiers::CONTROL,
            "shift" => KeyModifiers::SHIFT,
            "alt" => KeyModifiers::ALT,
            _ => return None,
        };
    }
    let code = match name {
        "enter" => KeyCode::Enter,
        "escape" | "esc" => KeyCode::Esc,
        "tab" if mods.contains(KeyModifiers::SHIFT) => KeyCode::BackTab,
        "tab" => KeyCode::Tab,
        "backspace" => KeyCode::Backspace,
        "delete" => KeyCode::Delete,
        "up" => KeyCode::Up,
        "down" => KeyCode::Down,
        "left" => KeyCode::Left,
        "right" => KeyCode::Right,
        "home" => KeyCode::Home,
        "end" => KeyCode::End,
        "pageup" => KeyCode::PageUp,
        "pagedown" => KeyCode::PageDown,
        "space" => KeyCode::Char(' '),
        s if s.chars().count() == 1 => KeyCode::Char(s.chars().next()?),
        _ => return None,
    };
    Some(KeyEvent::new(code, mods))
}

/// Drive the app with scripted input (`text:…`, `paste:…`, `resize:WxH`, key names) and return the final screen.
pub fn headless(
    store: Store,
    tool: Option<Tool>,
    mut w: usize,
    mut h: usize,
    keys: &[String],
) -> Canvas {
    let mut app = App::new(store, tool);
    app.steady_cursor = true;
    app.render(w, h);
    for k in keys {
        if let Some(size) = k.strip_prefix("resize:") {
            if let Some((a, b)) = size.split_once('x') {
                w = a.parse().unwrap_or(w);
                h = b.parse().unwrap_or(h);
            }
        } else if let Some(s) = k.strip_prefix("paste:") {
            app.paste(s);
        } else if let Some(s) = k.strip_prefix("text:") {
            for c in s.chars() {
                app.key(KeyEvent::new(KeyCode::Char(c), KeyModifiers::NONE));
                app.render(w, h);
            }
        } else if let Some(ev) = parse_key(k) {
            app.key(ev);
        }
        app.render(w, h);
    }
    app.render(w, h)
}

/// The screen as JSON rows of {"c","fg","bg","b"} cells (wide-character tails have c = "").
pub fn dump_json(c: &Canvas) -> String {
    let hex = |c: canvas::Rgb| format!("#{:02x}{:02x}{:02x}", c.0, c.1, c.2);
    let rows: Vec<serde_json::Value> = (0..c.h)
        .map(|y| {
            (0..c.w)
                .map(|x| {
                    let cell = c.cell(x, y);
                    let ch = if cell.ch == '\0' {
                        String::new()
                    } else {
                        cell.ch.to_string()
                    };
                    let mut o =
                        serde_json::json!({"c": ch, "fg": hex(cell.fg), "bg": hex(cell.bg)});
                    if cell.bold {
                        o["b"] = 1.into();
                    }
                    o
                })
                .collect()
        })
        .collect();
    serde_json::Value::Array(rows).to_string()
}
