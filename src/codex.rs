//! Codex backend: ~/.codex/config.toml -> model_provider + [model_providers.<id>].
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::sync::OnceLock;

use regex::Regex;
use toml_edit::{DocumentMut, Item, Table, TableLike, Value};

use crate::backend::{Backend, Field, Kind, Provider};
use crate::common::{
    Dict, Info, atomic_write, backup, env_dir, info_str, load_json_obj, name_from_url,
    parse_env_script, parse_generic, py_str, py_strip, set_info, splitlines,
};

pub const WIRE_APIS: &[&str] = &["responses", "chat"];

pub const FIELDS: &[Field] = &[
    Field::text("name", "名称", ""),
    Field::text("id", "配置 ID", "model_providers 下的键（留空自动生成）"),
    Field::text(
        "base_url",
        "Base URL",
        "https://api.example.com/v1（留空 = 官方 ChatGPT 登录）",
    ),
    Field {
        name: "key",
        label: "Key",
        kind: Kind::Password,
        options: &[],
        placeholder: "",
        default: "",
    },
    Field {
        name: "wire_api",
        label: "wire_api",
        kind: Kind::Select,
        options: WIRE_APIS,
        placeholder: "",
        default: "responses",
    },
    Field::text("model", "model", "可选，留空 = 不改当前模型"),
    Field::text("env_key", "env_key", "可选，填了则 Key 写入 ~/.codex/.env"),
];

pub const PASTE_HINT: &str = "[model_providers.x] TOML / export OPENAI_* / JSON / URL+Key";

pub fn codex_home() -> PathBuf {
    env_dir("CODEX_HOME", ".codex")
}
pub fn config_file() -> PathBuf {
    codex_home().join("config.toml")
}
fn dotenv_file() -> PathBuf {
    codex_home().join(".env")
}

/// Codex wants the API root (usually ending in /v1); drop endpoint paths people paste.
pub fn normalize_base_url(url: &str) -> String {
    static RE: OnceLock<Regex> = OnceLock::new();
    let url = py_strip(url).trim_end_matches('/');
    if url.is_empty() {
        return String::new();
    }
    RE.get_or_init(|| Regex::new(r"(?i)/(responses|chat/completions)$").unwrap())
        .replace(url, "")
        .into_owned()
}

pub fn slug(text: &str) -> String {
    static RE: OnceLock<Regex> = OnceLock::new();
    let s = RE
        .get_or_init(|| Regex::new(r"[^a-z0-9_-]+").unwrap())
        .replace_all(&text.to_lowercase(), "_")
        .into_owned();
    let s = s.trim_matches('_');
    if s.is_empty() {
        "provider".into()
    } else {
        s.into()
    }
}

// ---------------------------------------------------------------- toml helpers

/// A basic string escaped the way tomlkit writes it.
fn tstr(s: &str) -> Value {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\t' => out.push_str("\\t"),
            '\n' => out.push_str("\\n"),
            '\u{c}' => out.push_str("\\f"),
            '\r' => out.push_str("\\r"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => {
                out.push_str(&format!("\\u{:04x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out.push('"');
    Value::from_str(&out).unwrap_or_else(|_| Value::from(s))
}

/// Set `key`, keeping the old value's surrounding whitespace / trailing comment.
fn set_str(t: &mut dyn TableLike, key: &str, s: &str) {
    let v = tstr(s);
    match t.get_mut(key) {
        Some(Item::Value(old)) => {
            let decor = old.decor().clone();
            *old = v;
            *old.decor_mut() = decor;
        }
        Some(item) => *item = Item::Value(v),
        None => {
            t.insert(key, Item::Value(v));
        }
    }
}

/// Remove `key` but keep comment lines written above it (they belong to the next key).
fn remove_keep_comments(t: &mut Table, key: &str, doc_prefix: Option<&mut String>) {
    let Some(k) = t.key(key) else { return };
    let prefix = k
        .leaf_decor()
        .prefix()
        .and_then(|p| p.as_str())
        .unwrap_or("")
        .to_string();
    let keys: Vec<String> = t.iter().map(|(k, _)| k.to_string()).collect();
    let next = keys
        .iter()
        .position(|k| k == key)
        .and_then(|i| keys.get(i + 1))
        .cloned();
    t.remove(key);
    if !prefix.contains('#') {
        return;
    }
    if let Some(next) = next {
        if let Some(mut nk) = t.key_mut(&next) {
            let old = nk
                .leaf_decor()
                .prefix()
                .and_then(|p| p.as_str())
                .unwrap_or("")
                .to_string();
            nk.leaf_decor_mut().set_prefix(prefix + &old);
        }
    } else if let Some(dp) = doc_prefix {
        dp.push_str(&prefix);
    }
}

fn item_str(item: Option<&Item>) -> Option<String> {
    match item? {
        Item::Value(Value::String(s)) => Some(s.value().clone()),
        Item::Value(v) => Some(v.to_string().trim().to_string()),
        _ => None,
    }
}

fn read_doc() -> Result<DocumentMut, String> {
    let path = config_file();
    if !path.exists() {
        return Ok(DocumentMut::new());
    }
    let text = std::fs::read_to_string(&path).map_err(|e| e.to_string())?;
    text.parse::<DocumentMut>().map_err(|e| e.to_string())
}

fn read_dotenv(path: &Path) -> Dict {
    std::fs::read_to_string(path)
        .map(|t| parse_env_script(&t))
        .unwrap_or_default()
}

fn write_dotenv(path: &Path, name: &str, value: &str) -> Result<(), String> {
    let text = if path.exists() {
        std::fs::read_to_string(path).map_err(|e| e.to_string())?
    } else {
        String::new()
    };
    let mut lines: Vec<String> = splitlines(&text).into_iter().map(String::from).collect();
    let pat = Regex::new(&format!(r"^\s*(export\s+)?{}\s*=", regex::escape(name)))
        .map_err(|e| e.to_string())?;
    match lines.iter().position(|l| pat.is_match(l)) {
        Some(i) => lines[i] = format!("{name}={value}"),
        None => lines.push(format!("{name}={value}")),
    }
    backup(path)?;
    atomic_write(path, (lines.join("\n") + "\n").as_bytes())
}

/// The table whose header is printed first below `t` (skipping implicit super-tables).
fn printed_table(t: &mut Table) -> Option<&mut Table> {
    if !t.is_implicit() {
        return Some(t);
    }
    t.iter_mut()
        .find_map(|(_, i)| i.as_table_mut())
        .and_then(printed_table)
}

/// Keep the layout readable after root keys were added to a file that had none.
fn tidy(doc: &mut DocumentMut, had_values: bool, was_empty: bool) {
    let root = doc.as_table_mut();
    if had_values || !root.iter().any(|(_, item)| item.is_value()) {
        return;
    }
    let first_table = |root: &Table| {
        root.iter()
            .filter_map(|(k, item)| {
                item.as_table()
                    .filter(|t| !t.is_implicit())
                    .map(|t| (t.position(), k.to_string()))
            })
            .min()
            .map(|(_, k)| k)
    };
    // the first table header was at the top of the file: separate it from the new keys
    if let Some(t) = first_table(root)
        .and_then(|k| root.get_mut(&k))
        .and_then(Item::as_table_mut)
    {
        let prefix = t
            .decor()
            .prefix()
            .and_then(|p| p.as_str())
            .unwrap_or("")
            .to_string();
        if !prefix.starts_with('\n') {
            t.decor_mut().set_prefix(format!("\n{prefix}"));
        }
    }
    // a comment-only file keeps its text in the trailing decor; move it up under model_provider
    let trailing = doc.trailing().as_str().unwrap_or("").to_string();
    if !was_empty || !trailing.contains('#') {
        return;
    }
    let root = doc.as_table_mut();
    let next_key = root
        .iter()
        .filter(|(k, i)| i.is_value() && *k != "model_provider")
        .map(|(k, _)| k.to_string())
        .next();
    if let Some(mut key) = next_key.as_deref().and_then(|k| root.key_mut(k)) {
        let old = key
            .leaf_decor()
            .prefix()
            .and_then(|p| p.as_str())
            .unwrap_or("")
            .to_string();
        key.leaf_decor_mut().set_prefix(trailing + &old);
    } else if let Some(t) = root
        .iter_mut()
        .find_map(|(_, i)| i.as_table_mut())
        .and_then(printed_table)
    {
        t.decor_mut().set_prefix(trailing + "\n");
    } else {
        return;
    }
    doc.set_trailing("");
}

fn current_provider(doc: &DocumentMut) -> Option<String> {
    item_str(doc.get("model_provider"))
}

// ---------------------------------------------------------------- backend

/// First run: import every [model_providers.*] table plus an official entry.
pub fn bootstrap(b: &mut Backend) -> Result<(), String> {
    let doc = read_doc()?;
    let dotenv = read_dotenv(&dotenv_file());
    let current = current_provider(&doc);
    b.providers.clear();
    if let Some(tables) = doc.get("model_providers").and_then(Item::as_table_like) {
        for (pid, t) in tables.iter() {
            let Some(t) = t.as_table_like() else { continue };
            let env_key = item_str(t.get("env_key")).unwrap_or_default();
            let mut key = item_str(t.get("experimental_bearer_token")).unwrap_or_default();
            if key.is_empty() {
                key = dotenv
                    .get(&env_key)
                    .cloned()
                    .filter(|k| !k.is_empty())
                    .unwrap_or_else(|| {
                        if env_key.is_empty() {
                            String::new()
                        } else {
                            std::env::var(&env_key).unwrap_or_default()
                        }
                    });
            }
            let mut p = Provider::new(FIELDS);
            p.set(
                "name",
                item_str(t.get("name")).unwrap_or_else(|| pid.to_string()),
            );
            p.set("id", pid);
            p.set("base_url", item_str(t.get("base_url")).unwrap_or_default());
            p.set("key", key);
            p.set(
                "wire_api",
                item_str(t.get("wire_api")).unwrap_or_else(|| "responses".into()),
            );
            if current.as_deref() == Some(pid) {
                p.set("model", item_str(doc.get("model")).unwrap_or_default());
            }
            p.set("env_key", env_key);
            b.providers.push(p);
        }
    }
    b.providers
        .sort_by_key(|p| Some(p.s("id")) != current.as_deref());
    let mut official = Provider::new(FIELDS);
    official.set("name", "OpenAI 官方 (ChatGPT 登录)");
    b.providers.push(official);
    Ok(())
}

pub fn active(b: &Backend) -> Result<Option<usize>, String> {
    let doc = read_doc()?;
    let current = current_provider(&doc);
    let official = doc.get("model_provider").is_none() || current.as_deref() == Some("openai");
    Ok(b.providers.iter().position(|p| {
        if p.base_url().is_empty() {
            official
        } else {
            current.as_deref() == Some(p.s("id"))
        }
    }))
}

pub fn apply(p: &Provider) -> Result<(), String> {
    let mut doc = read_doc()?;
    let had_values = doc.iter().any(|(_, item)| item.is_value());
    let was_empty = doc.is_empty();
    if p.base_url().is_empty() {
        let mut prefix = doc
            .decor()
            .prefix()
            .and_then(|p| p.as_str())
            .unwrap_or("")
            .to_string();
        remove_keep_comments(doc.as_table_mut(), "model_provider", Some(&mut prefix));
        doc.decor_mut().set_prefix(prefix);
    } else {
        let id = p.s("id");
        if doc.get("model_providers").is_none() {
            let mut t = Table::new();
            t.set_implicit(true);
            doc.insert("model_providers", Item::Table(t));
        }
        let providers = doc["model_providers"]
            .as_table_like_mut()
            .ok_or("model_providers 不是表")?;
        if providers.get(id).is_none() {
            if providers.is_dotted() || doc["model_providers"].is_inline_table() {
                return Err("Inline tables cannot contain a table".into());
            }
            doc["model_providers"]
                .as_table_like_mut()
                .unwrap()
                .insert(id, Item::Table(Table::new()));
        }
        let item = &mut doc["model_providers"][id];
        // update in place so unrelated keys (env_key_instructions, headers, ...) survive
        let table = item
            .as_table_like_mut()
            .ok_or_else(|| format!("model_providers.{id} 不是表"))?;
        set_str(table, "name", p.name());
        set_str(table, "base_url", p.base_url());
        set_str(table, "wire_api", p.s("wire_api"));
        let env_key = p.s("env_key");
        if !env_key.is_empty() {
            set_str(table, "env_key", env_key);
            table.remove("experimental_bearer_token");
            if !p.key().is_empty() {
                write_dotenv(&dotenv_file(), env_key, p.key())?;
            }
        } else {
            table.remove("env_key");
            set_str(table, "experimental_bearer_token", p.key());
        }
        if doc.contains_key("model_provider") {
            set_str(doc.as_table_mut(), "model_provider", id);
        } else {
            // put it back at the top where it normally lives
            let root = doc.as_table_mut();
            let old = std::mem::take(root);
            let mut new = Table::new();
            new.set_implicit(old.is_implicit());
            new.set_position(old.position());
            *new.decor_mut() = old.decor().clone();
            new.insert("model_provider", Item::Value(tstr(id)));
            for (k, item) in old.iter() {
                if let Some(key) = old.key(k) {
                    new.insert_formatted(key, item.clone());
                }
            }
            *root = new;
        }
    }
    if !p.model().is_empty() {
        set_str(doc.as_table_mut(), "model", p.model());
    }
    tidy(&mut doc, had_values, was_empty);
    let target = config_file();
    backup(&target)?;
    atomic_write(&target, doc.to_string().as_bytes())
}

/// `[model_providers.x]` TOML, `export OPENAI_*`, JSON, or generic URL+key.
pub fn parse(text: &str) -> Info {
    let text = py_strip(text);
    let mut out = Info::new();
    let data = text.parse::<DocumentMut>().ok();
    let mut handled = false;
    if let Some(data) = data.as_ref().filter(|d| !d.is_empty()) {
        let mut table: Option<&dyn TableLike> = None;
        match data.get("model_providers").and_then(Item::as_table_like) {
            Some(tables) if !tables.is_empty() => {
                if let Some((pid, t)) = tables.iter().next() {
                    set_info(&mut out, "id", pid);
                    table = t.as_table_like();
                }
            }
            _ => {
                if data.contains_key("base_url") {
                    table = Some(data.as_table());
                }
            }
        }
        if let Some(table) = table {
            handled = true;
            for (src, dst) in [
                ("name", "name"),
                ("base_url", "base_url"),
                ("wire_api", "wire_api"),
                ("env_key", "env_key"),
                ("experimental_bearer_token", "key"),
            ] {
                if let Some(Item::Value(Value::String(s))) = table.get(src) {
                    set_info(&mut out, dst, s.value().clone());
                }
            }
            if let Some(Item::Value(Value::String(s))) = data.get("model") {
                set_info(&mut out, "model", s.value().clone());
            }
        }
    }
    if !handled {
        let env: Dict = match load_json_obj(text).filter(|o| !o.is_empty()) {
            Some(obj) => obj.iter().map(|(k, v)| (k.clone(), py_str(v))).collect(),
            None => parse_env_script(text),
        };
        match env.get("OPENAI_BASE_URL").filter(|v| !v.is_empty()) {
            Some(url) => {
                set_info(&mut out, "base_url", url.clone());
                for (src, dst) in [("OPENAI_API_KEY", "key"), ("OPENAI_MODEL", "model")] {
                    if let Some(v) = env.get(src).filter(|v| !v.is_empty()) {
                        set_info(&mut out, dst, v.clone());
                    }
                }
            }
            None => out = parse_generic(text),
        }
    }
    let url = info_str(&out, "base_url");
    if !url.is_empty() {
        let url = normalize_base_url(&url);
        set_info(&mut out, "base_url", url.clone());
        if !out.contains_key("name") {
            set_info(&mut out, "name", name_from_url(&url));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn slugs() {
        assert_eq!(slug("Now Coding 全球加速"), "now_coding");
        assert_eq!(slug("全球"), "provider");
    }

    #[test]
    fn parses_toml() {
        let out = parse(
            "model = \"m\"\n[model_providers.foo]\nname = \"Foo\"\nbase_url = \"https://x/v1/responses\"\n",
        );
        assert_eq!(
            out.keys().collect::<Vec<_>>(),
            ["id", "name", "base_url", "model"]
        );
        assert_eq!(info_str(&out, "base_url"), "https://x/v1");
    }

    #[test]
    fn parses_exports() {
        let out =
            parse("export OPENAI_BASE_URL=https://api.foo.com/v1\nexport OPENAI_API_KEY=sk-1");
        assert_eq!(info_str(&out, "name"), "foo.com");
        assert_eq!(info_str(&out, "key"), "sk-1");
    }
}
