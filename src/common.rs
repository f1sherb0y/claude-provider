//! Shared helpers: paths, atomic writes, backups, clipboard, pasted-text parsing.
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::OnceLock;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use indexmap::IndexMap;
use regex::Regex;
use serde_json::Value;

pub const MAX_BACKUPS: usize = 20;

pub type Dict = IndexMap<String, String>;

pub fn home() -> PathBuf {
    // Windows: like Python's Path.home() (USERPROFILE); HOME may be an MSYS path there
    #[cfg(windows)]
    #[allow(deprecated)]
    if let Some(h) = std::env::home_dir() {
        return h;
    }
    match std::env::var_os("HOME") {
        Some(h) if !h.is_empty() => PathBuf::from(h),
        #[allow(deprecated)]
        _ => std::env::home_dir().unwrap_or_else(|| PathBuf::from("/")),
    }
}

/// `os.environ.get(name) or default`
pub fn env_dir(name: &str, default: &str) -> PathBuf {
    match std::env::var_os(name) {
        Some(v) if !v.is_empty() => PathBuf::from(v),
        _ => home().join(default),
    }
}

pub fn config_dir() -> PathBuf {
    env_dir("CLAUDE_PROVIDER_HOME", ".claude-provider")
}
pub fn data_file() -> PathBuf {
    config_dir().join("providers.json")
}
pub fn backup_dir() -> PathBuf {
    config_dir().join("backups")
}

// ---------------------------------------------------------------- small helpers

pub fn mask(secret: &str) -> String {
    let chars: Vec<char> = secret.chars().collect();
    if chars.is_empty() {
        return "-".into();
    }
    if chars.len() <= 12 {
        return chars[..3.min(chars.len())].iter().collect::<String>() + "…";
    }
    let head: String = chars[..7].iter().collect();
    let tail: String = chars[chars.len() - 4..].iter().collect();
    format!("{head}…{tail}")
}

/// Hostname like urllib's `urlparse(url).hostname`.
fn hostname(url: &str) -> Option<String> {
    let colon = url.find(':')?;
    let scheme = &url[..colon];
    let valid = scheme
        .chars()
        .next()
        .is_some_and(|c| c.is_ascii_alphabetic())
        && scheme
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "+-.".contains(c));
    if !valid {
        return None;
    }
    let rest = url[colon + 1..].strip_prefix("//")?;
    let end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
    let netloc = &rest[..end];
    let hostinfo = netloc.rsplit_once('@').map_or(netloc, |(_, h)| h);
    let host = if let Some(bracketed) = hostinfo.strip_prefix('[') {
        bracketed.split(']').next().unwrap_or("")
    } else {
        hostinfo.split(':').next().unwrap_or("")
    };
    if host.is_empty() {
        None
    } else {
        Some(host.to_lowercase())
    }
}

pub fn name_from_url(url: &str) -> String {
    let full = if url.contains("://") {
        url.to_string()
    } else {
        format!("https://{url}")
    };
    let host = hostname(&full).unwrap_or_else(|| url.to_string());
    let host = host.strip_prefix("www.").unwrap_or(&host);
    host.strip_prefix("api.").unwrap_or(host).to_string()
}

/// Python's str.splitlines().
pub fn splitlines(text: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let mut start = 0;
    let mut chars = text.char_indices().peekable();
    while let Some((i, c)) = chars.next() {
        let brk = matches!(
            c,
            '\n' | '\r'
                | '\x0b'
                | '\x0c'
                | '\x1c'
                | '\x1d'
                | '\x1e'
                | '\u{85}'
                | '\u{2028}'
                | '\u{2029}'
        );
        if brk {
            out.push(&text[start..i]);
            let mut next = i + c.len_utf8();
            if c == '\r' && matches!(chars.peek(), Some((_, '\n'))) {
                chars.next();
                next += 1;
            }
            start = next;
        }
    }
    if start < text.len() {
        out.push(&text[start..]);
    }
    out
}

/// Python's str.strip() (whitespace per str.isspace()).
pub fn py_strip(s: &str) -> &str {
    s.trim_matches(|c: char| c.is_whitespace() || ('\x1c'..='\x1f').contains(&c))
}

/// Python's str(value) for a JSON value.
pub fn py_str(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        other => py_repr(other),
    }
}

fn py_repr(v: &Value) -> String {
    match v {
        Value::Null => "None".into(),
        Value::Bool(b) => if *b { "True" } else { "False" }.into(),
        Value::Number(n) => {
            if n.is_f64() {
                let f = n.as_f64().unwrap_or(0.0);
                let s = format!("{f:?}");
                match s.split_once('e') {
                    Some((m, e)) if !e.starts_with('-') => format!("{m}e+{e:0>2}"),
                    Some((m, e)) => format!("{m}e-{:0>2}", &e[1..]),
                    None => s,
                }
            } else {
                n.to_string()
            }
        }
        Value::String(s) => format!("'{}'", s.replace('\\', "\\\\").replace('\'', "\\'")),
        Value::Array(a) => format!("[{}]", a.iter().map(py_repr).collect::<Vec<_>>().join(", ")),
        Value::Object(o) => format!(
            "{{{}}}",
            o.iter()
                .map(|(k, v)| format!("{}: {}", py_repr(&Value::String(k.clone())), py_repr(v)))
                .collect::<Vec<_>>()
                .join(", ")
        ),
    }
}

// ---------------------------------------------------------------- files

fn tmp_suffix() -> String {
    static COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let n = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0) as u64
        ^ ((std::process::id() as u64) << 32)
        ^ COUNTER
            .fetch_add(1, std::sync::atomic::Ordering::Relaxed)
            .wrapping_mul(0x9E37_79B9_7F4A_7C15);
    let alphabet = b"abcdefghijklmnopqrstuvwxyz0123456789_";
    let mut x = n;
    (0..8)
        .map(|_| {
            let c = alphabet[(x % alphabet.len() as u64) as usize] as char;
            x = x / alphabet.len() as u64 ^ x.rotate_left(17);
            c
        })
        .collect()
}

/// Write via a private (0600) temp file in the same directory, then rename over `path`.
pub fn atomic_write(path: &Path, data: &[u8]) -> Result<(), String> {
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    fs::create_dir_all(parent).map_err(|e| format!("{}: {e}", parent.display()))?;
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let (tmp, mut file) = loop {
        let tmp = parent.join(format!("{name}.{}.cp-tmp", tmp_suffix()));
        let mut opts = fs::OpenOptions::new();
        opts.write(true).create_new(true);
        #[cfg(unix)]
        std::os::unix::fs::OpenOptionsExt::mode(&mut opts, 0o600);
        match opts.open(&tmp) {
            Ok(f) => break (tmp, f),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(format!("{}: {e}", tmp.display())),
        }
    };
    let result = file
        .write_all(data)
        .and_then(|_| file.flush())
        .and_then(|_| {
            drop(file);
            rename_retry(&tmp, path)
        })
        .map_err(|e| format!("{}: {e}", path.display()));
    let _ = fs::remove_file(&tmp);
    result
}

/// On Windows a rename over a file that another process (editor, antivirus, the tool
/// itself) briefly has open fails with "access denied"; retry for a moment.
fn rename_retry(from: &Path, to: &Path) -> std::io::Result<()> {
    let mut tries = 0;
    loop {
        match fs::rename(from, to) {
            Err(e) if cfg!(windows) && e.kind() == std::io::ErrorKind::PermissionDenied && tries < 20 => {
                tries += 1;
                std::thread::sleep(Duration::from_millis(50));
            }
            r => return r,
        }
    }
}

/// JSON like Python's `json.dumps(data, indent=2, ensure_ascii=False) + "\n"`.
pub fn to_json(data: &Value) -> String {
    let mut s = serde_json::to_string_pretty(data).unwrap_or_default();
    s.push('\n');
    s
}

pub fn write_json(path: &Path, data: &Value) -> Result<(), String> {
    atomic_write(path, to_json(data).as_bytes())
}

/// (stem, suffix) with pathlib semantics.
pub fn stem_suffix(name: &str) -> (&str, &str) {
    match name.rfind('.') {
        Some(i) if i > 0 && i < name.len() - 1 => (&name[..i], &name[i..]),
        _ => (name, ""),
    }
}

/// Copy `path` into the backup dir as `<stem>.<timestamp><suffix>`, keeping the newest MAX_BACKUPS.
pub fn backup(path: &Path) -> Result<(), String> {
    if !path.exists() {
        return Ok(());
    }
    let dir = backup_dir();
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
    builder
        .create(&dir)
        .map_err(|e| format!("{}: {e}", dir.display()))?;
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let (stem, suffix) = stem_suffix(&name);
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let stamp = format!(
        "{}.{:06}",
        chrono::Local::now().format("%Y%m%d_%H%M%S"),
        nanos % 1_000_000
    );
    let data = fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
    atomic_write(&dir.join(format!("{stem}.{stamp}{suffix}")), &data)?;
    let prefix = format!("{stem}.");
    let mut old: Vec<String> = fs::read_dir(&dir)
        .map_err(|e| e.to_string())?
        .filter_map(|e| e.ok()?.file_name().into_string().ok())
        .filter(|n| {
            n.len() >= prefix.len() + suffix.len()
                && n.starts_with(&prefix)
                && n.ends_with(suffix)
                && (!n.starts_with('.') || prefix.starts_with('.'))
        })
        .collect();
    old.sort();
    if old.len() > MAX_BACKUPS {
        for n in &old[..old.len() - MAX_BACKUPS] {
            let _ = fs::remove_file(dir.join(n));
        }
    }
    Ok(())
}

// ---------------------------------------------------------------- clipboard

fn which(cmd: &str) -> bool {
    let Some(paths) = std::env::var_os("PATH") else {
        return false;
    };
    std::env::split_paths(&paths).any(|dir| {
        let p = dir.join(cmd);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            p.metadata()
                .is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
        }
        #[cfg(not(unix))]
        {
            p.is_file() || p.with_extension("exe").is_file()
        }
    })
}

fn run_with_timeout(cmd: &[&str], timeout: Duration) -> Option<(bool, Vec<u8>)> {
    let mut child = Command::new(cmd[0])
        .args(&cmd[1..])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .ok()?;
    let mut stdout = child.stdout.take()?;
    let reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stdout.read_to_end(&mut buf);
        buf
    });
    let start = Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return Some((status.success(), reader.join().ok()?)),
            Ok(None) if start.elapsed() < timeout => std::thread::sleep(Duration::from_millis(10)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return None;
            }
        }
    }
}

pub fn read_clipboard() -> String {
    let commands: Vec<Vec<&str>> = if cfg!(windows) {
        vec![vec![
            "powershell",
            "-NoProfile",
            "-Command",
            // default console encoding would mangle non-ASCII text
            "[Console]::OutputEncoding = [Text.Encoding]::UTF8; Get-Clipboard -Raw",
        ]]
    } else if cfg!(target_os = "macos") {
        vec![vec!["pbpaste"]]
    } else {
        let wayland = vec![vec!["wl-paste", "--no-newline"]];
        let x11 = vec![
            vec!["xclip", "-o", "-selection", "clipboard"],
            vec!["xsel", "--clipboard", "--output"],
        ];
        if std::env::var_os("WAYLAND_DISPLAY").is_some() {
            wayland.into_iter().chain(x11).collect()
        } else {
            x11.into_iter().chain(wayland).collect()
        }
    };
    for cmd in commands {
        if !which(cmd[0]) {
            continue;
        }
        if let Some((true, out)) = run_with_timeout(&cmd, Duration::from_secs(5)) {
            if let Ok(text) = String::from_utf8(out) {
                let text = text.strip_prefix('\u{feff}').unwrap_or(&text);
                return text.replace("\r\n", "\n").replace('\r', "\n");
            }
        }
    }
    String::new()
}

// ---------------------------------------------------------------- pasted-text parsing

fn re(cell: &'static OnceLock<Regex>, pat: &str) -> &'static Regex {
    cell.get_or_init(|| Regex::new(pat).expect("valid regex"))
}

/// bash `export K=V`, PowerShell `$env:K="V"`, cmd `set K=V`, or plain K=V lines.
pub fn parse_env_script(text: &str) -> Dict {
    static ENV_LINE: OnceLock<Regex> = OnceLock::new();
    let pat = re(
        &ENV_LINE,
        r"^\s*(?:export\s+|set\s+|\$env:)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*;?\s*$",
    );
    let mut env = Dict::new();
    for line in splitlines(text) {
        if let Some(m) = pat.captures(line) {
            let mut v = &m[2];
            let b = v.as_bytes();
            if b.len() >= 2 && b[0] == b[b.len() - 1] && (b[0] == b'"' || b[0] == b'\'') {
                v = &v[1..v.len() - 1];
            }
            env.insert(m[1].to_string(), v.to_string());
        }
    }
    env
}

pub fn load_json_obj(text: &str) -> Option<serde_json::Map<String, Value>> {
    match serde_json::from_str::<Value>(text) {
        Ok(Value::Object(m)) => Some(m),
        _ => None,
    }
}

/// parse() output: field name -> value (`extra_env` carries a dict).
pub type Info = IndexMap<String, crate::backend::Val>;

pub fn info_str(info: &Info, key: &str) -> String {
    match info.get(key) {
        Some(crate::backend::Val::S(s)) => s.clone(),
        _ => String::new(),
    }
}

pub fn set_info(info: &mut Info, key: &str, value: impl Into<String>) {
    info.insert(key.to_string(), crate::backend::Val::S(value.into()));
}

/// NewAPI-style JSON ({"url":..,"key":..}) or free text containing a URL and a key.
pub fn parse_generic(text: &str) -> Info {
    let mut out = Info::new();
    if let Some(data) = load_json_obj(text) {
        let mut lower: IndexMap<String, String> = IndexMap::new();
        for (k, v) in &data {
            if let Value::String(s) = v {
                lower.insert(k.to_lowercase(), s.clone());
            }
        }
        let pick = |keys: &[&str]| {
            keys.iter()
                .find_map(|k| lower.get(*k).filter(|v| !v.is_empty()).cloned())
        };
        if let Some(v) = pick(&["url", "base_url", "baseurl", "endpoint", "host"]) {
            set_info(&mut out, "base_url", v);
        }
        if let Some(v) = pick(&["key", "api_key", "apikey", "token", "auth_token"]) {
            set_info(&mut out, "key", v);
        }
        for k in ["name", "model"] {
            if let Some(v) = lower.get(k).filter(|v| !v.is_empty()) {
                set_info(&mut out, k, v.clone());
            }
        }
        return out;
    }
    static URL: OnceLock<Regex> = OnceLock::new();
    static KEY: OnceLock<Regex> = OnceLock::new();
    if let Some(m) = re(&URL, r#"https?://[^\s"',]+"#).find(text) {
        set_info(&mut out, "base_url", m.as_str());
    }
    let rest = text.replace(&info_str(&out, "base_url"), "");
    if let Some(m) = re(&KEY, r"\b(sk-[A-Za-z0-9_\-]{8,}|[A-Za-z0-9_\-]{32,})\b").captures(&rest) {
        set_info(&mut out, "key", &m[1]);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mask_matches_python() {
        assert_eq!(mask(""), "-");
        assert_eq!(mask("abcdef"), "abc…");
        assert_eq!(mask("sk-RoGTabcdefghijklmnopqrstH0Kg"), "sk-RoGT…H0Kg");
    }

    #[test]
    fn names_from_urls() {
        assert_eq!(name_from_url("https://api.foo.com/v1"), "foo.com");
        assert_eq!(
            name_from_url("https://www.fastaitoken.com"),
            "fastaitoken.com"
        );
        assert_eq!(name_from_url("nowcoding.ai:8080/x"), "nowcoding.ai");
        assert_eq!(
            name_from_url("https://user@API.Example.com:1"),
            "example.com"
        );
    }

    #[test]
    fn env_script() {
        let env = parse_env_script("export A=\"1\"\r\n$env:B='x y';\nset C = 3\n# no\n");
        assert_eq!(env.get("A").unwrap(), "1");
        assert_eq!(env.get("B").unwrap(), "x y");
        assert_eq!(env.get("C").unwrap(), "3");
        assert_eq!(env.len(), 3);
    }

    #[test]
    fn generic() {
        let out = parse_generic("url: https://a.b/v1, key sk-abcdefghij12345");
        assert_eq!(info_str(&out, "base_url"), "https://a.b/v1");
        assert_eq!(info_str(&out, "key"), "sk-abcdefghij12345");
        let out = parse_generic(r#"{"URL": "https://x", "Token": "t", "model": 1}"#);
        assert_eq!(out.keys().collect::<Vec<_>>(), ["base_url", "key"]);
    }

    #[test]
    fn stems() {
        assert_eq!(stem_suffix("settings.json"), ("settings", ".json"));
        assert_eq!(stem_suffix(".env"), (".env", ""));
        assert_eq!(stem_suffix("a.tar.gz"), ("a.tar", ".gz"));
    }
}
