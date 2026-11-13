# Claude Provider · Go

Switch between API providers for **Claude Code** and **Codex** from a terminal UI or the command line. Save your endpoints, keys, and model preferences once, then select a provider to update the tool's local configuration.

This is the **Go implementation**, built on [tcell](https://github.com/gdamore/tcell). The interface and command help are in Chinese. The earlier implementations live on the [`rust`](https://github.com/f1sherb0y/claude-provider/tree/rust) and [`python`](https://github.com/f1sherb0y/claude-provider/tree/python) branches; all three share the same state file.

## Features

- Manage Claude Code and Codex providers in separate tabs.
- Add, edit, duplicate, reorder, and switch providers with keyboard shortcuts or the mouse.
- Import provider details from the clipboard, JSON, environment-variable snippets, or Codex TOML.
- Import existing provider settings on first launch and keep entries for official login.
- Give a provider its own environment variables (proxy, CA bundle, ...), for both tools.
- Keep a separate Codex login (`auth.json`) per login-type provider, so a local-proxy gateway and the real ChatGPT login can coexist.
- Back up configuration files before changing them and leave unrelated settings, comments, and formatting alone.

## Installation

Requires Go 1.24 or newer.

```sh
go install github.com/f1sherb0y/claude-provider/cmd/ccp@latest
ccp
```

This installs the `ccp` executable into `$(go env GOPATH)/bin`, which must be on your `PATH`.

### From source

```sh
git clone https://github.com/f1sherb0y/claude-provider.git
cd claude-provider
go build -o ccp ./cmd/ccp      # ccp.exe on Windows
```

The result is a single self-contained executable.

### Coming from the Rust or Python version

The state file (`~/.claude-provider/providers.json`) and configuration directories are the same. This version adds fields to Codex providers that the older versions drop when they save, so use one implementation at a time; all of them install a command named `ccp`, so check which one is first on your `PATH`.

## Quick start

1. Run `ccp`. On first launch, it imports the current Claude custom endpoint, if configured, and the provider tables in your Codex configuration.
2. Use **Left / Right** to select Claude Code or Codex.
3. Press **a** to add a provider, or **v** to import one from the clipboard. In the editor, paste a configuration snippet into the top box to fill the fields automatically.
4. Press **Ctrl+S** to save, then **Enter** on the provider to apply it. Saving an entry alone does not activate it.
5. Start a new Claude Code or Codex session to use the updated configuration.

To return to official login, select `Anthropic 官方` or `OpenAI 官方 (ChatGPT 登录)`.

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

In the editor, **Ctrl+S** saves, **Esc** cancels, **Tab** moves between fields, and **Ctrl+R** shows or hides the key.

## Command line

CLI subcommands target **Claude Code by default**, regardless of the last TUI tab. Add `--codex` / `-x` for Codex.

```sh
ccp list                         # List Claude providers; ls is an alias
ccp current                      # Show the active Claude provider's name
ccp use "My Claude"               # Switch by name
ccp use 2                        # Switch by the 1-based list position
ccp -x list
ccp -x use "My Codex"
ccp rm "Old provider"             # Remove a saved entry immediately
ccp --help
ccp add --help
```

`use` and `rm` accept a list position, an exact name, or a unique case-insensitive name prefix / name-or-URL substring. The active provider is marked with `●` in `list`.

### Add a provider

```sh
ccp add --name "My Claude" --url "https://api.example.com" --key "YOUR_API_KEY"
ccp -x add --name "My Codex" --url "https://api.example.com/v1" --key "YOUR_API_KEY" --model "YOUR_MODEL_ID" --use
```

| Option | Purpose |
| --- | --- |
| `TEXT` | Parse a configuration snippet; use `-` to read standard input |
| `-c`, `--clip` | Read the snippet from the clipboard |
| `--name` | Set the display name |
| `--url` | Set the base URL |
| `--key` | Set the API key or token |
| `--model` | Set an optional model |
| `--env KEY=VALUE` | Add an environment variable the provider needs (repeatable) |
| `--use` | Activate the resulting provider |

Explicit options override parsed values. An endpoint needs both a URL and a key. A provider given only `--env` values is a *login-type* provider: the tool keeps using its built-in endpoint and sign-in, with those variables set. Adding a provider identical to a saved one reuses the existing entry.

Keys passed with `--key` may appear in shell history. The TUI, clipboard import, and standard input avoid putting the key in the command itself.

### Import a snippet

```sh
ccp add --clip --name "Imported Claude"
ccp -x add - < provider.toml
```

Both backends accept generic JSON (`{"name": ..., "url": ..., "key": ..., "model": ...}`). Claude also accepts a `settings.json` object with an `env` section, a flat environment-variable JSON object, or environment assignments. Codex accepts `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and optional `OPENAI_MODEL` assignments, or a TOML snippet:

```toml
model = "YOUR_MODEL_ID"

[model_providers.example]
name = "Example Codex"
base_url = "https://api.example.com/v1"
wire_api = "responses"
experimental_bearer_token = "YOUR_API_KEY"
```

Environment snippets support Bash `export KEY=VALUE`, PowerShell `$env:KEY="VALUE"`, cmd `set KEY=VALUE`, and plain `KEY=VALUE` lines.

## Configuration behavior

### Claude Code

Switching updates provider-related variables in `settings.json`'s `env` object: it clears the endpoint, key, and model overrides of the previous provider, sets the new ones, and restores any of your own values that the previous provider's extra variables had overridden. The rest of the file is preserved.

Claude defaults to `ANTHROPIC_AUTH_TOKEN`; choose `ANTHROPIC_API_KEY` in the editor if your setup needs it. URLs have a trailing `/v1`, `/messages`, or `/v1/messages` removed.

### Codex

Switching touches up to three files in `~/.codex`:

| File | What changes |
| --- | --- |
| `config.toml` | `model_provider` is set to the provider's ID (`openai` for a login-type provider), and an endpoint's `[model_providers.<id>]` table is created or updated in place. A nonempty provider model sets the top-level `model`. Edits are made on the text of the file: comments, ordering, and every other setting stay as they are, and the result is re-parsed and checked before it is written. |
| `.env` | The provider's extra variables are written into a `# BEGIN ccp (managed)` block. Variables that belonged to another saved provider are removed — including identical lines that provider's own installer wrote — and values of yours that they had overridden are restored. A provider with `env_key` gets its key here instead of in `config.toml`. |
| `auth.json` | Only for login-type providers; see below. |

Codex URLs have a trailing `/responses` or `/chat/completions` removed. `wire_api` defaults to `responses`. Tables of providers you are not using are left in `config.toml`.

#### Login-type providers and `auth.json`

A provider with an empty Base URL uses Codex's built-in `openai` provider and whatever `auth.json` signs in as. There can be several: the official ChatGPT login, and for example a gateway that installs a local proxy plus a placeholder login. They are told apart by their extra variables (the entry whose variables are all present in `.env`, most specific first, is the active one).

Each of them keeps its own copy of `auth.json` in `~/.claude-provider/codex-auth/<id>.json`:

- Before switching, the `auth.json` in place is saved to the login it belongs to (matched by account), because Codex refreshes tokens in that file.
- Switching to a login puts its saved `auth.json` back.
- Switching to a login that has never been saved while another login's file is in place moves that file away, so Codex asks you to run `codex login` rather than use the wrong credentials.
- Switching to an endpoint leaves `auth.json` alone.

This utility never performs a login itself.

### Deleting

Deleting an entry removes it from the saved list. It does not undo the active configuration or remove credentials, provider tables, or login snapshots already written; switch to another provider first if you want to stop using it.

## Files and backups

`~` means your user home directory, such as `C:\Users\you` on Windows.

| File | Purpose | Directory override |
| --- | --- | --- |
| `~/.claude-provider/providers.json` | Saved providers, keys, ordering, and last-used tab | `CLAUDE_PROVIDER_HOME` |
| `~/.claude-provider/codex-auth/` | One saved `auth.json` per Codex login-type provider | `CLAUDE_PROVIDER_HOME` |
| `~/.claude-provider/backups/` | Timestamped copies of files before they are changed | `CLAUDE_PROVIDER_HOME` |
| `~/.claude/settings.json` | Claude Code configuration | `CLAUDE_CONFIG_DIR` |
| `~/.codex/config.toml`, `.env`, `auth.json` | Codex configuration and login | `CODEX_HOME` |

Files are backed up before modification, with the newest **20 backups per file** retained, and written atomically.

**Keys are stored in plain text.** Masking in the UI is only for display. Saved providers, login snapshots, target configuration files, and backups contain credentials; keep them out of version control and shared folders.

## Troubleshooting

- **`ccp` is not found, or the wrong version starts:** Put `$(go env GOPATH)/bin` on your `PATH`, ahead of other installations of `ccp`.
- **Clipboard import does not work:** Paste the snippet into the editor's top box or use standard input with `add -`.
- **A switch is not reflected in your tool:** Start a new session and check that both the utility and the tool use the same configuration directory.
- **The current provider is unknown:** The active configuration does not match a saved entry. Reapply a saved provider. Initial discovery runs only when the state file is first created.
- **Codex says "model_providers … 是内联表":** `config.toml` defines providers as an inline table or dotted keys, which cannot be extended safely. Rewrite it as `[model_providers.<id>]` tables.

## Development

```sh
go test ./...
go vet ./...
go build -o ccp ./cmd/ccp
```

Everything is one `main` package in `cmd/ccp`: CLI (`cli.go`), provider/state handling (`backend.go`, `store.go`, `common.go`), tool backends (`claude.go`; `codex.go` with `tomledit.go` and `dotenv.go`), and the terminal UI (`tui*.go`). `ccp __render 100x30 [keys...]` prints the screen a scripted session ends on, which is how the UI is tested without a terminal.
