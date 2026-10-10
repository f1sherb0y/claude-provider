//! Render scripted TUI scenarios to JSON grids (for comparing with the Textual goldens).
//! usage: render <scenarios.json> <fixtures dir> <out dir> [names...]
use std::path::Path;

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for e in std::fs::read_dir(from).unwrap() {
        let e = e.unwrap();
        let dst = to.join(e.file_name());
        if e.file_type().unwrap().is_dir() {
            copy_dir(&e.path(), &dst);
        } else {
            std::fs::copy(e.path(), dst).unwrap();
        }
    }
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let scenarios: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(&args[1]).unwrap()).unwrap();
    let (fixtures, out) = (Path::new(&args[2]), Path::new(&args[3]));
    let only = &args[4..];
    std::fs::create_dir_all(out).unwrap();
    let work = std::env::temp_dir().join(format!("ccp-render-{}", std::process::id()));
    for sc in scenarios.as_array().unwrap() {
        let name = sc["name"].as_str().unwrap();
        if !only.is_empty() && !only.iter().any(|o| o == name) {
            continue;
        }
        let _ = std::fs::remove_dir_all(&work);
        copy_dir(fixtures, &work);
        unsafe {
            std::env::set_var("CLAUDE_PROVIDER_HOME", work.join("home"));
            std::env::set_var("CLAUDE_CONFIG_DIR", work.join("claude"));
            std::env::set_var("CODEX_HOME", work.join("codex"));
        }
        let store = claude_provider::store::Store::load().unwrap();
        let tool = sc["tab"]
            .as_str()
            .and_then(claude_provider::backend::Tool::from_id);
        let keys: Vec<String> = sc["keys"]
            .as_array()
            .map(|a| a.iter().map(|k| k.as_str().unwrap().to_string()).collect())
            .unwrap_or_default();
        let w = sc["w"].as_u64().unwrap() as usize;
        let h = sc["h"].as_u64().unwrap() as usize;
        let canvas = claude_provider::tui::headless(store, tool, w, h, &keys);
        std::fs::write(
            out.join(format!("{name}.json")),
            claude_provider::tui::dump_json(&canvas),
        )
        .unwrap();
    }
    let _ = std::fs::remove_dir_all(&work);
}
