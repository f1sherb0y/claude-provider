//! A plain cell grid we paint the whole UI into, plus the Textual-dark palette.
use super::text::cw;

pub type Rgb = (u8, u8, u8);

pub const fn hex(v: u32) -> Rgb {
    ((v >> 16) as u8, (v >> 8) as u8, v as u8)
}

// -- palette (Textual "textual-dark", resolved to the cells it renders)
pub const FG: Rgb = hex(0xe0e0e0);
pub const SCREEN: Rgb = hex(0x121212);
pub const HEADER: Rgb = hex(0x242f38);
pub const TAB_DIM: Rgb = hex(0x797979);
pub const RULE: Rgb = hex(0x262626);
pub const BLUE: Rgb = hex(0x0178d4);
pub const TABLE_HEAD: Rgb = hex(0x2d3740);
pub const ROW_EVEN: Rgb = hex(0x1c1c1c);
pub const ROW_ODD: Rgb = hex(0x272727);
pub const CURSOR_FG: Rgb = hex(0xddedf9);
pub const GREEN: Rgb = hex(0x98e024);
pub const KEY: Rgb = hex(0xffa62b);
pub const TOAST: Rgb = hex(0x343f49);
pub const TOAST_INFO: Rgb = hex(0x4ebf71);
pub const TOAST_TITLE: Rgb = hex(0x8ad4a1);
pub const TOAST_WARN: Rgb = hex(0xfea62b);
pub const TOAST_ERR: Rgb = hex(0xb93c5b);
pub const SURFACE: Rgb = hex(0x1e1e1e);
pub const ACCENT: Rgb = hex(0xfea62b);
pub const MUTED: Rgb = hex(0xa5a5a5);
pub const BOOST: Rgb = hex(0x262626);
pub const FOCUS: Rgb = hex(0x554023);
pub const SELECTION: Rgb = hex(0x485d72);
pub const CURSOR: Rgb = hex(0xe0e0e0);
pub const CURSOR_TEXT: Rgb = hex(0x121212);
pub const PLACEHOLDER: Rgb = hex(0x787878);
pub const PLACEHOLDER_FOCUS: Rgb = hex(0x958876);
pub const ARROW: Rgb = hex(0x838383);
pub const ARROW_FOCUS: Rgb = hex(0x9a9081);
pub const QUICK: Rgb = hex(0x233127);
pub const QUICK_FOCUS: Rgb = hex(0x2a4632);
pub const QUICK_LINE: Rgb = hex(0x2b392f);
pub const QUICK_LINE_FOCUS: Rgb = hex(0x324d3a);
pub const QUICK_PH: Rgb = hex(0x7b837d);
pub const QUICK_PH_FOCUS: Rgb = hex(0x7f9084);
pub const AREA_LINE: Rgb = hex(0x2e2e2e);
pub const AREA_FOCUS: Rgb = hex(0x564021);
pub const AREA_LINE_FOCUS: Rgb = hex(0x5c4729);
pub const AREA_PH: Rgb = hex(0x7c7c7c);
pub const OPTIONS: Rgb = hex(0x272727);
pub const BAR: Rgb = hex(0x003054);
pub const BAR_BACK: Rgb = hex(0x000000);

/// rich's "dim": the colour 66% of the way from the background to `fg`.
pub fn dim(fg: Rgb, bg: Rgb) -> Rgb {
    let f = |b: u8, c: u8| (b as f64 + (c as f64 - b as f64) * 0.66) as u8;
    (f(bg.0, fg.0), f(bg.1, fg.1), f(bg.2, fg.2))
}

/// The modal backdrop: every colour 60% of the way towards the screen background.
pub fn backdrop(c: Rgb) -> Rgb {
    let f = |c: u8, s: u8| (c as f64 + (s as f64 - c as f64) * 0.6) as u8;
    (f(c.0, SCREEN.0), f(c.1, SCREEN.1), f(c.2, SCREEN.2))
}

/// `'\0'` marks the right half of a wide character.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Cell {
    pub ch: char,
    pub fg: Rgb,
    pub bg: Rgb,
    pub bold: bool,
}

#[derive(Clone, Copy)]
pub struct Style {
    pub fg: Rgb,
    pub bg: Option<Rgb>,
    pub bold: bool,
}

pub const fn st(fg: Rgb, bg: Rgb) -> Style {
    Style {
        fg,
        bg: Some(bg),
        bold: false,
    }
}

impl Style {
    pub const fn bold(mut self) -> Style {
        self.bold = true;
        self
    }
}

pub struct Canvas {
    pub w: usize,
    pub h: usize,
    pub cells: Vec<Cell>,
}

impl Canvas {
    pub fn new(w: usize, h: usize) -> Canvas {
        let blank = Cell {
            ch: ' ',
            fg: FG,
            bg: SCREEN,
            bold: false,
        };
        Canvas {
            w,
            h,
            cells: vec![blank; w * h],
        }
    }

    pub fn cell(&self, x: usize, y: usize) -> &Cell {
        &self.cells[y * self.w + x]
    }

    /// Write one cell, blanking any wide character it cuts in half.
    fn put(&mut self, x: usize, y: usize, cell: Cell) {
        if x >= self.w || y >= self.h {
            return;
        }
        let i = y * self.w + x;
        let old = self.cells[i].ch;
        if old == '\0' && x > 0 {
            self.cells[i - 1].ch = ' ';
        }
        if cw(old) == 2 && x + 1 < self.w && self.cells[i + 1].ch == '\0' {
            self.cells[i + 1].ch = ' ';
        }
        self.cells[i] = cell;
    }

    pub fn fill(&mut self, x: usize, y: usize, w: usize, h: usize, bg: Rgb) {
        for yy in y..(y + h).min(self.h) {
            for xx in x..(x + w).min(self.w) {
                self.put(
                    xx,
                    yy,
                    Cell {
                        ch: ' ',
                        fg: FG,
                        bg,
                        bold: false,
                    },
                );
            }
        }
    }

    /// Draw `s` from `x`, never past column `limit`; returns the column after the text.
    pub fn text(&mut self, x: usize, y: usize, s: &str, style: Style, limit: usize) -> usize {
        let limit = limit.min(self.w);
        let mut x = x;
        for c in s.chars() {
            let n = cw(c);
            if n == 0 {
                continue;
            }
            if x + n > limit {
                if x < limit {
                    self.glyph(x, y, ' ', style);
                    x += 1;
                }
                break;
            }
            self.glyph(x, y, c, style);
            if n == 2 {
                self.glyph(x + 1, y, '\0', style);
            }
            x += n;
        }
        x
    }

    pub fn glyph(&mut self, x: usize, y: usize, ch: char, style: Style) {
        if x >= self.w || y >= self.h {
            return;
        }
        let bg = style.bg.unwrap_or(self.cells[y * self.w + x].bg);
        self.put(
            x,
            y,
            Cell {
                ch,
                fg: style.fg,
                bg,
                bold: style.bold,
            },
        );
    }

    /// Restyle a single cell (and its other half, for wide characters).
    pub fn restyle(&mut self, x: usize, y: usize, f: impl Fn(&mut Cell)) {
        if x >= self.w || y >= self.h {
            return;
        }
        let i = y * self.w + x;
        f(&mut self.cells[i]);
        if cw(self.cells[i].ch) == 2 && x + 1 < self.w {
            f(&mut self.cells[i + 1]);
        }
    }

    /// Copy `rows` rows of `src`, starting at its row `sy`, to (`dx`, `dy`).
    pub fn blit(&mut self, src: &Canvas, sy: usize, dx: usize, dy: usize, rows: usize) {
        for r in 0..rows.min(src.h.saturating_sub(sy)) {
            for x in 0..src.w {
                if dx + x < self.w && dy + r < self.h {
                    self.put(dx + x, dy + r, *src.cell(x, sy + r));
                }
            }
        }
    }

    pub fn map_all(&mut self, f: impl Fn(&mut Cell)) {
        self.cells.iter_mut().for_each(f);
    }
}

/// Textual's `ScrollBar.render_bar` (vertical, 2 cells thick).
pub fn scrollbar(
    c: &mut Canvas,
    x: usize,
    y: usize,
    size: usize,
    virt: usize,
    window: usize,
    pos: usize,
) {
    const BARS: [char; 8] = ['▁', '▂', '▃', '▄', '▅', '▆', '▇', ' '];
    let mut cells = vec![(' ', FG, BAR_BACK); size];
    if size > 0 && virt > 0 && window > 0 && size != virt {
        let thumb = (window as f64 / (virt as f64 / size as f64)).max(1.0);
        let ratio = if virt > window {
            pos as f64 / (virt - window) as f64
        } else {
            0.0
        };
        let position = (size as f64 - thumb) * ratio;
        let start = (position * 8.0) as usize;
        let end = start + (thumb * 8.0).ceil() as usize;
        let (si, sb) = (start / 8, start % 8);
        let (ei, eb) = (end / 8, end % 8);
        for cell in cells.iter_mut().take(ei.min(size)).skip(si) {
            *cell = (' ', FG, BAR);
        }
        if si < size && BARS[7 - sb] != ' ' {
            cells[si] = (BARS[7 - sb], BAR, BAR_BACK);
        }
        if ei < size && BARS[7 - eb] != ' ' {
            cells[ei] = (BARS[7 - eb], BAR_BACK, BAR);
        }
    }
    for (i, (ch, fg, bg)) in cells.into_iter().enumerate() {
        for dx in 0..2 {
            c.glyph(x + dx, y + i, ch, st(fg, bg));
        }
    }
}
