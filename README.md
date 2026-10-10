# Claude Provider · Rust

Switch between API providers for **Claude Code** and **Codex** from a terminal UI or the command line. Save your endpoints, keys, and model preferences once, then select a provider to update the tool's local configuration.

This is the **Rust implementation**, built with [Ratatui](https://ratatui.rs/) and [Crossterm](https://github.com/crossterm-rs/crossterm). The interface and command help are currently in Chinese. The Python implementation is available on the [`main` branch](https://github.com/f1sherb0y/claude-provider/tree/main).

## Features

- Manage Claude Code and Codex providers in separate tabs.
- Add, edit, duplicate, reorder, and switch providers with keyboard shortcuts.
- Import provider details from the clipboard, JSON, environment-variable snippets, or Codex TOML.
- Import existing provider settings on first launch and keep entries for official login.
- Set provider-specific models, Claude environment variables, and Codex authentication options.
- Back up existing configuration files before changes and preserve unrelated settings.

## Installation

Use a recent **stable Rust toolchain** with Cargo (the project uses Rust edition 2024), Git, and a terminal. Install Rust through [rustup](https://rustup.rs/), and install Claude Code and/or Codex separately for the tools you want to configure.

Install directly from the `rust` branch:

```sh
cargo install --git https://github.com/f1sherb0y/claude-provider.git --branch rust --locked claude-provider
ccp
```

The package is named `claude-provider` and installs the `ccp` executable. Cargo's bin directory must be on your `PATH` (normally `~/.cargo/bin`, or `%USERPROFILE%\.cargo\bin` on Windows).

### Install from source

```sh
git clone --branch rust https://github.com/f1sherb0y/claude-provider.git
cd claude-provider
cargo install --path . --locked
ccp
```

To build locally and run without installing:

```sh
cargo build --release --locked
cargo run --release --locked
```

The compiled executable is `target/release/ccp` (`target\release\ccp.exe` on Windows). Once built, it runs without a Rust or Python runtime.

### Existing Python installations

The Rust implementation uses the same `~/.claude-provider/providers.json` state file and configuration directories as the Python implementation, including the older flat Claude-only state format. Back up your state before moving between versions. Both implementations use the command name `ccp`; check your `PATH` if the wrong one starts.

## Quick start

1. Run `ccp`. On first launch, it imports the current Claude custom endpoint, if configured, and the provider tables in your Codex configuration.
2. Use **Left / Right** to select Claude Code or Codex.
3. Press **a** to add a provider, or **v** to import one from the clipboard. In the editor, paste a configuration snippet into the top box to fill the fields automatically.
4. Press **Ctrl+S** to save, then **Enter** on the provider to apply it. Saving an entry alone does not activate it.
5. Start a new Claude Code or Codex session to use the updated configuration.

To return to official login, select `Anthropic 官方` or `OpenAI 官方 (ChatGPT 登录)`. Sign in through the corresponding tool if needed; this utility does not perform login.

## Terminal UI

```sh
ccp             # Open the last-used tab
ccp --claude    # Open the Claude Code tab
ccp --codex     # Open the Codex tab (also: ccp -x)
```

| Key | Action |
| --- | --- |
| Up / Down | Select a provider |
| Left / Right | Switch between Claude Code and Codex |
| Enter | Apply the selected provider |
| 1–9 | Immediately apply the provider at that position |
| a | Add a provider |
| v | Add a provider from the clipboard |
| e | Edit the selected provider |
| c | Duplicate the selected provider |
| d, then d again within 3 seconds | Delete the selected saved entry |
| Shift+K / Shift+J | Move the selected entry up / down |
| q | Quit |

In the editor, **Ctrl+S** saves, **Esc** cancels, and **Ctrl+R** shows or hides the key.

## Command line

CLI subcommands target **Claude Code by default**, regardless of the last TUI tab. Put `--codex` / `-x` or `--claude` **before** the subcommand.

```sh
ccp list                         # List Claude providers; ls is an alias
ccp current                      # Show the active Claude provider's name
ccp use "My Claude"               # Switch by name
ccp use 2                        # Switch by the 1-based list position
ccp --codex list
ccp -x use "My Codex"
ccp rm "Old provider"             # Remove a saved entry immediately
ccp --help
ccp add --help
```

`use` and `rm` accept a list position, an exact name, or a unique case-insensitive name prefix / name-or-URL substring. Prefer an exact name or position if a partial match is ambiguous. The active provider is marked with `●` in `list`.

### Add a provider

Replace the example URLs and keys with your provider's values:

```sh
ccp add --name "My Claude" --url "https://api.example.com" --key "YOUR_API_KEY"
ccp -x add --name "My Codex" --url "https://api.example.com/v1" --key "YOUR_API_KEY" --model "YOUR_MODEL_ID" --use
```

`--use` applies the provider immediately after adding it. Without it, the command only saves the entry. Adding the same URL and key again reuses the existing entry.

| Option | Purpose |
| --- | --- |
| `TEXT` | Parse a configuration snippet; use `-` to read standard input |
| `-c`, `--clip` | Read the snippet from the clipboard |
| `--name` | Set the display name |
| `--url` | Set the base URL |
| `--key` | Set the API key or token |
| `--model` | Set an optional model |
| `--use` | Activate the resulting provider |

Explicit options override parsed values. CLI `add` requires both a URL and a key. Use the TUI for advanced fields such as Claude's key variable and extra environment variables, or Codex's provider ID, `wire_api`, and `env_key`.

Keys passed with `--key` may appear in shell history. The TUI, clipboard import, and standard input avoid putting the key in the command itself.

### Import a snippet

```sh
ccp add --clip --name "Imported Claude"
ccp -x add --clip --name "Imported Codex"
```

Read a file through standard input:

```powershell
# Windows PowerShell
Get-Content -Raw provider.json | ccp add -
Get-Content -Raw provider.toml | ccp -x add -
```

```sh
# macOS / Linux
ccp add - < provider.json
ccp -x add - < provider.toml
```

Both backends accept generic JSON:

```json
{
  "name": "Example provider",
  "url": "https://api.example.com/v1",
  "key": "YOUR_API_KEY",
  "model": "YOUR_MODEL_ID"
}
```

Claude also accepts a `settings.json` object with an `env` section, a flat environment-variable JSON object, or environment assignments:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://api.example.com",
    "ANTHROPIC_AUTH_TOKEN": "YOUR_API_KEY",
    "ANTHROPIC_MODEL": "YOUR_MODEL_ID"
  }
}
```

Codex accepts `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and optional `OPENAI_MODEL` assignments, or a TOML snippet:

```toml
model = "YOUR_MODEL_ID"

[model_providers.example]
name = "Example Codex"
base_url = "https://api.example.com/v1"
wire_api = "responses"
experimental_bearer_token = "YOUR_API_KEY"
```

Environment snippets support Bash `export KEY=VALUE`, PowerShell `$env:KEY="VALUE"`, cmd `set KEY=VALUE`, and plain `KEY=VALUE` lines. A pasted Codex TOML snippet imports the first provider table; first-launch discovery reads all existing tables.

## Configuration behavior

| Tool | What switching changes |
| --- | --- |
| Claude Code | Updates provider-related variables in `settings.json`'s `env` object. Clears managed endpoint, key, and model overrides from the previous provider, preserves unrelated settings, and restores unrelated environment values temporarily overridden by a saved provider. |
| Codex | Updates `model_provider` and the selected `[model_providers.<id>]` table in `config.toml`, preserving unrelated TOML settings and comments. A nonempty provider model updates the top-level `model`; an empty model leaves it unchanged. |

Claude defaults to `ANTHROPIC_AUTH_TOKEN`; choose `ANTHROPIC_API_KEY` in the editor if your setup needs it. Claude URLs have trailing `/v1`, `/messages`, or `/v1/messages` removed. Codex URLs have trailing `/responses` or `/chat/completions` removed, retaining an API-root `/v1` when present.

Codex defaults to `wire_api = "responses"`; the editor also offers `chat`. Choose a value supported by your installed Codex version and endpoint. By default, the key is written to `experimental_bearer_token`. Setting `env_key` instead writes the variable name to the provider table and a nonempty key to Codex's `.env` file.

Selecting the built-in official Claude entry clears managed provider overrides. Selecting the official Codex entry removes the top-level `model_provider` override and keeps the current model. Existing login credentials are left in place.

Deleting an entry removes it from this utility's saved list. It does not undo the active configuration or remove previously written credentials or Codex provider tables; switch to another provider first if you want to stop using it.

### Files and backups

`~` means your user home directory, such as `C:\Users\you` on Windows.

| File | Purpose | Directory override |
| --- | --- | --- |
| `~/.claude-provider/providers.json` | Saved providers, keys, ordering, and last-used tab | `CLAUDE_PROVIDER_HOME` |
| `~/.claude-provider/backups/` | Timestamped copies of configuration files before writes | `CLAUDE_PROVIDER_HOME` |
| `~/.claude/settings.json` | Claude Code configuration | `CLAUDE_CONFIG_DIR` |
| `~/.codex/config.toml` | Codex configuration | `CODEX_HOME` |
| `~/.codex/.env` | Codex keys when a provider uses `env_key` | `CODEX_HOME` |

Set directory overrides before launching the utility. `CLAUDE_PROVIDER_HOME` relocates its saved state and backups; the tool configuration directories use their own overrides.

Existing target files are backed up before modification, with the newest **20 backups per filename pattern** retained. To restore one, close the utility and copy the chosen backup over its original file, then start a new tool session.

**Keys are stored in plain text.** Masking in the UI is only for display. Saved providers, target configuration files, and backups can contain credentials; keep them out of version control and shared folders.

## Troubleshooting

- **Cargo reports an unsupported edition or compiler version:** Update the stable toolchain with `rustup update stable` and build with that toolchain.
- **`ccp` is not found:** Add Cargo's bin directory to your `PATH` and reopen your terminal, or run the executable from `target/release` after building locally.
- **Clipboard import does not work:** Paste the snippet into the editor's top box or use standard input with `add -`.
- **A switch is not reflected in your tool:** Start a new session and check that both the utility and the tool use the same configuration directory. Check for overrides in the shell or other tool configuration.
- **The current provider is unknown:** The active configuration may not match a saved entry. Import it explicitly or reapply a saved provider. Initial discovery runs when backend state is first created; it does not continuously import later edits.

## Development

```sh
cargo test --locked --all-targets
cargo build --release --locked
cargo run --locked -- --help
```

`Cargo.lock` is committed so builds can use the recorded dependency versions. The source is organized into CLI (`src/cli.rs`), shared provider/state handling (`src/backend.rs`, `src/store.rs`, and `src/common.rs`), tool backends (`src/claude.rs` and `src/codex.rs`), and terminal UI (`src/tui/`).
