//! Command line entry point. No subcommand -> TUI.
use std::io::Read;

use clap::{ArgGroup, Parser, Subcommand};

use crate::backend::{Tool, Val};
use crate::common::{Info, mask, read_clipboard, set_info};
use crate::store::Store;

#[derive(Parser)]
#[command(
    name = "claude-provider",
    about = "快速切换 Claude Code / Codex 的 API provider（不带子命令启动 TUI）"
)]
#[command(group(ArgGroup::new("tool").args(["codex", "claude"])))]
struct Args {
    /// 操作 Codex
    #[arg(short = 'x', long)]
    codex: bool,
    /// 操作 Claude Code（默认）
    #[arg(long)]
    claude: bool,
    #[command(subcommand)]
    cmd: Option<Cmd>,
}

#[derive(Subcommand)]
enum Cmd {
    /// 列出所有 provider
    #[command(visible_alias = "ls")]
    List,
    /// 显示当前 provider
    Current,
    /// 切换 provider（名称/序号/模糊匹配）
    Use { name: String },
    /// 新增 provider；支持 JSON / settings.json / TOML / export / $env: 文本
    Add {
        /// 要识别的文本；- 表示从 stdin 读取
        text: Option<String>,
        /// 从剪贴板读取
        #[arg(short = 'c', long)]
        clip: bool,
        #[arg(long)]
        name: Option<String>,
        #[arg(long)]
        url: Option<String>,
        #[arg(long)]
        key: Option<String>,
        #[arg(long, default_value = "")]
        model: String,
        /// 添加后立即切换
        #[arg(long = "use")]
        use_: bool,
    },
    /// 删除 provider
    Rm { name: String },
}

fn print_list(store: &Store, tool: Tool) -> Result<(), String> {
    let b = store.get(tool);
    let active = b.active()?;
    for (i, p) in b.providers.iter().enumerate() {
        let mark = if active == Some(i) { "●" } else { " " };
        let url = if p.base_url().is_empty() {
            "(官方登录)"
        } else {
            p.base_url()
        };
        println!(
            "{mark} {}. {:<24} {:<40} {}  {}",
            i + 1,
            p.name(),
            url,
            mask(p.key()),
            p.model()
        );
    }
    Ok(())
}

pub fn main() -> i32 {
    let args = Args::parse();
    match run(args) {
        Ok(()) => 0,
        Err(msg) => {
            eprintln!("{msg}");
            1
        }
    }
}

fn run(args: Args) -> Result<(), String> {
    let mut store = Store::load()?;
    let tool = if args.codex {
        Some(Tool::Codex)
    } else if args.claude {
        Some(Tool::Claude)
    } else {
        None
    };
    let Some(cmd) = args.cmd else {
        return crate::tui::run(store, tool);
    };
    let tool = tool.unwrap_or(Tool::Claude);
    match cmd {
        Cmd::List => print_list(&store, tool)?,
        Cmd::Current => {
            let b = store.get(tool);
            match b.active()? {
                Some(i) => println!("{}", b.providers[i].name()),
                None => println!("(未匹配到已保存的 provider)"),
            }
        }
        Cmd::Use { name } => {
            let i = store
                .get(tool)
                .find(&name)
                .ok_or_else(|| format!("找不到 provider: {name}"))?;
            store.apply(tool, i)?;
            let b = store.get(tool);
            println!(
                "✔ {} 已切换到 {}  →  {}（新开会话生效）",
                b.label(),
                b.providers[i].name(),
                b.target().display()
            );
        }
        Cmd::Add {
            text,
            clip,
            name,
            url,
            key,
            model,
            use_,
        } => {
            let text = if clip {
                read_clipboard()
            } else if text.as_deref() == Some("-") {
                let mut s = String::new();
                std::io::stdin()
                    .read_to_string(&mut s)
                    .map_err(|e| e.to_string())?;
                s
            } else {
                text.unwrap_or_default()
            };
            let b = store.get(tool);
            let mut info: Info = if text.is_empty() {
                Info::new()
            } else {
                b.parse(&text)
            };
            for (k, v) in [
                ("name", name),
                ("base_url", url),
                ("key", key),
                ("model", Some(model)),
            ] {
                if let Some(v) = v.filter(|v| !v.is_empty()) {
                    set_info(&mut info, k, v);
                }
            }
            let has = |k: &str| {
                info.get(k)
                    .is_some_and(|v| !matches!(v, Val::S(s) if s.is_empty()))
            };
            if !has("base_url") || !has("key") {
                return Err(
                    "没有识别到 url 和 key（用 --url/--key，或者传入 JSON / TOML / export 文本）"
                        .into(),
                );
            }
            let new = b.from_info(&info);
            let i = match b.find_same(new.base_url(), new.key()) {
                Some(i) => {
                    println!("已存在相同的 provider: {}", b.providers[i].name());
                    i
                }
                None => {
                    let label = b.label();
                    let i = store.upsert(tool, new, None)?;
                    println!(
                        "✔ 已添加 {} 到 {label}",
                        store.get(tool).providers[i].name()
                    );
                    i
                }
            };
            if use_ {
                store.apply(tool, i)?;
                println!("✔ 已切换到 {}", store.get(tool).providers[i].name());
            }
        }
        Cmd::Rm { name } => {
            let i = store
                .get(tool)
                .find(&name)
                .ok_or_else(|| format!("找不到 provider: {name}"))?;
            let pname = store.get(tool).providers[i].name().to_string();
            store.remove(tool, i)?;
            println!("✔ 已删除 {pname}");
        }
    }
    Ok(())
}
