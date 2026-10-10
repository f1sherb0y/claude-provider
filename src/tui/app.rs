//! Application state, drawing and input handling (a port of the Textual ProviderApp).
use std::time::{Duration, Instant};

use crossterm::event::{KeyCode, KeyEvent, KeyModifiers, MouseButton, MouseEvent, MouseEventKind};

use super::canvas::*;
use super::text::{truncate, width, wrap};
use super::widgets::{Input, Select, TextArea, printable};
use crate::backend::{Backend, Kind, Provider, Tool, Val};
use crate::common::{Dict, Info, info_str, mask, py_strip, read_clipboard, splitlines};
use crate::store::Store;

#[derive(Clone, Copy, PartialEq)]
pub enum Sev {
    Info,
    Warn,
    Err,
}

struct Toast {
    title: String,
    msg: String,
    sev: Sev,
    until: Instant,
}

const FOOTER: [(char, &str); 6] = [
    ('a', "新增"),
    ('v', "剪贴板新增"),
    ('e', "编辑"),
    ('c', "复制"),
    ('d', "删除"),
    ('q', "退出"),
];
const SUBTITLE: &str = " Ctrl+S 保存 · Esc 取消 · Ctrl+R 显示 Key ";

fn dict_text(d: &Dict) -> String {
    d.iter()
        .map(|(k, v)| format!("{k}={v}"))
        .collect::<Vec<_>>()
        .join("\n")
}

fn parse_env_lines(text: &str) -> Dict {
    let mut env = Dict::new();
    for line in splitlines(text) {
        let line = py_strip(line);
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Some((k, v)) = line.split_once('=') {
            env.insert(py_strip(k).to_string(), py_strip(v).to_string());
        }
    }
    env
}

// ------------------------------------------------------------------ editor
enum Widget {
    Input(Input),
    Area(TextArea),
    Select(Select),
}

impl Widget {
    fn height(&self) -> usize {
        if matches!(self, Widget::Area(_)) {
            3
        } else {
            1
        }
    }
}

struct Editor {
    title: String,
    original: Option<usize>,
    base: Provider,
    quick: TextArea,
    widgets: Vec<Widget>,
    /// 0 = the quick-paste box, i + 1 = field i
    focus: usize,
    scroll: usize,
    follow_focus: bool,
    /// the name we filled in from a paste (still replaceable by a later paste)
    auto_name: Option<String>,
    labels: Vec<&'static str>,
}

struct Geom {
    x: usize,
    y: usize,
    dw: usize,
    dh: usize,
    cx: usize,
    cw: usize,
    vh: usize,
    content_h: usize,
    bar: bool,
}

impl Editor {
    fn new(backend: &Backend, p: Provider, title: String, original: Option<usize>) -> Editor {
        let widgets = backend
            .fields()
            .iter()
            .map(|f| match f.kind {
                Kind::Select => Widget::Select(Select::new(f.options, p.s(f.name))),
                Kind::Dict => Widget::Area(TextArea::new(
                    &dict_text(&p.dict(f.name)),
                    f.placeholder,
                    false,
                )),
                _ => Widget::Input(Input::new(
                    p.s(f.name),
                    f.kind == Kind::Password,
                    f.placeholder,
                )),
            })
            .collect();
        let placeholder = format!("⚡ 粘贴 {}，自动识别", backend.paste_hint());
        let mut ed = Editor {
            title,
            original,
            base: p,
            quick: TextArea::new("", &placeholder, true),
            widgets,
            focus: 0,
            scroll: 0,
            follow_focus: true,
            auto_name: None,
            labels: backend.fields().iter().map(|f| f.label).collect(),
        };
        let name = backend
            .fields()
            .iter()
            .position(|f| f.name == "name")
            .map_or(0, |i| i + 1);
        ed.set_focus(if original.is_none() { 0 } else { name });
        ed
    }

    fn field_index(&self, backend: &Backend, name: &str) -> Option<usize> {
        backend.fields().iter().position(|f| f.name == name)
    }

    fn input(&mut self, backend: &Backend, name: &str) -> Option<&mut Input> {
        let i = self.field_index(backend, name)?;
        match &mut self.widgets[i] {
            Widget::Input(w) => Some(w),
            _ => None,
        }
    }

    fn set_focus(&mut self, to: usize) {
        if self.focus > 0 {
            match &mut self.widgets[self.focus - 1] {
                Widget::Input(w) => w.blur(),
                Widget::Select(s) => s.open = false,
                Widget::Area(_) => {}
            }
        }
        self.focus = to;
        if to > 0
            && let Widget::Input(w) = &mut self.widgets[to - 1]
        {
            w.focus();
        }
        self.follow_focus = true;
    }

    fn open_select(&mut self) -> Option<&mut Select> {
        match self.widgets.get_mut(self.focus.checked_sub(1)?)? {
            Widget::Select(s) if s.open => Some(s),
            _ => None,
        }
    }

    /// Fill the form from a parsed paste; returns the "recognised" message.
    fn fill(&mut self, backend: &Backend, info: &Info) -> Option<String> {
        for (f, w) in backend.fields().iter().zip(self.widgets.iter_mut()) {
            let Some(value) = info.get(f.name) else {
                continue;
            };
            match (value, w) {
                (Val::D(d), Widget::Area(t)) if !d.is_empty() => t.set_text(&dict_text(d)),
                (Val::S(s), Widget::Select(sel)) if !s.is_empty() => sel.set(s),
                (Val::S(s), Widget::Input(inp)) if !s.is_empty() => {
                    if f.name == "name" {
                        let cur = inp.value();
                        if !cur.is_empty() && self.auto_name.as_deref() != Some(cur.as_str()) {
                            continue;
                        }
                        let name = backend.unique_name(s, self.original);
                        inp.set_value(&name);
                        self.auto_name = Some(name);
                    } else {
                        inp.set_value(s);
                    }
                }
                _ => {}
            }
        }
        (!info.is_empty()).then(|| {
            format!(
                "已识别：{}",
                info.keys().cloned().collect::<Vec<_>>().join(", ")
            )
        })
    }

    fn quick_changed(&mut self, backend: &Backend) -> Option<String> {
        let text = self.quick.text();
        if py_strip(&text).is_empty() {
            return None;
        }
        let info = backend.parse(&text);
        self.fill(backend, &info)
    }

    fn values(&self, backend: &Backend) -> Result<Provider, String> {
        let mut p = self.base.clone();
        for (f, w) in backend.fields().iter().zip(&self.widgets) {
            match w {
                Widget::Area(t) => {
                    p.0.insert(f.name.to_string(), Val::D(parse_env_lines(&t.text())));
                }
                Widget::Select(s) => p.set(f.name, s.value()),
                Widget::Input(i) => p.set(f.name, py_strip(&i.value())),
            }
        }
        if p.name().is_empty() && !p.base_url().is_empty() {
            let name = info_str(&backend.parse(p.base_url()), "name");
            p.set("name", name);
        }
        let name = p.name().to_string();
        if name.is_empty() {
            return Err("名称不能为空".into());
        }
        if backend
            .providers
            .iter()
            .enumerate()
            .any(|(i, q)| q.name() == name && Some(i) != self.original)
        {
            return Err(format!("名称 {name} 已存在"));
        }
        Ok(p)
    }

    /// (content row, height) of widget `i` (0 = quick box).
    fn rows(&self) -> Vec<(usize, usize)> {
        let mut out = vec![(0, 2)];
        let mut y = 3;
        for w in &self.widgets {
            out.push((y, w.height()));
            y += w.height();
        }
        out
    }

    fn geom(&self, w: usize, h: usize) -> Geom {
        let dw = (w * 9 / 10).clamp(30, 84).min(w);
        let content_h = 3 + self.widgets.iter().map(Widget::height).sum::<usize>();
        let dh = (content_h + 2).min(h);
        let (x, y) = ((w - dw) / 2, (h - dh) / 2);
        let vh = dh.saturating_sub(2);
        let bar = content_h > vh;
        let cw = dw.saturating_sub(if bar { 6 } else { 4 });
        Geom {
            x,
            y,
            dw,
            dh,
            cx: x + 2,
            cw,
            vh,
            content_h,
            bar,
        }
    }

    fn render(&mut self, c: &mut Canvas, blink: bool) {
        let g = self.geom(c.w, c.h);
        if g.dw < 6 || g.dh < 3 {
            return;
        }
        // frame
        c.fill(g.x, g.y, g.dw, g.dh, SURFACE);
        let border = st(ACCENT, SURFACE);
        let (right, bottom) = (g.x + g.dw - 1, g.y + g.dh - 1);
        for xx in g.x + 1..right {
            c.glyph(xx, g.y, '─', border);
            c.glyph(xx, bottom, '─', border);
        }
        for yy in g.y + 1..bottom {
            c.glyph(g.x, yy, '│', border);
            c.glyph(right, yy, '│', border);
        }
        c.glyph(g.x, g.y, '╭', border);
        c.glyph(right, g.y, '╮', border);
        c.glyph(g.x, bottom, '╰', border);
        c.glyph(right, bottom, '╯', border);
        let title = truncate(&format!(" {} ", self.title), g.dw.saturating_sub(4));
        c.text(g.x + 2, g.y, &title, border.bold(), right);
        let sub = truncate(SUBTITLE, g.dw.saturating_sub(4));
        c.text(
            right - 1 - width(&sub),
            bottom,
            &sub,
            st(MUTED, SURFACE),
            right - 1,
        );

        // content, drawn off-screen then clipped into the viewport
        let rows = self.rows();
        if self.follow_focus {
            let (ry, rh) = rows[self.focus];
            if ry < self.scroll {
                self.scroll = ry;
            } else if ry + rh > self.scroll + g.vh {
                self.scroll = ry + rh - g.vh;
            }
            self.follow_focus = false;
        }
        self.scroll = self.scroll.min(g.content_h.saturating_sub(g.vh));
        let mut oc = Canvas::new(g.cw, g.content_h);
        oc.fill(0, 0, g.cw, g.content_h, SURFACE);
        self.quick
            .render(&mut oc, 0, 0, g.cw, 2, self.focus == 0, blink);
        for (i, w) in self.widgets.iter_mut().enumerate() {
            let (ry, rh) = rows[i + 1];
            let focused = self.focus == i + 1;
            let open = matches!(w, Widget::Select(s) if s.open);
            let label = if focused && !open {
                st(ACCENT, SURFACE).bold()
            } else {
                st(MUTED, SURFACE)
            };
            oc.text(0, ry, self.labels[i], label, 10);
            let (wx, ww) = (10, g.cw.saturating_sub(10));
            match w {
                Widget::Input(inp) => inp.render(&mut oc, wx, ry, ww, focused, blink),
                Widget::Area(t) => t.render(&mut oc, wx, ry, ww, rh, focused, blink),
                Widget::Select(s) => s.render(&mut oc, wx, ry, ww, focused),
            }
        }
        c.blit(&oc, self.scroll, g.cx, g.y + 1, g.vh);
        if g.bar {
            scrollbar(
                c,
                g.cx + g.cw,
                g.y + 1,
                g.vh,
                g.content_h,
                g.vh,
                self.scroll,
            );
        }
        if self.focus > 0
            && let Widget::Select(s) = &self.widgets[self.focus - 1]
        {
            let (ry, _) = rows[self.focus];
            if s.open && ry >= self.scroll {
                s.render_overlay(
                    c,
                    g.cx + 10,
                    g.y + 1 + ry - self.scroll + 1,
                    g.cw.saturating_sub(10),
                );
            }
        }
    }
}

// ------------------------------------------------------------------- app
enum Outcome {
    Stay,
    Cancel,
    Save,
}

pub struct App {
    store: Store,
    tool: Tool,
    cursor: usize,
    top: usize,
    active: Option<usize>,
    editor: Option<Editor>,
    toasts: Vec<Toast>,
    pending_delete: Option<(usize, String, Instant)>,
    pub quit: bool,
    w: usize,
    h: usize,
    last_input: Instant,
    /// headless renders never blink the cursor off
    pub steady_cursor: bool,
    footer_hits: Vec<(usize, usize, char)>,
    toast_hits: Vec<(usize, usize, usize, usize, usize)>,
}

impl App {
    pub fn new(store: Store, tool: Option<Tool>) -> App {
        let tool = tool
            .or_else(|| Tool::from_id(&store.last_tab))
            .unwrap_or(Tool::Claude);
        let mut app = App {
            store,
            tool,
            cursor: 0,
            top: 0,
            active: None,
            editor: None,
            toasts: vec![],
            pending_delete: None,
            quit: false,
            w: 80,
            h: 24,
            last_input: Instant::now(),
            steady_cursor: false,
            footer_hits: vec![],
            toast_hits: vec![],
        };
        app.refresh(None);
        app.cursor = app.active.unwrap_or(0);
        app
    }

    fn backend(&self) -> &Backend {
        self.store.get(self.tool)
    }

    pub fn notify(&mut self, title: &str, msg: &str, sev: Sev, secs: f64) {
        let until = Instant::now() + Duration::from_secs_f64(secs);
        // each paste recognition supersedes the previous one
        if msg.starts_with("已识别：") {
            self.toasts
                .retain(|t| !t.msg.starts_with("已识别：") || t.msg == msg);
        }
        // a repeat of a visible toast just refreshes it instead of stacking up
        if let Some(t) = self
            .toasts
            .iter_mut()
            .find(|t| t.msg == msg && t.title == title)
        {
            t.until = until;
            return;
        }
        self.toasts.push(Toast {
            title: title.into(),
            msg: msg.into(),
            sev,
            until,
        });
    }

    fn info(&mut self, msg: &str) {
        self.notify("", msg, Sev::Info, 5.0);
    }
    fn error(&mut self, msg: &str) {
        self.notify("", msg, Sev::Err, 5.0);
    }

    /// Re-read the active provider from disk; optionally move the cursor to `focus`.
    fn refresh(&mut self, focus: Option<usize>) {
        match self.backend().active() {
            Ok(a) => self.active = a,
            Err(e) => {
                self.active = None;
                let target = self.backend().target().display().to_string();
                self.error(&format!("读取 {target} 失败: {e}"));
            }
        }
        if let Some(f) = focus {
            self.cursor = f;
        }
        let n = self.backend().providers.len();
        self.cursor = self.cursor.min(n.saturating_sub(1));
    }

    fn selected(&self) -> Option<usize> {
        (self.cursor < self.backend().providers.len()).then_some(self.cursor)
    }

    fn switch_to(&mut self, idx: usize) {
        if let Err(e) = self.store.apply(self.tool, idx) {
            self.error(&format!("写入失败: {e}"));
            return;
        }
        self.refresh(Some(idx));
        let msg = format!(
            "{} → {}，新开会话生效",
            self.backend().label(),
            self.backend().providers[idx].name()
        );
        self.notify("✔ 切换成功", &msg, Sev::Info, 5.0);
    }

    fn set_tab(&mut self, tool: Tool) {
        if tool == self.tool {
            return;
        }
        self.tool = tool;
        self.pending_delete = None;
        self.store.last_tab = tool.id().to_string();
        if let Err(e) = self.store.save() {
            self.error(&format!("写入失败: {e}"));
        }
        self.cursor = 0;
        self.top = 0;
        self.refresh(None);
        if let Some(a) = self.active {
            self.cursor = a;
        }
    }

    fn cycle_tab(&mut self, delta: isize) {
        let i = Tool::ALL.iter().position(|t| *t == self.tool).unwrap_or(0) as isize;
        let n = Tool::ALL.len() as isize;
        self.set_tab(Tool::ALL[(i + delta).rem_euclid(n) as usize]);
    }

    fn open_editor(&mut self, p: Provider, title: String, original: Option<usize>, paste: &str) {
        let backend = self.store.get(self.tool);
        let title = format!("{title} · {}", backend.label());
        let mut ed = Editor::new(backend, p, title, original);
        let mut msg = None;
        if !paste.is_empty() {
            ed.quick.set_text(paste);
            msg = ed.quick_changed(backend);
        }
        self.editor = Some(ed);
        if let Some(m) = msg {
            self.notify("", &m, Sev::Info, 2.0);
        }
    }

    fn action(&mut self, c: char) {
        let n = self.backend().providers.len();
        match c {
            'a' => {
                let p = self.backend().blank();
                self.open_editor(p, "新增 Provider".into(), None, "");
            }
            'v' => {
                let text = read_clipboard();
                if info_str(&self.backend().parse(&text), "base_url").is_empty() {
                    self.notify("", "剪贴板里没有识别到 Base URL", Sev::Warn, 5.0);
                } else {
                    let p = self.backend().blank();
                    self.open_editor(p, "从剪贴板新增".into(), None, &text);
                }
            }
            'e' => {
                if let Some(i) = self.selected() {
                    let p = self.backend().providers[i].clone();
                    let title = format!("编辑 {}", p.name());
                    self.open_editor(p, title, Some(i), "");
                }
            }
            'c' => {
                if let Some(i) = self.selected() {
                    let mut p = self.backend().providers[i].clone();
                    let title = format!("复制 {}", p.name());
                    p.set("name", self.backend().unique_name(p.name(), None));
                    if p.0.contains_key("id") {
                        p.set("id", "");
                    }
                    self.open_editor(p, title, None, "");
                }
            }
            'd' => self.delete(),
            'J' | 'K' => {
                if let Some(i) = self.selected() {
                    self.pending_delete = None;
                    let delta = if c == 'J' { 1 } else { -1 };
                    match self.store.move_by(self.tool, i, delta) {
                        Ok(j) => self.refresh(Some(j)),
                        Err(e) => self.error(&format!("写入失败: {e}")),
                    }
                }
            }
            'q' => self.quit = true,
            '1'..='9' => {
                let i = c as usize - '1' as usize;
                if i < n {
                    self.switch_to(i);
                }
            }
            _ => {}
        }
    }

    fn delete(&mut self) {
        let Some(i) = self.selected() else { return };
        let name = self.backend().providers[i].name().to_string();
        let armed = matches!(&self.pending_delete,
            Some((j, n, t)) if *j == i && *n == name && t.elapsed() < Duration::from_secs(3));
        if armed {
            self.pending_delete = None;
            if let Err(e) = self.store.remove(self.tool, i) {
                self.error(&format!("写入失败: {e}"));
                return;
            }
            self.refresh(None);
            self.info(&format!("已删除 {name}"));
        } else {
            self.pending_delete = Some((i, name.clone(), Instant::now()));
            self.notify("", &format!("再按一次 d 删除 {name}"), Sev::Warn, 3.0);
        }
    }

    // ---------------------------------------------------------------- input
    pub fn key(&mut self, k: KeyEvent) {
        self.last_input = Instant::now();
        let ctrl = k.modifiers.contains(KeyModifiers::CONTROL);
        if ctrl && k.code == KeyCode::Char('q') {
            self.quit = true;
            return;
        }
        if self.editor.is_some() {
            self.editor_key(k);
            return;
        }
        if ctrl && k.code == KeyCode::Char('c') {
            self.quit = true;
            return;
        }
        let n = self.backend().providers.len();
        let page = self.h.saturating_sub(5).max(1);
        match k.code {
            KeyCode::Enter => {
                if let Some(i) = self.selected() {
                    self.switch_to(i);
                }
            }
            KeyCode::Left => self.cycle_tab(-1),
            KeyCode::Right => self.cycle_tab(1),
            KeyCode::Up => self.cursor = self.cursor.saturating_sub(1),
            KeyCode::Down => self.cursor = (self.cursor + 1).min(n.saturating_sub(1)),
            KeyCode::Home => self.cursor = 0,
            KeyCode::End => self.cursor = n.saturating_sub(1),
            KeyCode::PageUp => self.cursor = self.cursor.saturating_sub(page),
            KeyCode::PageDown => self.cursor = (self.cursor + page).min(n.saturating_sub(1)),
            _ => {
                if let Some(c) = printable(&k) {
                    self.action(c);
                }
            }
        }
    }

    fn editor_key(&mut self, k: KeyEvent) {
        let ctrl = k.modifiers.contains(KeyModifiers::CONTROL);
        let backend = self.store.get(self.tool);
        let ed = self.editor.as_mut().unwrap();
        let mut outcome = Outcome::Stay;
        let mut toast = None;
        match k.code {
            KeyCode::Esc => {
                if let Some(s) = ed.open_select() {
                    s.open = false;
                } else {
                    outcome = Outcome::Cancel;
                }
            }
            KeyCode::Char('s') if ctrl => outcome = Outcome::Save,
            KeyCode::Char('r') if ctrl => {
                if let Some(i) = ed.input(backend, "key") {
                    i.password = !i.password;
                }
            }
            KeyCode::Tab | KeyCode::BackTab => {
                let n = ed.widgets.len() + 1;
                let back = k.code == KeyCode::BackTab || k.modifiers.contains(KeyModifiers::SHIFT);
                ed.set_focus(if back {
                    (ed.focus + n - 1) % n
                } else {
                    (ed.focus + 1) % n
                });
            }
            _ => {
                ed.follow_focus = true;
                if ed.focus == 0 {
                    let before = ed.quick.text();
                    ed.quick.key(&k);
                    if ed.quick.text() != before {
                        toast = ed.quick_changed(backend);
                    }
                } else {
                    match &mut ed.widgets[ed.focus - 1] {
                        Widget::Input(w) => {
                            w.key(&k);
                        }
                        Widget::Area(t) => {
                            t.key(&k);
                        }
                        Widget::Select(s) => {
                            s.key(&k);
                        }
                    }
                }
            }
        }
        if let Some(m) = toast {
            self.notify("", &m, Sev::Info, 2.0);
        }
        self.finish_editor(outcome);
    }

    fn finish_editor(&mut self, outcome: Outcome) {
        match outcome {
            Outcome::Stay => {}
            Outcome::Cancel => self.editor = None,
            Outcome::Save => {
                let ed = self.editor.as_ref().unwrap();
                match ed.values(self.backend()) {
                    Err(e) => self.error(&e),
                    Ok(p) => {
                        let original = ed.original;
                        self.editor = None;
                        self.pending_delete = None;
                        let name = p.name().to_string();
                        match self.store.upsert(self.tool, p, original) {
                            Ok(i) => {
                                self.refresh(Some(i));
                                self.info(&format!("已保存 {name}"));
                            }
                            Err(e) => self.error(&format!("写入失败: {e}")),
                        }
                    }
                }
            }
        }
    }

    pub fn paste(&mut self, text: &str) {
        self.last_input = Instant::now();
        let backend = self.store.get(self.tool);
        let Some(ed) = self.editor.as_mut() else {
            return;
        };
        ed.follow_focus = true;
        let mut toast = None;
        if ed.focus == 0 {
            ed.quick.insert(text);
            toast = ed.quick_changed(backend);
        } else {
            match &mut ed.widgets[ed.focus - 1] {
                Widget::Input(w) => w.insert(text),
                Widget::Area(t) => t.insert(text),
                Widget::Select(_) => {}
            }
        }
        if let Some(m) = toast {
            self.notify("", &m, Sev::Info, 2.0);
        }
    }

    pub fn mouse(&mut self, m: MouseEvent) {
        let (x, y) = (m.column as usize, m.row as usize);
        match m.kind {
            MouseEventKind::Down(MouseButton::Left) => {}
            MouseEventKind::ScrollUp | MouseEventKind::ScrollDown => {
                let up = m.kind == MouseEventKind::ScrollUp;
                if let Some(ed) = self.editor.as_mut() {
                    ed.scroll = if up {
                        ed.scroll.saturating_sub(1)
                    } else {
                        ed.scroll + 1
                    };
                } else {
                    let code = if up { KeyCode::Up } else { KeyCode::Down };
                    self.key(KeyEvent::new(code, KeyModifiers::NONE));
                }
                return;
            }
            _ => return,
        }
        self.last_input = Instant::now();
        if let Some(&(.., id)) = self
            .toast_hits
            .iter()
            .find(|&&(tx, ty, tw, th, _)| (tx..tx + tw).contains(&x) && (ty..ty + th).contains(&y))
        {
            self.toasts.remove(id);
            return;
        }
        if self.editor.is_some() {
            self.editor_click(x, y);
            return;
        }
        if y == 1 {
            let (claude, codex) = (0..13, 13..20);
            if claude.contains(&x) {
                self.set_tab(Tool::Claude);
            } else if codex.contains(&x) {
                self.set_tab(Tool::Codex);
            }
        } else if y + 1 == self.h {
            if let Some(&(.., c)) = self
                .footer_hits
                .iter()
                .find(|&&(a, b, _)| (a..b).contains(&x))
            {
                self.action(c);
            }
        } else if y >= 4 {
            let i = self.top + y - 4;
            if i < self.backend().providers.len() {
                self.cursor = i;
                self.switch_to(i);
            }
        }
    }

    fn editor_click(&mut self, x: usize, y: usize) {
        let (w, h) = (self.w, self.h);
        let ed = self.editor.as_mut().unwrap();
        let g = ed.geom(w, h);
        let rows = ed.rows();
        // an open drop-down takes the click first
        if ed.focus > 0
            && let Widget::Select(s) = &mut ed.widgets[ed.focus - 1]
            && s.open
        {
            let top = g.y + 1 + rows[ed.focus].0.saturating_sub(ed.scroll) + 1;
            let (ox, ow) = (g.cx + 10, g.cw.saturating_sub(10));
            if (ox..ox + ow).contains(&x) && y > top && y < top + s.overlay_rows() - 1 {
                s.pick(y - top - 1);
            } else {
                s.open = false;
            }
            return;
        }
        if !(g.cx..g.cx + g.cw).contains(&x) || !(g.y + 1..g.y + 1 + g.vh).contains(&y) {
            return;
        }
        let cy = y - g.y - 1 + ed.scroll;
        let Some(i) = rows
            .iter()
            .position(|&(ry, rh)| (ry..ry + rh).contains(&cy))
        else {
            return;
        };
        ed.set_focus(i);
        ed.follow_focus = false;
        let dy = cy - rows[i].0;
        if i == 0 {
            ed.quick.click((x - g.cx).saturating_sub(1), dy);
            return;
        }
        let dx = x.saturating_sub(g.cx + 11);
        match &mut ed.widgets[i - 1] {
            Widget::Input(inp) => inp.click(dx),
            Widget::Area(t) => t.click(dx, dy),
            Widget::Select(s) => s.open(),
        }
    }

    /// Drop expired toasts; returns how long until something on screen changes.
    pub fn tick(&mut self) -> Duration {
        let now = Instant::now();
        self.toasts.retain(|t| t.until > now);
        let mut next = Duration::from_secs(1);
        for t in &self.toasts {
            next = next.min(t.until - now);
        }
        if self.editor.is_some() {
            let ms = (now - self.last_input).as_millis() as u64 % 500;
            next = next.min(Duration::from_millis(500 - ms));
        }
        next
    }

    // ---------------------------------------------------------------- render
    pub fn render(&mut self, w: usize, h: usize) -> Canvas {
        self.w = w;
        self.h = h;
        let mut c = Canvas::new(w, h);
        if w == 0 || h == 0 {
            return c;
        }
        self.render_header(&mut c);
        self.render_tabs(&mut c);
        self.render_table(&mut c);
        self.render_footer(&mut c);
        let blink =
            self.steady_cursor || (self.last_input.elapsed().as_millis() / 500).is_multiple_of(2);
        if let Some(ed) = self.editor.as_mut() {
            c.map_all(|cell| {
                cell.fg = backdrop(cell.fg);
                cell.bg = backdrop(cell.bg);
            });
            ed.render(&mut c, blink);
        }
        self.render_toasts(&mut c);
        c
    }

    fn render_header(&self, c: &mut Canvas) {
        c.fill(0, 0, c.w, 1, HEADER);
        let s = st(FG, HEADER);
        c.text(1, 0, "⭘", s, c.w);
        let title = "Claude Provider";
        let x = 8 + c.w.saturating_sub(18 + width(title)) / 2;
        c.text(x, 0, title, s, c.w.saturating_sub(10).max(x));
    }

    fn render_tabs(&self, c: &mut Canvas) {
        c.fill(0, 1, c.w, 2, SCREEN);
        let mut x = 0;
        let mut span = (0, 0);
        for t in Tool::ALL {
            let label = self.store.get(t).label();
            let lw = width(label);
            let fg = if t == self.tool { FG } else { TAB_DIM };
            c.text(x + 1, 1, label, st(fg, SCREEN), c.w);
            if t == self.tool {
                span = (x + 1, x + 1 + lw);
            }
            x += lw + 2;
        }
        for xx in 0..c.w {
            c.glyph(xx, 2, '━', st(RULE, SCREEN));
        }
        for xx in span.0..span.1 {
            c.glyph(xx, 2, '━', st(BLUE, SCREEN));
        }
        if span.0 > 0 {
            c.glyph(span.0 - 1, 2, '╸', st(RULE, SCREEN));
        }
        c.glyph(span.1, 2, '╺', st(RULE, SCREEN));
    }

    fn render_table(&mut self, c: &mut Canvas) {
        let (w, h) = (c.w, c.h);
        if h < 5 {
            return;
        }
        let backend = self.store.get(self.tool);
        let providers = &backend.providers;
        let visible = h - 5; // data rows below the header
        c.fill(0, 3, w, h - 4, ROW_ODD);

        let longest = |vals: &mut dyn Iterator<Item = &str>, cap: usize, floor: usize| {
            vals.map(|v| width(v).min(cap)).fold(floor, usize::max)
        };
        let show_key = w >= 80;
        let show_model = w >= 110 && providers.iter().any(|p| !p.model().is_empty());
        let name_w = longest(
            &mut providers.iter().map(|p| p.name()),
            if w >= 80 { 24 } else { 14 },
            4,
        );
        let (key_w, model_w) = (12, longest(&mut providers.iter().map(|p| p.model()), 18, 5));
        let num_w = providers.len().to_string().len().max(1);
        let fixed = 1
            + num_w
            + name_w
            + if show_key { key_w } else { 0 }
            + if show_model { model_w } else { 0 };
        let n_cols = 4 + usize::from(show_key) + usize::from(show_model);
        let url_w = w.saturating_sub(fixed + 2 * n_cols + 2).max(8);
        let mut cols = vec![
            (" ", 1),
            ("#", num_w),
            ("名称", name_w),
            ("Base URL", url_w),
        ];
        if show_key {
            cols.push(("Key", key_w));
        }
        if show_model {
            cols.push(("Model", model_w));
        }

        // header
        c.fill(0, 3, w, 1, TABLE_HEAD);
        let mut x = 0;
        for (label, cw) in &cols {
            c.text(
                x + 1,
                3,
                &truncate(label, *cw),
                st(FG, TABLE_HEAD).bold(),
                w,
            );
            x += cw + 2;
        }

        // keep the cursor row on screen
        if self.cursor < self.top {
            self.top = self.cursor;
        } else if visible > 0 && self.cursor >= self.top + visible {
            self.top = self.cursor + 1 - visible;
        }
        self.top = self.top.min(providers.len().saturating_sub(visible));

        for (r, (i, p)) in providers
            .iter()
            .enumerate()
            .skip(self.top)
            .take(visible)
            .enumerate()
        {
            let y = 4 + r;
            let is_cursor = i == self.cursor;
            let (fg, bg) = if is_cursor {
                (CURSOR_FG, BLUE)
            } else {
                (FG, if i % 2 == 0 { ROW_EVEN } else { ROW_ODD })
            };
            c.fill(0, y, w, 1, bg);
            let base = Style {
                fg,
                bg: Some(bg),
                bold: is_cursor,
            };
            let faint = Style {
                fg: dim(fg, bg),
                ..base
            };
            let active = self.active == Some(i);
            let mut cells: Vec<(String, Style)> = vec![
                (
                    "●".into(),
                    if active {
                        Style {
                            fg: if is_cursor { fg } else { GREEN },
                            bold: true,
                            ..base
                        }
                    } else {
                        base
                    },
                ),
                ((i + 1).to_string(), base),
                (
                    p.name().into(),
                    Style {
                        bold: base.bold || active,
                        ..base
                    },
                ),
                if p.base_url().is_empty() {
                    ("(官方登录)".into(), faint)
                } else {
                    (p.base_url().into(), base)
                },
            ];
            if !active {
                cells[0].0.clear();
            }
            if show_key {
                cells.push((mask(p.key()), base));
            }
            if show_model {
                cells.push(if p.model().is_empty() {
                    ("-".into(), faint)
                } else {
                    (p.model().into(), base)
                });
            }
            let mut x = 0;
            for ((text, style), (_, cw)) in cells.iter().zip(&cols) {
                c.text(x + 1, y, &truncate(text, *cw), *style, w);
                x += cw + 2;
            }
        }
        if providers.len() + 1 > h - 4 && w >= 2 {
            scrollbar(c, w - 2, 3, h - 4, providers.len() + 1, h - 4, self.top);
        }
    }

    fn render_footer(&mut self, c: &mut Canvas) {
        let y = c.h - 1;
        c.fill(0, y, c.w, 1, HEADER);
        self.footer_hits.clear();
        let mut x = 0;
        for (k, desc) in FOOTER {
            let start = x;
            x = c.text(x, y, &format!(" {k} "), st(KEY, HEADER).bold(), c.w);
            x = c.text(x, y, &format!("{desc} "), st(FG, HEADER), c.w);
            self.footer_hits.push((start, x, k));
        }
    }

    fn render_toasts(&mut self, c: &mut Canvas) {
        self.toast_hits.clear();
        let tw = 60.min(c.w.saturating_sub(2) / 2);
        if tw < 4 || c.h < 4 {
            return;
        }
        let x = c.w - 2 - tw;
        let mut bottom = c.h - 1; // exclusive
        for (id, t) in self.toasts.iter().enumerate().rev() {
            let text_w = tw - 3;
            let mut lines: Vec<(String, Style)> = vec![];
            if !t.title.is_empty() {
                let color = if t.sev == Sev::Info {
                    TOAST_TITLE
                } else {
                    t_bar(t.sev)
                };
                lines.push((truncate(&t.title, text_w), st(color, TOAST).bold()));
            }
            let chars: Vec<char> = t.msg.chars().collect();
            for (a, b) in wrap(&t.msg, text_w) {
                lines.push((
                    chars[a..b].iter().collect::<String>().trim_end().into(),
                    st(FG, TOAST),
                ));
            }
            let th = lines.len() + 2;
            if bottom < th {
                break;
            }
            let top = bottom - th;
            c.fill(x, top, tw, th, TOAST);
            for yy in top..bottom {
                c.glyph(x, yy, '▌', st(t_bar(t.sev), TOAST));
            }
            for (i, (line, style)) in lines.iter().enumerate() {
                c.text(x + 2, top + 1 + i, line, *style, x + tw - 1);
            }
            self.toast_hits.push((x, top, tw, th, id));
            if top == 0 {
                break;
            }
            bottom = top - 1;
        }
    }
}

fn t_bar(sev: Sev) -> Rgb {
    match sev {
        Sev::Info => TOAST_INFO,
        Sev::Warn => TOAST_WARN,
        Sev::Err => TOAST_ERR,
    }
}
