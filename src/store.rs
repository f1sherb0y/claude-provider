//! Loads/saves providers.json and wires up the backends.
use serde_json::{Map, Value};

use crate::backend::{Backend, Provider, Tool};
use crate::common::{data_file, write_json};

pub struct Store {
    pub last_tab: String,
    pub backends: Vec<Backend>,
}

impl Store {
    pub fn load() -> Result<Store, String> {
        let path = data_file();
        let mut data = Map::new();
        if path.exists() {
            let text =
                std::fs::read_to_string(&path).map_err(|e| format!("{}: {e}", path.display()))?;
            match serde_json::from_str::<Value>(&text)
                .map_err(|e| format!("{}: {e}", path.display()))?
            {
                Value::Object(m) => data = m,
                _ => return Err(format!("{}: 不是 JSON 对象", path.display())),
            }
        }
        if data.contains_key("providers") {
            // v0.1 format: flat Claude-only
            let mut m = Map::new();
            m.insert("claude".into(), Value::Object(data));
            data = m;
        }
        let last_tab = match data.get("last_tab") {
            Some(Value::String(s)) => s.clone(),
            _ => "claude".into(),
        };
        let backends = Tool::ALL
            .iter()
            .map(|&t| Backend::new(t, data.get(t.id())))
            .collect::<Result<Vec<_>, _>>()?;
        let store = Store { last_tab, backends };
        if Tool::ALL.iter().any(|t| !data.contains_key(t.id())) {
            store.save()?;
        }
        Ok(store)
    }

    pub fn save(&self) -> Result<(), String> {
        let mut m = Map::new();
        m.insert("last_tab".into(), Value::String(self.last_tab.clone()));
        for b in &self.backends {
            m.insert(b.tool.id().into(), b.dump_state());
        }
        write_json(&data_file(), &Value::Object(m))
    }

    pub fn get(&self, tool: Tool) -> &Backend {
        self.backends
            .iter()
            .find(|b| b.tool == tool)
            .expect("backend")
    }
    pub fn get_mut(&mut self, tool: Tool) -> &mut Backend {
        self.backends
            .iter_mut()
            .find(|b| b.tool == tool)
            .expect("backend")
    }

    // -- mutations that persist
    pub fn apply(&mut self, tool: Tool, idx: usize) -> Result<(), String> {
        self.get_mut(tool).apply(idx)?;
        if tool == Tool::Claude {
            self.save()?; // applied_keys / shadowed changed
        }
        Ok(())
    }
    pub fn upsert(
        &mut self,
        tool: Tool,
        p: Provider,
        original: Option<usize>,
    ) -> Result<usize, String> {
        let i = self.get_mut(tool).upsert(p, original);
        self.save()?;
        Ok(i)
    }
    pub fn remove(&mut self, tool: Tool, idx: usize) -> Result<(), String> {
        self.get_mut(tool).remove(idx);
        self.save()
    }
    pub fn move_by(&mut self, tool: Tool, idx: usize, delta: isize) -> Result<usize, String> {
        let j = self.get_mut(tool).move_by(idx, delta);
        self.save()?;
        Ok(j)
    }
}
