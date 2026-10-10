//! Input / TextArea / Select, modelled on the Textual widgets they replace.
use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

use super::canvas::*;
use super::text::{cw, width, wrap};

/// The character a key press types, if it is a plain printable key.
pub fn printable(k: &KeyEvent) -> Option<char> {
    match k.code {
        KeyCode::Char(c)
            if !k
                .modifiers
                .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER) =>
        {
            Some(c)
        }
        _ => None,
    }
}

fn ctrl(k: &KeyEvent, c: char) -> bool {
    k.code == KeyCode::Char(c) && k.modifiers.contains(KeyModifiers::CONTROL)
}

fn word_left(s: &[char], mut i: usize) -> usize {
    while i > 0 && s[i - 1].is_whitespace() {
        i -= 1;
    }
    while i > 0 && !s[i - 1].is_whitespace() {
        i -= 1;
    }
    i
}

fn word_right(s: &[char], mut i: usize) -> usize {
    while i < s.len() && s[i].is_whitespace() {
        i += 1;
    }
    while i < s.len() && !s[i].is_whitespace() {
        i += 1;
    }
    i
}

// ------------------------------------------------------------------ Input
pub struct Input {
    pub chars: Vec<char>,
    pub pos: usize,
    anchor: Option<usize>,
    pub password: bool,
    placeholder: String,
    scroll: usize,
}

impl Input {
    pub fn new(value: &str, password: bool, placeholder: &str) -> Input {
        let chars: Vec<char> = value.chars().collect();
        Input {
            pos: chars.len(),
            chars,
            anchor: None,
            password,
            placeholder: placeholder.into(),
            scroll: 0,
        }
    }
    pub fn value(&self) -> String {
        self.chars.iter().collect()
    }
    pub fn set_value(&mut self, v: &str) {
        self.chars = v.chars().collect();
        self.pos = self.chars.len();
        self.anchor = None;
    }
    /// Focusing an input selects its whole value, like Textual.
    pub fn focus(&mut self) {
        self.pos = self.chars.len();
        self.anchor = (!self.chars.is_empty()).then_some(0);
    }
    pub fn blur(&mut self) {
        self.anchor = None;
    }
    fn selection(&self) -> Option<(usize, usize)> {
        let a = self.anchor?;
        (a != self.pos).then(|| (a.min(self.pos), a.max(self.pos)))
    }
    fn delete(&mut self, a: usize, b: usize) {
        self.chars.drain(a..b);
        self.pos = a;
        self.anchor = None;
    }
    fn delete_selection(&mut self) -> bool {
        match self.selection() {
            Some((a, b)) => {
                self.delete(a, b);
                true
            }
            None => false,
        }
    }
    /// Typed or pasted text (only the first line of a paste).
    pub fn insert(&mut self, s: &str) {
        let line = s.split(['\r', '\n']).next().unwrap_or("");
        self.delete_selection();
        self.anchor = None;
        for c in line.chars().filter(|c| !c.is_control()) {
            self.chars.insert(self.pos, c);
            self.pos += 1;
        }
    }
    fn go(&mut self, to: usize, select: bool) {
        if select {
            self.anchor.get_or_insert(self.pos);
        } else {
            self.anchor = None;
        }
        self.pos = to.min(self.chars.len());
    }

    pub fn key(&mut self, k: &KeyEvent) -> bool {
        let shift = k.modifiers.contains(KeyModifiers::SHIFT);
        let word = k.modifiers.contains(KeyModifiers::CONTROL);
        if let Some(c) = printable(k) {
            self.insert(&c.to_string());
            return true;
        }
        let len = self.chars.len();
        match k.code {
            KeyCode::Left if word => self.go(word_left(&self.chars, self.pos), shift),
            KeyCode::Right if word => self.go(word_right(&self.chars, self.pos), shift),
            KeyCode::Left => match self.selection() {
                Some((a, _)) if !shift => self.go(a, false),
                _ => self.go(self.pos.saturating_sub(1), shift),
            },
            KeyCode::Right => match self.selection() {
                Some((_, b)) if !shift => self.go(b, false),
                _ => self.go(self.pos + 1, shift),
            },
            KeyCode::Home => self.go(0, shift),
            KeyCode::End => self.go(len, shift),
            _ if ctrl(k, 'a') => self.go(0, false),
            _ if ctrl(k, 'e') => self.go(len, false),
            KeyCode::Backspace => {
                if !self.delete_selection() && self.pos > 0 {
                    self.delete(self.pos - 1, self.pos);
                }
            }
            KeyCode::Delete => {
                if !self.delete_selection() && self.pos < len {
                    self.delete(self.pos, self.pos + 1);
                }
            }
            _ if ctrl(k, 'd') => {
                if !self.delete_selection() && self.pos < len {
                    self.delete(self.pos, self.pos + 1);
                }
            }
            _ if ctrl(k, 'w') => {
                if !self.delete_selection() {
                    self.delete(word_left(&self.chars, self.pos), self.pos);
                }
            }
            _ if ctrl(k, 'u') => self.delete(0, self.pos),
            _ if ctrl(k, 'k') => {
                let p = self.pos;
                self.delete(p, len);
            }
            KeyCode::Enter => {}
            _ => return false,
        }
        true
    }

    fn shown(&self) -> Vec<char> {
        if self.password {
            vec!['•'; self.chars.len()]
        } else {
            self.chars.clone()
        }
    }

    /// Place the cursor at a click `dx` cells into the text.
    pub fn click(&mut self, dx: usize) {
        let shown = self.shown();
        let target = self.scroll + dx;
        let mut at = 0;
        let mut i = 0;
        while i < shown.len() && at + cw(shown[i]) <= target {
            at += cw(shown[i]);
            i += 1;
        }
        self.go(i, false);
    }

    pub fn render(
        &mut self,
        c: &mut Canvas,
        x: usize,
        y: usize,
        w: usize,
        focused: bool,
        blink: bool,
    ) {
        let bg = if focused { FOCUS } else { BOOST };
        c.fill(x, y, w, 1, bg);
        let (tx, end) = (x + 1, x + w);
        let window = w.saturating_sub(2);
        let cursor = focused && blink;
        if self.chars.is_empty() {
            self.scroll = 0;
            let ph = if focused {
                PLACEHOLDER_FOCUS
            } else {
                PLACEHOLDER
            };
            c.text(tx, y, &self.placeholder, st(ph, bg), end);
            if cursor {
                c.restyle(tx, y, |cell| {
                    cell.fg = CURSOR_TEXT;
                    cell.bg = CURSOR;
                });
            }
            return;
        }
        let shown = self.shown();
        let cells_before = width(&shown[..self.pos].iter().collect::<String>());
        let total = width(&shown.iter().collect::<String>());
        let off = cells_before + usize::from(self.pos == shown.len());
        if off < self.scroll {
            self.scroll = off.saturating_sub(usize::from(self.pos == shown.len()));
        } else if off > self.scroll + window {
            self.scroll = off - window;
        }
        self.scroll = self.scroll.min((total + 1).saturating_sub(window));
        let sel = if focused { self.selection() } else { None };
        let (mut at, mut px) = (0, tx);
        for (i, &ch) in shown.iter().enumerate() {
            let n = cw(ch);
            if at < self.scroll {
                at += n;
                continue;
            }
            let mut style = st(FG, bg);
            if sel.is_some_and(|(a, b)| a <= i && i < b) {
                style.bg = Some(SELECTION);
            }
            if cursor && i == self.pos {
                style = st(CURSOR_TEXT, CURSOR);
            }
            px = c.text(px, y, &ch.to_string(), style, end);
            if px >= end {
                return;
            }
        }
        if cursor && self.pos == shown.len() {
            c.glyph(px, y, ' ', st(CURSOR_TEXT, CURSOR));
        }
    }
}

// --------------------------------------------------------------- TextArea
pub struct TextArea {
    pub lines: Vec<Vec<char>>,
    row: usize,
    col: usize,
    scroll: usize,
    placeholder: String,
    quick: bool,
    wrap_w: usize,
}

/// One screen line: a slice `start..end` of document line `row`.
#[derive(Clone, Copy)]
struct Vis {
    row: usize,
    start: usize,
    end: usize,
}

impl TextArea {
    pub fn new(text: &str, placeholder: &str, quick: bool) -> TextArea {
        let mut t = TextArea {
            lines: vec![vec![]],
            row: 0,
            col: 0,
            scroll: 0,
            placeholder: placeholder.into(),
            quick,
            wrap_w: 60,
        };
        t.set_text(text);
        t.row = 0;
        t.col = 0;
        t
    }
    pub fn text(&self) -> String {
        self.lines
            .iter()
            .map(|l| l.iter().collect::<String>())
            .collect::<Vec<_>>()
            .join("\n")
    }
    /// Replace the content and put the cursor at the end.
    pub fn set_text(&mut self, text: &str) {
        let text = text.replace("\r\n", "\n").replace('\r', "\n");
        self.lines = text.split('\n').map(|l| l.chars().collect()).collect();
        self.row = self.lines.len() - 1;
        self.col = self.lines[self.row].len();
    }
    pub fn insert(&mut self, s: &str) {
        let s = s.replace("\r\n", "\n").replace('\r', "\n");
        let rest = self.lines[self.row].split_off(self.col);
        let mut parts = s.split('\n');
        let first = parts.next().unwrap_or("");
        self.lines[self.row].extend(first.chars());
        self.col = self.lines[self.row].len();
        for part in parts {
            self.row += 1;
            self.lines.insert(self.row, part.chars().collect());
            self.col = self.lines[self.row].len();
        }
        self.lines[self.row].extend(rest);
    }

    fn layout(&self, w: usize) -> Vec<Vis> {
        let mut out = vec![];
        for (row, line) in self.lines.iter().enumerate() {
            let s: String = line.iter().collect();
            for (start, end) in wrap(&s, w) {
                out.push(Vis { row, start, end });
            }
        }
        out
    }
    fn cursor_vis(&self, vis: &[Vis]) -> usize {
        let mut best = 0;
        for (i, v) in vis.iter().enumerate() {
            if v.row == self.row && v.start <= self.col {
                best = i;
            }
        }
        best
    }
    fn vertical(&mut self, delta: isize) {
        let vis = self.layout(self.wrap_w);
        let cur = self.cursor_vis(&vis);
        let v = vis[cur];
        let dx = width(
            &self.lines[v.row][v.start..self.col]
                .iter()
                .collect::<String>(),
        );
        let target = cur as isize + delta;
        if target < 0 {
            self.col = 0;
            self.row = 0;
            return;
        }
        if target as usize >= vis.len() {
            self.row = self.lines.len() - 1;
            self.col = self.lines[self.row].len();
            return;
        }
        let t = vis[target as usize];
        let last = t.end == self.lines[t.row].len();
        let mut col = t.start;
        let mut at = 0;
        while col < t.end && at + cw(self.lines[t.row][col]) <= dx && (last || col + 1 < t.end) {
            at += cw(self.lines[t.row][col]);
            col += 1;
        }
        self.row = t.row;
        self.col = col;
    }

    pub fn key(&mut self, k: &KeyEvent) -> bool {
        if let Some(c) = printable(k) {
            self.insert(&c.to_string());
            return true;
        }
        let len = self.lines[self.row].len();
        match k.code {
            KeyCode::Enter => self.insert("\n"),
            KeyCode::Left => {
                if self.col > 0 {
                    self.col -= 1;
                } else if self.row > 0 {
                    self.row -= 1;
                    self.col = self.lines[self.row].len();
                }
            }
            KeyCode::Right => {
                if self.col < len {
                    self.col += 1;
                } else if self.row + 1 < self.lines.len() {
                    self.row += 1;
                    self.col = 0;
                }
            }
            KeyCode::Up => self.vertical(-1),
            KeyCode::Down => self.vertical(1),
            KeyCode::PageUp => self.vertical(-3),
            KeyCode::PageDown => self.vertical(3),
            KeyCode::Home => self.col = 0,
            KeyCode::End => self.col = len,
            _ if ctrl(k, 'a') => self.col = 0,
            _ if ctrl(k, 'e') => self.col = len,
            KeyCode::Backspace => {
                if self.col > 0 {
                    self.col -= 1;
                    self.lines[self.row].remove(self.col);
                } else if self.row > 0 {
                    let line = self.lines.remove(self.row);
                    self.row -= 1;
                    self.col = self.lines[self.row].len();
                    self.lines[self.row].extend(line);
                }
            }
            KeyCode::Delete => self.delete_right(),
            _ if ctrl(k, 'd') => self.delete_right(),
            _ if ctrl(k, 'w') => {
                let to = word_left(&self.lines[self.row], self.col);
                self.lines[self.row].drain(to..self.col);
                self.col = to;
            }
            _ if ctrl(k, 'u') => {
                self.lines[self.row].drain(..self.col);
                self.col = 0;
            }
            _ if ctrl(k, 'k') => {
                if self.col == len && self.row + 1 < self.lines.len() {
                    self.delete_right();
                } else {
                    self.lines[self.row].truncate(self.col);
                }
            }
            _ => return false,
        }
        true
    }
    fn delete_right(&mut self) {
        if self.col < self.lines[self.row].len() {
            self.lines[self.row].remove(self.col);
        } else if self.row + 1 < self.lines.len() {
            let line = self.lines.remove(self.row + 1);
            self.lines[self.row].extend(line);
        }
    }

    pub fn click(&mut self, dx: usize, dy: usize) {
        let vis = self.layout(self.wrap_w);
        let Some(&v) = vis.get(self.scroll + dy).or(vis.last()) else {
            return;
        };
        let (mut col, mut at) = (v.start, 0);
        while col < v.end && at + cw(self.lines[v.row][col]) <= dx {
            at += cw(self.lines[v.row][col]);
            col += 1;
        }
        self.row = v.row;
        self.col = col;
    }

    pub fn is_empty(&self) -> bool {
        self.lines.len() == 1 && self.lines[0].is_empty()
    }

    #[allow(clippy::too_many_arguments)]
    pub fn render(
        &mut self,
        c: &mut Canvas,
        x: usize,
        y: usize,
        w: usize,
        h: usize,
        focused: bool,
        blink: bool,
    ) {
        let (bg, line_bg, ph) = match (self.quick, focused) {
            (true, false) => (QUICK, QUICK_LINE, QUICK_PH),
            (true, true) => (QUICK_FOCUS, QUICK_LINE_FOCUS, QUICK_PH_FOCUS),
            (false, false) => (BOOST, AREA_LINE, AREA_PH),
            (false, true) => (AREA_FOCUS, AREA_LINE_FOCUS, AREA_PH),
        };
        c.fill(x, y, w, h, bg);
        let tx = x + 1;
        let mut tw = w.saturating_sub(2);
        let cursor = focused && blink;
        if self.is_empty() {
            self.wrap_w = tw;
            self.scroll = 0;
            let lines = wrap(&self.placeholder, tw);
            let chars: Vec<char> = self.placeholder.chars().collect();
            for (i, &(a, b)) in lines.iter().take(h).enumerate() {
                let s: String = chars[a..b].iter().collect();
                c.text(tx, y + i, s.trim_end(), st(ph, bg), tx + tw);
            }
            if cursor {
                c.restyle(tx, y, |cell| {
                    cell.fg = CURSOR_TEXT;
                    cell.bg = CURSOR;
                });
            }
            return;
        }
        let mut vis = self.layout(tw);
        let bar = vis.len() > h;
        if bar {
            tw = tw.saturating_sub(2);
            vis = self.layout(tw);
        }
        self.wrap_w = tw;
        let cur = self.cursor_vis(&vis);
        if cur < self.scroll {
            self.scroll = cur;
        } else if cur >= self.scroll + h {
            self.scroll = cur + 1 - h;
        }
        self.scroll = self.scroll.min(vis.len().saturating_sub(h));
        for (i, v) in vis.iter().skip(self.scroll).take(h).enumerate() {
            let yy = y + i;
            let lbg = if self.scroll + i == cur { line_bg } else { bg };
            c.fill(tx, yy, tw, 1, lbg);
            let mut px = tx;
            for col in v.start..v.end {
                let ch = self.lines[v.row][col];
                let style = if cursor && v.row == self.row && col == self.col {
                    st(CURSOR_TEXT, CURSOR)
                } else {
                    st(FG, lbg)
                };
                px = c.text(px, yy, &ch.to_string(), style, tx + tw);
            }
            if cursor
                && self.scroll + i == cur
                && self.col == v.end
                && self.col == self.lines[v.row].len()
            {
                c.glyph(px, yy, ' ', st(CURSOR_TEXT, CURSOR));
            }
        }
        if bar {
            scrollbar(c, tx + tw, y, h, vis.len(), h, self.scroll);
        }
    }
}

// ----------------------------------------------------------------- Select
pub struct Select {
    pub options: &'static [&'static str],
    pub value: usize,
    pub open: bool,
    highlight: usize,
}

impl Select {
    pub fn new(options: &'static [&'static str], value: &str) -> Select {
        let value = options.iter().position(|o| *o == value).unwrap_or(0);
        Select {
            options,
            value,
            open: false,
            highlight: value,
        }
    }
    pub fn value(&self) -> &'static str {
        self.options.get(self.value).copied().unwrap_or("")
    }
    pub fn set(&mut self, v: &str) {
        if let Some(i) = self.options.iter().position(|o| *o == v) {
            self.value = i;
        }
    }
    pub fn open(&mut self) {
        self.open = true;
        self.highlight = self.value;
    }
    pub fn pick(&mut self, i: usize) {
        if i < self.options.len() {
            self.value = i;
        }
        self.open = false;
    }

    pub fn key(&mut self, k: &KeyEvent) -> bool {
        let n = self.options.len();
        if !self.open {
            match k.code {
                KeyCode::Enter | KeyCode::Down | KeyCode::Up | KeyCode::Char(' ') => self.open(),
                _ => return false,
            }
            return true;
        }
        match k.code {
            KeyCode::Up => self.highlight = self.highlight.saturating_sub(1),
            KeyCode::Down => self.highlight = (self.highlight + 1).min(n.saturating_sub(1)),
            KeyCode::Home | KeyCode::PageUp => self.highlight = 0,
            KeyCode::End | KeyCode::PageDown => self.highlight = n.saturating_sub(1),
            KeyCode::Enter | KeyCode::Char(' ') => self.pick(self.highlight),
            KeyCode::Esc => self.open = false,
            _ => {}
        }
        true
    }

    pub fn render(&self, c: &mut Canvas, x: usize, y: usize, w: usize, focused: bool) {
        let focused = focused && !self.open;
        let bg = if focused { FOCUS } else { BOOST };
        c.fill(x, y, w, 1, bg);
        let arrow_x = (x + w).saturating_sub(2);
        c.text(
            x + 1,
            y,
            self.value(),
            st(FG, bg),
            arrow_x.saturating_sub(1),
        );
        let arrow = if focused { ARROW_FOCUS } else { ARROW };
        c.glyph(arrow_x, y, if self.open { '▲' } else { '▼' }, st(arrow, bg));
    }

    /// The drop-down list, drawn on top of everything below the select.
    pub fn render_overlay(&self, c: &mut Canvas, x: usize, y: usize, w: usize) {
        if !self.open || w < 4 {
            return;
        }
        let rows = self.options.len() + 2;
        for i in 0..rows {
            let yy = y + i;
            c.glyph(x, yy, '▊', st(SURFACE, BLUE));
            c.glyph(x + w - 1, yy, '▎', st(BLUE, SURFACE));
            let edge = match i {
                0 => Some('▔'),
                _ if i == rows - 1 => Some('▁'),
                _ => None,
            };
            if let Some(ch) = edge {
                for xx in x + 1..x + w - 1 {
                    c.glyph(xx, yy, ch, st(BLUE, OPTIONS));
                }
                continue;
            }
            let o = i - 1;
            c.fill(x + 1, yy, w - 2, 1, OPTIONS);
            let style = if o == self.highlight {
                st(CURSOR_FG, BLUE).bold()
            } else {
                st(FG, OPTIONS)
            };
            let label = format!(" {}", self.options[o]);
            c.fill(x + 2, yy, w - 4, 1, style.bg.unwrap());
            c.text(x + 2, yy, &label, style, x + w - 2);
        }
    }

    pub fn overlay_rows(&self) -> usize {
        self.options.len() + 2
    }
}
