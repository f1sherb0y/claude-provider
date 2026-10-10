//! Mirror of /tmp/tk/t.py: apply 3 providers to several config.toml shapes.
use claude_provider::backend::{Provider, Tool};
use claude_provider::codex;

fn main() {
    let out = std::env::args().nth(1).expect("out dir");
    let cases: &[(&str, &str)] = &[
        ("empty", ""),
        (
            "orig",
            "# my codex config\nmodel = \"gpt-5\"\n\n[features]\nshell_tool = true\n",
        ),
        (
            "withmp",
            "model_provider = \"old\" # current\nmodel = \"gpt-5\"\n\n[model_providers.old]\nname = \"Old\"\nbase_url = \"https://old/v1\"\nwire_api = \"chat\"\nexperimental_bearer_token = \"sk-old\"\nheaders = { a = \"b\" }\n",
        ),
        ("nomodel", "[features]\nshell_tool = true\n"),
        (
            "inline",
            "model_providers = { x = { name = \"X\", base_url = \"u\" } }\n",
        ),
        ("comment_only", "# just a comment\n"),
        ("kv_only", "approval = \"never\"\n"),
    ];
    let mk = |kv: &[(&str, &str)]| {
        let mut p = Provider::new(codex::FIELDS);
        for (k, v) in kv {
            p.set(k, *v);
        }
        p
    };
    let provs = [
        mk(&[
            ("name", "New \"q\" 全球"),
            ("id", "nowcoding"),
            ("base_url", "https://nowcoding.ai/v1"),
            ("key", "sk-abc\\x"),
            ("model", "gpt-5-codex"),
        ]),
        mk(&[
            ("name", "Old2"),
            ("id", "old"),
            ("base_url", "https://old2/v1"),
            ("key", "k2"),
            ("wire_api", "responses"),
            ("env_key", "OLD_KEY"),
        ]),
        mk(&[("name", "OpenAI 官方")]),
    ];
    let _ = Tool::Codex;
    for (cname, text) in cases {
        for (i, p) in provs.iter().enumerate() {
            let d = tempfile::tempdir().unwrap();
            unsafe {
                std::env::set_var("CODEX_HOME", d.path());
                std::env::set_var("CLAUDE_PROVIDER_HOME", d.path().join("h"));
            }
            if !text.is_empty() {
                std::fs::write(d.path().join("config.toml"), text).unwrap();
            }
            let res = match codex::apply(p) {
                Ok(()) => std::fs::read_to_string(d.path().join("config.toml")).unwrap_or_default(),
                Err(e) => format!("ERROR {e}"),
            };
            let env = std::fs::read_to_string(d.path().join(".env")).ok();
            let body = res + &env.map(|e| format!("\n---ENV---\n{e}")).unwrap_or_default();
            std::fs::write(format!("{out}/{cname}.{i}.toml"), body).unwrap();
        }
    }
}
