//! Claude Code backend: ~/.claude/settings.json -> env.
use std::path::PathBuf;
use std::sync::OnceLock;

use regex::Regex;
use serde_json::{Map, Value};

use crate::backend::{Backend, Field, Kind, Provider, Val};
use crate::common::{
    Dict, Info, backup, env_dir, info_str, load_json_obj, name_from_url, parse_env_script,
    parse_generic, py_str, py_strip, set_info, write_json,
};

pub const KEY_VARS: &[&str] = &["ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"];

/// Env vars that always belong to "the provider" and are cleared on every switch.
pub const MANAGED_KEYS: &[&str] = &[
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_MODEL",
    "ANTHROPIC_SMALL_FAST_MODEL",
    "ANTHROPIC_DEFAULT_OPUS_MODEL",
    "ANTHROPIC_DEFAULT_SONNET_MODEL",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL",
    "CLAUDE_CODE_SUBAGENT_MODEL",
];

/// env var -> Provider field; anything else lands in extra_env
fn env_field(k: &str) -> Option<&'static str> {
    Some(match k {
        "ANTHROPIC_BASE_URL" => "base_url",
        "ANTHROPIC_AUTH_TOKEN" | "ANTHROPIC_API_KEY" => "key",
        "ANTHROPIC_MODEL" => "model",
        "ANTHROPIC_DEFAULT_HAIKU_MODEL" => "haiku_model",
        _ => return None,
    })
}

pub const FIELDS: &[Field] = &[
    Field::text("name", "名称", ""),
    Field::text(
        "base_url",
        "Base URL",
        "https://api.example.com（留空 = 官方登录）",
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
        name: "key_var",
        label: "Key 变量",
        kind: Kind::Select,
        options: KEY_VARS,
        placeholder: "",
        default: "ANTHROPIC_AUTH_TOKEN",
    },
    Field::text("model", "Model", "ANTHROPIC_MODEL（可选）"),
    Field::text(
        "haiku_model",
        "Haiku",
        "ANTHROPIC_DEFAULT_HAIKU_MODEL（可选）",
    ),
    Field {
        name: "extra_env",
        label: "额外 env",
        kind: Kind::Dict,
        options: &[],
        placeholder: "每行 KEY=VALUE",
        default: "",
    },
];

pub const PASTE_HINT: &str = "settings.json / env JSON / export / $env: / URL+Key";

pub fn settings_file() -> PathBuf {
    env_dir("CLAUDE_CONFIG_DIR", ".claude").join("settings.json")
}

/// Claude Code appends /v1/messages itself, so strip API path suffixes.
pub fn normalize_base_url(url: &str) -> String {
    static RE: OnceLock<Regex> = OnceLock::new();
    let url = py_strip(url).trim_end_matches('/');
    if url.is_empty() {
        return String::new();
    }
    let re = RE.get_or_init(|| Regex::new(r"(?i)(/v1)?(/messages)?$").unwrap());
    re.replace(url, "").into_owned()
}

pub fn to_env(p: &Provider) -> Dict {
    let mut env = Dict::new();
    if !p.base_url().is_empty() {
        env.insert(
            "ANTHROPIC_BASE_URL".into(),
            normalize_base_url(p.base_url()),
        );
    }
    if !p.key().is_empty() {
        env.insert(p.s("key_var").into(), p.key().into());
    }
    if !p.model().is_empty() {
        env.insert("ANTHROPIC_MODEL".into(), p.model().into());
    }
    if !p.s("haiku_model").is_empty() {
        env.insert(
            "ANTHROPIC_DEFAULT_HAIKU_MODEL".into(),
            p.s("haiku_model").into(),
        );
    }
    for (k, v) in p.dict("extra_env") {
        env.insert(k, v);
    }
    env
}

fn from_env(env: &Dict) -> Info {
    let mut out = Info::new();
    let mut extra = Dict::new();
    for (k, v) in env {
        match env_field(k) {
            None => {
                extra.insert(k.clone(), v.clone());
            }
            Some("key") => {
                // AUTH_TOKEN wins if both are present
                if !out.contains_key("key") || k == "ANTHROPIC_AUTH_TOKEN" {
                    set_info(&mut out, "key", v.clone());
                    set_info(&mut out, "key_var", k.clone());
                }
            }
            Some(f) => set_info(&mut out, f, v.clone()),
        }
    }
    if !extra.is_empty() {
        out.insert("extra_env".into(), Val::D(extra));
    }
    out
}

fn read_settings() -> Result<Map<String, Value>, String> {
    let path = settings_file();
    if !path.exists() {
        return Ok(Map::new());
    }
    let text = std::fs::read_to_string(&path).map_err(|e| e.to_string())?;
    match serde_json::from_str::<Value>(&text).map_err(|e| e.to_string())? {
        Value::Object(m) => Ok(m),
        _ => Err(format!("{}: 不是 JSON 对象", path.display())),
    }
}

fn settings_env(settings: &Map<String, Value>) -> Map<String, Value> {
    match settings.get("env") {
        Some(Value::Object(m)) => m.clone(),
        _ => Map::new(),
    }
}

/// First run: official login entry + whatever is configured right now.
pub fn bootstrap(b: &mut Backend) -> Result<(), String> {
    let mut official = Provider::new(FIELDS);
    official.set("name", "Anthropic 官方");
    b.providers = vec![official];
    let env = settings_env(&read_settings()?);
    if let Some(Value::String(base)) = env
        .get("ANTHROPIC_BASE_URL")
        .filter(|v| v.as_str() != Some(""))
    {
        let subset: Dict = env
            .iter()
            .filter(|(k, _)| env_field(k).is_some())
            .map(|(k, v)| (k.clone(), py_str(v)))
            .collect();
        let mut p = Provider::new(FIELDS);
        p.set("name", name_from_url(base));
        for (k, v) in from_env(&subset) {
            p.0.insert(k, v);
        }
        b.providers.insert(0, p);
        let mut keys: Vec<String> = env
            .keys()
            .filter(|k| MANAGED_KEYS.contains(&k.as_str()))
            .cloned()
            .collect();
        keys.sort();
        b.applied_keys = keys;
    }
    Ok(())
}

fn env_eq(env: &Map<String, Value>, k: &str, want: Option<&String>) -> bool {
    match (env.get(k), want) {
        (None, None) => true,
        (Some(Value::String(a)), Some(b)) => a == b,
        _ => false,
    }
}

pub fn active(b: &Backend) -> Result<Option<usize>, String> {
    let env = settings_env(&read_settings()?);
    for (i, p) in b.providers.iter().enumerate() {
        let pe = to_env(p);
        if MANAGED_KEYS.iter().all(|k| env_eq(&env, k, pe.get(*k)))
            && pe.iter().all(|(k, v)| env_eq(&env, k, Some(v)))
        {
            return Ok(Some(i));
        }
    }
    // looser match: same url + key, other params drifted
    for (i, p) in b.providers.iter().enumerate() {
        let pe = to_env(p);
        if ["ANTHROPIC_BASE_URL", p.s("key_var")]
            .iter()
            .all(|k| env_eq(&env, k, pe.get(*k)))
        {
            return Ok(Some(i));
        }
    }
    Ok(None)
}

pub fn apply(b: &mut Backend, p: &Provider) -> Result<(), String> {
    let mut settings = read_settings()?;
    let mut env = settings_env(&settings);
    for k in MANAGED_KEYS
        .iter()
        .copied()
        .chain(b.applied_keys.iter().map(String::as_str))
    {
        env.shift_remove(k);
    }
    for (k, v) in &b.shadowed {
        env.insert(k.clone(), v.clone()); // restore user values a previous provider overrode
    }
    // Only take ownership of keys we actually change, so a provider that repeats
    // a global setting (e.g. DISABLE_NONESSENTIAL_TRAFFIC=1) won't delete it later.
    let changed: Dict = to_env(p)
        .into_iter()
        .filter(|(k, v)| MANAGED_KEYS.contains(&k.as_str()) || !env_eq(&env, k, Some(v)))
        .collect();
    b.shadowed = changed
        .keys()
        .filter(|k| !MANAGED_KEYS.contains(&k.as_str()))
        .filter_map(|k| env.get(k).map(|v| (k.clone(), v.clone())))
        .collect();
    for (k, v) in &changed {
        env.insert(k.clone(), Value::String(v.clone()));
    }
    settings.insert("env".into(), Value::Object(env));
    let target = settings_file();
    backup(&target)?;
    write_json(&target, &Value::Object(settings))?;
    let mut keys: Vec<String> = changed.into_keys().collect();
    keys.sort();
    b.applied_keys = keys;
    Ok(())
}

/// settings.json ({"env": {...}}), flat env JSON, shell env scripts, or generic URL+key.
pub fn parse(text: &str) -> Info {
    let text = py_strip(text);
    let env: Dict = match load_json_obj(text) {
        Some(data) => {
            let env = match data.get("env") {
                Some(Value::Object(m)) => m.clone(),
                _ => data,
            };
            env.iter().map(|(k, v)| (k.clone(), py_str(v))).collect()
        }
        None => parse_env_script(text),
    };
    let mut out = if env.keys().any(|k| env_field(k).is_some()) {
        from_env(&env)
    } else {
        parse_generic(text)
    };
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
    fn normalizes_urls() {
        assert_eq!(
            normalize_base_url(" https://a.b/v1/messages/ "),
            "https://a.b"
        );
        assert_eq!(normalize_base_url("https://a.b/V1"), "https://a.b");
        assert_eq!(
            normalize_base_url("https://a.b/messages/v1"),
            "https://a.b/messages"
        );
        assert_eq!(normalize_base_url(""), "");
    }

    #[test]
    fn parses_settings_json() {
        let out = parse(
            r#"{"env": {"ANTHROPIC_API_KEY": "k1", "ANTHROPIC_AUTH_TOKEN": "k2", "ANTHROPIC_BASE_URL": "https://api.x.com/v1", "T": 5}}"#,
        );
        assert_eq!(
            out.keys().collect::<Vec<_>>(),
            ["key", "key_var", "base_url", "extra_env", "name"]
        );
        assert_eq!(info_str(&out, "key"), "k2");
        assert_eq!(info_str(&out, "base_url"), "https://api.x.com");
        assert_eq!(info_str(&out, "name"), "x.com");
        assert_eq!(
            out["extra_env"],
            Val::D([("T".to_string(), "5".to_string())].into_iter().collect())
        );
    }
}
