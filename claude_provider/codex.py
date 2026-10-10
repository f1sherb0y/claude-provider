"""Codex backend: ~/.codex/config.toml -> model_provider + [model_providers.<id>]."""
from __future__ import annotations

import dataclasses
import os
import re
import tomllib
from dataclasses import dataclass
from pathlib import Path

import tomlkit

from .common import Backend, Field, atomic_write, backup, load_json_obj, name_from_url, parse_env_script, parse_generic

WIRE_APIS = ("responses", "chat")


def codex_home() -> Path:
    return Path(os.environ.get("CODEX_HOME") or Path.home() / ".codex")


def normalize_base_url(url: str) -> str:
    """Codex wants the API root (usually ending in /v1); drop endpoint paths people paste."""
    url = url.strip().rstrip("/")
    return re.sub(r"/(responses|chat/completions)$", "", url, flags=re.I) if url else url


def slug(text: str) -> str:
    return re.sub(r"[^a-z0-9_-]+", "_", text.lower()).strip("_") or "provider"


@dataclass
class CodexProvider:
    name: str
    id: str = ""  # key under [model_providers]; empty base_url = official ChatGPT/OpenAI login
    base_url: str = ""
    key: str = ""
    wire_api: str = "responses"
    model: str = ""  # empty = leave the current model alone
    env_key: str = ""  # if set, key goes to ~/.codex/.env under this name instead of the config


def _read_dotenv(path: Path) -> dict[str, str]:
    return parse_env_script(path.read_text(encoding="utf-8")) if path.exists() else {}


def _write_dotenv(path: Path, name: str, value: str) -> None:
    lines = path.read_text(encoding="utf-8").splitlines() if path.exists() else []
    pat = re.compile(rf"^\s*(export\s+)?{re.escape(name)}\s*=")
    for i, line in enumerate(lines):
        if pat.match(line):
            lines[i] = f"{name}={value}"
            break
    else:
        lines.append(f"{name}={value}")
    backup(path)
    atomic_write(path, "\n".join(lines) + "\n")


class CodexBackend(Backend):
    id = "codex"
    label = "Codex"
    provider_cls = CodexProvider
    fields = [
        Field("name", "名称"),
        Field("id", "配置 ID（model_providers 下的键，留空自动生成）"),
        Field("base_url", "Base URL（留空 = 官方 ChatGPT 登录）", placeholder="https://api.example.com/v1"),
        Field("key", "Key", "password"),
        Field("wire_api", "wire_api", "select", WIRE_APIS),
        Field("model", "model（可选，留空 = 不改当前模型）"),
        Field("env_key", "env_key（可选，填了则 Key 写入 ~/.codex/.env）"),
    ]
    paste_hint = "[model_providers.x] TOML / export OPENAI_* / JSON / URL+Key"

    @property
    def target(self) -> Path:
        return codex_home() / "config.toml"

    @property
    def dotenv(self) -> Path:
        return codex_home() / ".env"

    def read_doc(self) -> tomlkit.TOMLDocument:
        if not self.target.exists():
            return tomlkit.document()
        return tomlkit.parse(self.target.read_text(encoding="utf-8"))

    def bootstrap(self) -> None:
        """First run: import every [model_providers.*] table plus an official entry."""
        doc = self.read_doc()
        dotenv = _read_dotenv(self.dotenv)
        current = doc.get("model_provider")
        self.providers = []
        for pid, t in doc.get("model_providers", {}).items():
            env_key = str(t.get("env_key", ""))
            key = str(t.get("experimental_bearer_token", "")) or dotenv.get(env_key) or os.environ.get(env_key, "")
            self.providers.append(CodexProvider(
                name=str(t.get("name", pid)),
                id=pid,
                base_url=str(t.get("base_url", "")),
                key=key,
                wire_api=str(t.get("wire_api", "responses")),
                model=str(doc.get("model", "")) if pid == current else "",
                env_key=env_key,
            ))
        self.providers.sort(key=lambda p: p.id != current)
        self.providers.append(CodexProvider(name="OpenAI 官方 (ChatGPT 登录)"))

    def active(self) -> CodexProvider | None:
        current = self.read_doc().get("model_provider")
        for p in self.providers:
            if (p.base_url and p.id == current) or (not p.base_url and current in (None, "openai")):
                return p
        return None

    def apply(self, p: CodexProvider) -> None:
        doc = self.read_doc()
        if not p.base_url:
            doc.pop("model_provider", None)
        else:
            providers = doc.get("model_providers")
            if providers is None:
                providers = doc["model_providers"] = tomlkit.table(is_super_table=True)
            table = providers.get(p.id)
            if table is None:
                table = providers[p.id] = tomlkit.table()
            # update in place so unrelated keys (env_key_instructions, headers, ...) survive
            table["name"] = p.name
            table["base_url"] = p.base_url
            table["wire_api"] = p.wire_api
            if p.env_key:
                table["env_key"] = p.env_key
                table.pop("experimental_bearer_token", None)
                if p.key:
                    _write_dotenv(self.dotenv, p.env_key, p.key)
            else:
                table.pop("env_key", None)
                table["experimental_bearer_token"] = p.key
            if "model_provider" in doc:
                doc["model_provider"] = p.id
            else:
                try:  # put it back at the top where it normally lives
                    doc._insert_at(0, "model_provider", p.id)
                except Exception:
                    doc["model_provider"] = p.id
        if p.model:
            doc["model"] = p.model
        backup(self.target)
        atomic_write(self.target, tomlkit.dumps(doc))

    # -- input
    def parse(self, text: str) -> dict:
        text = text.strip()
        out: dict = {}
        try:
            data = tomllib.loads(text)
        except tomllib.TOMLDecodeError:
            data = None
        table = None
        if data:
            tables = data.get("model_providers")
            if isinstance(tables, dict) and tables:
                pid, table = next(iter(tables.items()))
                out["id"] = pid
            elif "base_url" in data:
                table = data
        if table is not None:
            for src, dst in (("name", "name"), ("base_url", "base_url"), ("wire_api", "wire_api"),
                             ("env_key", "env_key"), ("experimental_bearer_token", "key")):
                if isinstance(table.get(src), str):
                    out[dst] = table[src]
            if isinstance(data.get("model"), str):
                out["model"] = data["model"]
        else:
            json_obj = load_json_obj(text)
            env = {k: str(v) for k, v in json_obj.items()} if json_obj else parse_env_script(text)
            if env.get("OPENAI_BASE_URL"):
                out["base_url"] = env["OPENAI_BASE_URL"]
                if env.get("OPENAI_API_KEY"):
                    out["key"] = env["OPENAI_API_KEY"]
                if env.get("OPENAI_MODEL"):
                    out["model"] = env["OPENAI_MODEL"]
            else:
                out = parse_generic(text)
        if out.get("base_url"):
            out["base_url"] = normalize_base_url(out["base_url"])
            out.setdefault("name", name_from_url(out["base_url"]))
        return out

    def normalize(self, p: CodexProvider) -> CodexProvider:
        p = dataclasses.replace(p, base_url=normalize_base_url(p.base_url))
        if p.base_url and not p.id:
            taken = {q.id for q in self.providers}
            base = pid = slug(p.name)
            i = 2
            while pid in taken:
                pid, i = f"{base}_{i}", i + 1
            p.id = pid
        return p
