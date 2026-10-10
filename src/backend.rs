//! Provider model and the backend-independent parts of a backend.
use indexmap::IndexMap;
use serde_json::{Map, Value};

use crate::common::{Dict, Info, py_str};
use crate::{claude, codex};

#[derive(Clone, Debug, PartialEq)]
pub enum Val {
    S(String),
    D(Dict),
}

impl Val {
    pub fn is_empty(&self) -> bool {
        matches!(self, Val::S(s) if s.is_empty())
    }
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Kind {
    Text,
    Password,
    Select,
    Dict,
}

/// One editable provider attribute, rendered by the TUI form.
pub struct Field {
    pub name: &'static str,
    pub label: &'static str,
    pub kind: Kind,
    pub options: &'static [&'static str],
    pub placeholder: &'static str,
    pub default: &'static str,
}

impl Field {
    pub const fn text(name: &'static str, label: &'static str, placeholder: &'static str) -> Field {
        Field {
            name,
            label,
            kind: Kind::Text,
            options: &[],
            placeholder,
            default: "",
        }
    }
}

/// A provider: every field of its backend, in declaration order.
#[derive(Clone, Debug, PartialEq)]
pub struct Provider(pub IndexMap<String, Val>);

impl Provider {
    pub fn new(fields: &[Field]) -> Provider {
        Provider(
            fields
                .iter()
                .map(|f| {
                    let v = if f.kind == Kind::Dict {
                        Val::D(Dict::new())
                    } else {
                        Val::S(f.default.into())
                    };
                    (f.name.to_string(), v)
                })
                .collect(),
        )
    }
    pub fn s(&self, key: &str) -> &str {
        match self.0.get(key) {
            Some(Val::S(s)) => s,
            _ => "",
        }
    }
    pub fn dict(&self, key: &str) -> Dict {
        match self.0.get(key) {
            Some(Val::D(d)) => d.clone(),
            _ => Dict::new(),
        }
    }
    pub fn set(&mut self, key: &str, v: impl Into<String>) {
        self.0.insert(key.to_string(), Val::S(v.into()));
    }
    pub fn name(&self) -> &str {
        self.s("name")
    }
    pub fn base_url(&self) -> &str {
        self.s("base_url")
    }
    pub fn key(&self) -> &str {
        self.s("key")
    }
    pub fn model(&self) -> &str {
        self.s("model")
    }

    fn from_json(fields: &[Field], obj: &Map<String, Value>) -> Provider {
        let mut p = Provider::new(fields);
        for f in fields {
            let Some(v) = obj.get(f.name) else { continue };
            let val = match (f.kind, v) {
                (Kind::Dict, Value::Object(m)) => {
                    Val::D(m.iter().map(|(k, v)| (k.clone(), py_str(v))).collect())
                }
                (Kind::Dict, _) => Val::D(Dict::new()),
                (_, Value::Null) => Val::S(String::new()),
                (_, v) => Val::S(py_str(v)),
            };
            p.0.insert(f.name.to_string(), val);
        }
        p
    }

    fn to_json(&self) -> Value {
        Value::Object(
            self.0
                .iter()
                .map(|(k, v)| {
                    let v = match v {
                        Val::S(s) => Value::String(s.clone()),
                        Val::D(d) => Value::Object(
                            d.iter()
                                .map(|(k, v)| (k.clone(), Value::String(v.clone())))
                                .collect(),
                        ),
                    };
                    (k.clone(), v)
                })
                .collect(),
        )
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Tool {
    Claude,
    Codex,
}

impl Tool {
    pub const ALL: [Tool; 2] = [Tool::Claude, Tool::Codex];
    pub fn id(self) -> &'static str {
        match self {
            Tool::Claude => "claude",
            Tool::Codex => "codex",
        }
    }
    pub fn from_id(id: &str) -> Option<Tool> {
        Tool::ALL.into_iter().find(|t| t.id() == id)
    }
}

/// A tool whose provider config we manage (Claude Code, Codex).
pub struct Backend {
    pub tool: Tool,
    pub providers: Vec<Provider>,
    // Claude bookkeeping
    pub applied_keys: Vec<String>,
    pub shadowed: Map<String, Value>,
}

impl Backend {
    pub fn new(tool: Tool, state: Option<&Value>) -> Result<Backend, String> {
        let mut b = Backend {
            tool,
            providers: vec![],
            applied_keys: vec![],
            shadowed: Map::new(),
        };
        match state {
            Some(Value::Object(state)) => b.load_state(state),
            Some(Value::Null) | None => b.bootstrap()?,
            Some(_) => b.load_state(&Map::new()),
        }
        Ok(b)
    }

    pub fn label(&self) -> &'static str {
        match self.tool {
            Tool::Claude => "Claude Code",
            Tool::Codex => "Codex",
        }
    }
    pub fn fields(&self) -> &'static [Field] {
        match self.tool {
            Tool::Claude => claude::FIELDS,
            Tool::Codex => codex::FIELDS,
        }
    }
    pub fn paste_hint(&self) -> &'static str {
        match self.tool {
            Tool::Claude => claude::PASTE_HINT,
            Tool::Codex => codex::PASTE_HINT,
        }
    }
    pub fn target(&self) -> std::path::PathBuf {
        match self.tool {
            Tool::Claude => claude::settings_file(),
            Tool::Codex => codex::config_file(),
        }
    }
    pub fn blank(&self) -> Provider {
        Provider::new(self.fields())
    }

    // -- persistence
    fn load_state(&mut self, state: &Map<String, Value>) {
        let fields = self.fields();
        self.providers = match state.get("providers") {
            Some(Value::Array(a)) => a
                .iter()
                .filter_map(|p| p.as_object())
                .map(|o| Provider::from_json(fields, o))
                .collect(),
            _ => vec![],
        };
        if self.tool == Tool::Claude {
            if let Some(Value::Array(a)) = state.get("applied_keys") {
                self.applied_keys = a.iter().map(py_str).collect();
            }
            if let Some(Value::Object(m)) = state.get("shadowed") {
                self.shadowed = m.clone();
            }
        }
    }

    pub fn dump_state(&self) -> Value {
        let mut m = Map::new();
        m.insert(
            "providers".into(),
            Value::Array(self.providers.iter().map(Provider::to_json).collect()),
        );
        if self.tool == Tool::Claude {
            m.insert(
                "applied_keys".into(),
                Value::Array(
                    self.applied_keys
                        .iter()
                        .cloned()
                        .map(Value::String)
                        .collect(),
                ),
            );
            m.insert("shadowed".into(), Value::Object(self.shadowed.clone()));
        }
        Value::Object(m)
    }

    fn bootstrap(&mut self) -> Result<(), String> {
        match self.tool {
            Tool::Claude => claude::bootstrap(self),
            Tool::Codex => codex::bootstrap(self),
        }
    }

    // -- tool specific
    pub fn active(&self) -> Result<Option<usize>, String> {
        match self.tool {
            Tool::Claude => claude::active(self),
            Tool::Codex => codex::active(self),
        }
    }

    /// Write the provider into the tool's config (caller saves the store).
    pub fn apply(&mut self, idx: usize) -> Result<(), String> {
        let p = self.providers[idx].clone();
        match self.tool {
            Tool::Claude => claude::apply(self, &p),
            Tool::Codex => codex::apply(&p),
        }
    }

    pub fn parse(&self, text: &str) -> Info {
        match self.tool {
            Tool::Claude => claude::parse(text),
            Tool::Codex => codex::parse(text),
        }
    }

    /// Clean up a provider coming out of the editor before storing it.
    pub fn normalize(&self, mut p: Provider) -> Provider {
        match self.tool {
            Tool::Claude => {
                let url = claude::normalize_base_url(p.base_url());
                p.set("base_url", url);
            }
            Tool::Codex => {
                let url = codex::normalize_base_url(p.base_url());
                p.set("base_url", url);
                if !p.base_url().is_empty() && p.s("id").is_empty() {
                    let base = codex::slug(p.name());
                    let mut pid = base.clone();
                    let mut i = 2;
                    while self.providers.iter().any(|q| q.s("id") == pid) {
                        pid = format!("{base}_{i}");
                        i += 1;
                    }
                    p.set("id", pid);
                }
            }
        }
        p
    }

    // -- lookup
    pub fn find(&self, query: &str) -> Option<usize> {
        if !query.is_empty() && query.bytes().all(|b| b.is_ascii_digit()) {
            if let Ok(n) = query.parse::<usize>() {
                if (1..=self.providers.len()).contains(&n) {
                    return Some(n - 1);
                }
            }
        }
        let q = query.to_lowercase();
        let tiers: [&dyn Fn(&Provider) -> bool; 4] = [
            &|p| p.name() == query,
            &|p| p.name().to_lowercase() == q,
            &|p| p.name().to_lowercase().starts_with(&q),
            &|p| p.name().to_lowercase().contains(&q) || p.base_url().to_lowercase().contains(&q),
        ];
        for m in tiers {
            let hits: Vec<usize> = (0..self.providers.len())
                .filter(|&i| m(&self.providers[i]))
                .collect();
            if hits.len() == 1 {
                return Some(hits[0]);
            }
        }
        None
    }

    pub fn find_same(&self, base_url: &str, key: &str) -> Option<usize> {
        let url = base_url.trim_end_matches('/');
        self.providers
            .iter()
            .position(|p| p.base_url().trim_end_matches('/') == url && p.key() == key)
    }

    pub fn unique_name(&self, name: &str, exclude: Option<usize>) -> String {
        let taken = |n: &str| {
            self.providers
                .iter()
                .enumerate()
                .any(|(i, p)| Some(i) != exclude && p.name() == n)
        };
        if !taken(name) {
            return name.to_string();
        }
        let mut i = 2;
        while taken(&format!("{name} ({i})")) {
            i += 1;
        }
        format!("{name} ({i})")
    }

    // -- mutations (caller saves)
    pub fn upsert(&mut self, provider: Provider, original: Option<usize>) -> usize {
        let provider = self.normalize(provider);
        match original {
            Some(i) if i < self.providers.len() => {
                self.providers[i] = provider;
                i
            }
            _ => {
                self.providers.push(provider);
                self.providers.len() - 1
            }
        }
    }

    pub fn remove(&mut self, idx: usize) {
        self.providers.remove(idx);
    }

    pub fn move_by(&mut self, idx: usize, delta: isize) -> usize {
        let j = (idx as isize + delta).clamp(0, self.providers.len() as isize - 1) as usize;
        let p = self.providers.remove(idx);
        self.providers.insert(j, p);
        j
    }

    /// Build a new provider from parse() output.
    pub fn from_info(&self, info: &Info) -> Provider {
        let mut p = self.blank();
        for (k, v) in info {
            if p.0.contains_key(k) && !v.is_empty() {
                p.0.insert(k.clone(), v.clone());
            }
        }
        let name = if p.name().is_empty() {
            "provider".to_string()
        } else {
            p.name().to_string()
        };
        p.set("name", self.unique_name(&name, None));
        self.normalize(p)
    }
}
