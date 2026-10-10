//! Cell-width helpers with rich's semantics (truncate with "…", word wrap).
use unicode_width::UnicodeWidthChar;

pub fn cw(c: char) -> usize {
    if c == '\t' { 1 } else { c.width().unwrap_or(0) }
}

pub fn width(s: &str) -> usize {
    s.chars().map(cw).sum()
}

/// rich's `set_cell_size`: crop or pad to exactly `w` cells (a split wide char becomes a space).
pub fn set_cell_size(s: &str, w: usize) -> String {
    let mut out = String::new();
    let mut used = 0;
    for c in s.chars() {
        let n = cw(c);
        if used + n > w {
            break;
        }
        out.push(c);
        used += n;
    }
    out.extend(std::iter::repeat_n(' ', w - used));
    out
}

/// rich's `Text.truncate(w, overflow="ellipsis")`.
pub fn truncate(s: &str, w: usize) -> String {
    if width(s) <= w {
        return s.to_string();
    }
    if w == 0 {
        return String::new();
    }
    set_cell_size(s, w - 1) + "…"
}

/// Char ranges of the lines rich / Textual would wrap `s` into (words keep trailing spaces).
pub fn wrap(s: &str, w: usize) -> Vec<(usize, usize)> {
    let chars: Vec<char> = s.chars().collect();
    let w = w.max(1);
    // words: leading whitespace (only at the start), non-space run, trailing whitespace
    let mut words = Vec::new();
    let mut i = 0;
    while i < chars.len() {
        let start = i;
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        while i < chars.len() && !chars[i].is_whitespace() {
            i += 1;
        }
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        words.push((start, i));
    }
    let mut breaks = Vec::new();
    let mut offset = 0;
    for (start, end) in words {
        let word = &chars[start..end];
        let trimmed = word
            .iter()
            .rposition(|c| !c.is_whitespace())
            .map_or(0, |p| p + 1);
        let word_len: usize = word[..trimmed].iter().map(|&c| cw(c)).sum();
        let full_len: usize = word.iter().map(|&c| cw(c)).sum();
        if w >= offset + word_len {
            offset += full_len;
        } else if word_len > w {
            // fold the word across lines
            let mut pos = start;
            let mut line_w = 0;
            let mut line_start = start;
            while pos < end {
                let n = cw(chars[pos]);
                if line_w + n > w && pos > line_start {
                    if line_start > 0 {
                        breaks.push(line_start);
                    }
                    line_start = pos;
                    line_w = 0;
                }
                line_w += n;
                pos += 1;
            }
            if line_start > 0 {
                breaks.push(line_start);
            }
            offset = line_w;
        } else if offset > 0 && start > 0 {
            breaks.push(start);
            offset = full_len;
        }
    }
    breaks.dedup();
    let mut out = Vec::new();
    let mut prev = 0;
    for b in breaks {
        if b > prev {
            out.push((prev, b));
            prev = b;
        }
    }
    out.push((prev, chars.len()));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncates_like_rich() {
        assert_eq!(
            truncate("OpenAI 官方 (ChatGPT 登录)", 24),
            "OpenAI 官方 (ChatGPT 登…"
        );
        assert_eq!(truncate("Now Coding 全球加速", 14), "Now Coding 全…");
        assert_eq!(truncate("OpenAI 官方 (ChatGPT 登录)", 14), "OpenAI 官方 (…");
        assert_eq!(truncate("abc", 3), "abc");
    }

    #[test]
    fn wraps_like_rich() {
        let s = "⚡ 粘贴 settings.json / env JSON / export / $env: / URL+Key，自动识别";
        let lines: Vec<String> = wrap(s, 46)
            .into_iter()
            .map(|(a, b)| s.chars().skip(a).take(b - a).collect())
            .collect();
        assert_eq!(
            lines,
            [
                "⚡ 粘贴 settings.json / env JSON / export / ",
                "$env: / URL+Key，自动识别"
            ]
        );
        let s = "已识别：base_url, key, key_var, extra_env, name";
        assert_eq!(wrap(s, 46).len(), 2);
        assert_eq!(wrap("abcdefgh", 3), [(0, 3), (3, 6), (6, 8)]);
        assert_eq!(wrap("", 3), [(0, 0)]);
    }
}
