"""Shared helpers and the Backend base class."""
from __future__ import annotations

import dataclasses
import json
import os
import re
import shutil
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlparse

CONFIG_DIR = Path(os.environ.get("CLAUDE_PROVIDER_HOME") or Path.home() / ".claude-provider")
DATA_FILE = CONFIG_DIR / "providers.json"
BACKUP_DIR = CONFIG_DIR / "backups"
MAX_BACKUPS = 20


# ---------------------------------------------------------------- small helpers

def mask(secret: str) -> str:
    if not secret:
        return "-"
    if len(secret) <= 12:
        return secret[:3] + "…"
    return f"{secret[:7]}…{secret[-4:]}"


def name_from_url(url: str) -> str:
    host = urlparse(url if "://" in url else "https://" + url).hostname or url
    return host.removeprefix("www.").removeprefix("api.")


def atomic_write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".cp-tmp")
    with open(tmp, "w", encoding="utf-8", newline="\n") as f:
        f.write(text)
    os.replace(tmp, path)


def write_json(path: Path, data: dict) -> None:
    atomic_write(path, json.dumps(data, indent=2, ensure_ascii=False) + "\n")


def backup(path: Path) -> None:
    """Copy path into BACKUP_DIR as <stem>.<timestamp><suffix>, keeping the newest MAX_BACKUPS."""
    if not path.exists():
        return
    BACKUP_DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d_%H%M%S") + f".{time.time_ns() % 10**6:06d}"
    shutil.copy2(path, BACKUP_DIR / f"{path.stem}.{stamp}{path.suffix}")
    for old in sorted(BACKUP_DIR.glob(f"{path.stem}.*{path.suffix}"))[:-MAX_BACKUPS]:
        old.unlink(missing_ok=True)


def read_clipboard() -> str:
    try:
        import tkinter
        root = tkinter.Tk()
        root.withdraw()
        try:
            return root.clipboard_get()
        finally:
            root.destroy()
    except Exception:
        pass
    for cmd in (["powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw"], ["pbpaste"],
                ["xclip", "-o", "-selection", "clipboard"], ["wl-paste"]):
        try:
            return subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8", timeout=5).stdout
        except Exception:
            continue
    return ""


# ---------------------------------------------------------------- pasted-text parsing

_ENV_LINE = re.compile(
    r"^\s*(?:export\s+|set\s+|\$env:)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*;?\s*$"
)


def parse_env_script(text: str) -> dict[str, str]:
    """bash `export K=V`, PowerShell `$env:K="V"`, cmd `set K=V`, or plain K=V lines."""
    env = {}
    for line in text.splitlines():
        m = _ENV_LINE.match(line)
        if m:
            v = m.group(2)
            if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
                v = v[1:-1]
            env[m.group(1)] = v
    return env


def load_json_obj(text: str) -> dict | None:
    try:
        data = json.loads(text)
    except ValueError:
        return None
    return data if isinstance(data, dict) else None


def parse_generic(text: str) -> dict[str, str]:
    """NewAPI-style JSON ({"url":..,"key":..}) or free text containing a URL and a key."""
    out: dict[str, str] = {}
    data = load_json_obj(text)
    if data is not None:
        lower = {str(k).lower(): v for k, v in data.items() if isinstance(v, str)}
        for k in ("url", "base_url", "baseurl", "endpoint", "host"):
            if lower.get(k):
                out["base_url"] = lower[k]
                break
        for k in ("key", "api_key", "apikey", "token", "auth_token"):
            if lower.get(k):
                out["key"] = lower[k]
                break
        for k in ("name", "model"):
            if lower.get(k):
                out[k] = lower[k]
        return out
    m = re.search(r"https?://[^\s\"',]+", text)
    if m:
        out["base_url"] = m.group(0)
    m = re.search(r"\b(sk-[A-Za-z0-9_\-]{8,}|[A-Za-z0-9_\-]{32,})\b",
                  text.replace(out.get("base_url", ""), ""))
    if m:
        out["key"] = m.group(1)
    return out


# ---------------------------------------------------------------- backend base

@dataclass
class Field:
    """One editable provider attribute, rendered by the TUI form."""
    name: str
    label: str
    kind: str = "text"  # text | password | select | dict
    options: tuple[str, ...] = ()
    placeholder: str = ""


class Backend:
    """A tool whose provider config we manage (Claude Code, Codex, ...)."""

    id = ""
    label = ""
    provider_cls: type = object
    fields: list[Field] = []

    def __init__(self, state: dict | None, save: Callable[[], None]) -> None:
        self._save = save
        self.providers: list[Any] = []
        if state is None:
            self.bootstrap()
        else:
            self.load_state(state)

    # -- persistence (subclasses add their own bookkeeping)
    def load_state(self, state: dict) -> None:
        names = {f.name for f in dataclasses.fields(self.provider_cls)}
        self.providers = [self.provider_cls(**{k: v for k, v in p.items() if k in names})
                          for p in state.get("providers", [])]

    def dump_state(self) -> dict:
        return {"providers": [dataclasses.asdict(p) for p in self.providers]}

    def save(self) -> None:
        self._save()

    # -- to implement
    @property
    def target(self) -> Path:
        raise NotImplementedError

    def bootstrap(self) -> None:
        raise NotImplementedError

    def active(self) -> Any | None:
        raise NotImplementedError

    def apply(self, provider: Any) -> None:
        raise NotImplementedError

    def parse(self, text: str) -> dict:
        raise NotImplementedError

    def normalize(self, provider: Any) -> Any:
        """Clean up a provider coming out of the editor before storing it."""
        return provider

    # -- lookup
    def find(self, query: str) -> Any | None:
        if query.isdigit() and 1 <= int(query) <= len(self.providers):
            return self.providers[int(query) - 1]
        q = query.lower()
        for match in (
            lambda p: p.name == query,
            lambda p: p.name.lower() == q,
            lambda p: p.name.lower().startswith(q),
            lambda p: q in p.name.lower() or q in p.base_url.lower(),
        ):
            hits = [p for p in self.providers if match(p)]
            if len(hits) == 1:
                return hits[0]
        return None

    def find_same(self, base_url: str, key: str) -> Any | None:
        for p in self.providers:
            if p.base_url.rstrip("/") == base_url.rstrip("/") and p.key == key:
                return p
        return None

    def unique_name(self, name: str, exclude: Any = None) -> str:
        taken = {p.name for p in self.providers if p is not exclude}
        if name not in taken:
            return name
        i = 2
        while f"{name} ({i})" in taken:
            i += 1
        return f"{name} ({i})"

    # -- mutations
    def upsert(self, provider: Any, original: Any = None) -> Any:
        provider = self.normalize(provider)
        if original is not None and original in self.providers:
            self.providers[self.providers.index(original)] = provider
        else:
            self.providers.append(provider)
        self.save()
        return provider

    def remove(self, provider: Any) -> None:
        self.providers.remove(provider)
        self.save()

    def move(self, provider: Any, delta: int) -> None:
        i = self.providers.index(provider)
        j = max(0, min(len(self.providers) - 1, i + delta))
        self.providers.insert(j, self.providers.pop(i))
        self.save()

    def from_info(self, info: dict, **overrides: Any) -> Any:
        """Build a new provider from parse() output."""
        names = {f.name for f in dataclasses.fields(self.provider_cls)}
        data = {k: v for k, v in {**info, **overrides}.items() if k in names and v not in (None, "")}
        data["name"] = self.unique_name(data.get("name") or "provider")
        return self.normalize(self.provider_cls(**data))
