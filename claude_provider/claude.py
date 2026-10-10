"""Claude Code backend: ~/.claude/settings.json -> env."""
from __future__ import annotations

import dataclasses
import json
import os
import re
from dataclasses import dataclass, field
from pathlib import Path

from .common import Backend, Field, backup, load_json_obj, name_from_url, parse_env_script, parse_generic, write_json

KEY_VARS = ("ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY")

# Env vars that always belong to "the provider" and are cleared on every switch.
MANAGED_KEYS = {
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_MODEL",
    "ANTHROPIC_SMALL_FAST_MODEL",
    "ANTHROPIC_DEFAULT_OPUS_MODEL",
    "ANTHROPIC_DEFAULT_SONNET_MODEL",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL",
    "CLAUDE_CODE_SUBAGENT_MODEL",
}

# env var -> Provider field; anything else lands in extra_env
ENV_FIELDS = {
    "ANTHROPIC_BASE_URL": "base_url",
    "ANTHROPIC_AUTH_TOKEN": "key",
    "ANTHROPIC_API_KEY": "key",
    "ANTHROPIC_MODEL": "model",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "haiku_model",
}


def settings_file() -> Path:
    return Path(os.environ.get("CLAUDE_CONFIG_DIR") or Path.home() / ".claude") / "settings.json"


def normalize_base_url(url: str) -> str:
    """Claude Code appends /v1/messages itself, so strip API path suffixes."""
    url = url.strip().rstrip("/")
    return re.sub(r"(/v1)?(/messages)?$", "", url, flags=re.I) if url else url


@dataclass
class ClaudeProvider:
    name: str
    base_url: str = ""
    key: str = ""
    key_var: str = "ANTHROPIC_AUTH_TOKEN"
    model: str = ""
    haiku_model: str = ""
    extra_env: dict[str, str] = field(default_factory=dict)

    def to_env(self) -> dict[str, str]:
        env: dict[str, str] = {}
        if self.base_url:
            env["ANTHROPIC_BASE_URL"] = normalize_base_url(self.base_url)
        if self.key:
            env[self.key_var] = self.key
        if self.model:
            env["ANTHROPIC_MODEL"] = self.model
        if self.haiku_model:
            env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = self.haiku_model
        env.update(self.extra_env)
        return env


def _from_env(env: dict[str, str]) -> dict:
    out: dict = {}
    extra: dict[str, str] = {}
    for k, v in env.items():
        field_name = ENV_FIELDS.get(k)
        if field_name is None:
            extra[k] = v
        elif field_name == "key":
            # AUTH_TOKEN wins if both are present
            if "key" not in out or k == "ANTHROPIC_AUTH_TOKEN":
                out["key"], out["key_var"] = v, k
        else:
            out[field_name] = v
    if extra:
        out["extra_env"] = extra
    return out


class ClaudeBackend(Backend):
    id = "claude"
    label = "Claude Code"
    provider_cls = ClaudeProvider
    fields = [
        Field("name", "名称"),
        Field("base_url", "Base URL（留空 = 官方登录）", placeholder="https://api.example.com"),
        Field("key", "Key", "password"),
        Field("key_var", "Key 变量", "select", KEY_VARS),
        Field("model", "ANTHROPIC_MODEL（可选）"),
        Field("haiku_model", "ANTHROPIC_DEFAULT_HAIKU_MODEL（可选）"),
        Field("extra_env", "额外环境变量（每行 KEY=VALUE）", "dict"),
    ]
    paste_hint = "settings.json / env JSON / export / $env: / URL+Key"

    def __init__(self, state, save) -> None:
        self.applied_keys: list[str] = []
        self.shadowed: dict[str, str] = {}
        super().__init__(state, save)

    @property
    def target(self) -> Path:
        return settings_file()

    # -- persistence
    def load_state(self, state: dict) -> None:
        super().load_state(state)
        self.applied_keys = state.get("applied_keys", [])
        self.shadowed = state.get("shadowed", {})

    def dump_state(self) -> dict:
        return {**super().dump_state(), "applied_keys": self.applied_keys, "shadowed": self.shadowed}

    def read_settings(self) -> dict:
        if not self.target.exists():
            return {}
        with open(self.target, encoding="utf-8") as f:
            return json.load(f)

    def bootstrap(self) -> None:
        """First run: official login entry + whatever is configured right now."""
        self.providers = [ClaudeProvider(name="Anthropic 官方")]
        env = self.read_settings().get("env", {})
        if env.get("ANTHROPIC_BASE_URL"):
            info = _from_env({k: v for k, v in env.items() if k in ENV_FIELDS})
            self.providers.insert(0, ClaudeProvider(name=name_from_url(env["ANTHROPIC_BASE_URL"]), **info))
            self.applied_keys = sorted(k for k in env if k in MANAGED_KEYS)

    # -- settings interaction
    def _owned_keys(self) -> set[str]:
        return MANAGED_KEYS | set(self.applied_keys)

    def active(self) -> ClaudeProvider | None:
        env = self.read_settings().get("env", {})
        for p in self.providers:
            pe = p.to_env()
            if all(env.get(k) == pe.get(k) for k in MANAGED_KEYS) and all(env.get(k) == v for k, v in pe.items()):
                return p
        # looser match: same url + key, other params drifted
        for p in self.providers:
            pe = p.to_env()
            if all(env.get(k) == pe.get(k) for k in ("ANTHROPIC_BASE_URL", p.key_var)):
                return p
        return None

    def apply(self, provider: ClaudeProvider) -> None:
        settings = self.read_settings()
        env = dict(settings.get("env", {}))
        for k in self._owned_keys():
            env.pop(k, None)
        env.update(self.shadowed)  # restore user values a previous provider overrode
        new_env = provider.to_env()
        # Only take ownership of keys we actually change, so a provider that repeats
        # a global setting (e.g. DISABLE_NONESSENTIAL_TRAFFIC=1) won't delete it later.
        changed = {k: v for k, v in new_env.items() if k in MANAGED_KEYS or env.get(k) != v}
        self.shadowed = {k: env[k] for k in changed if k in env and k not in MANAGED_KEYS}
        env.update(changed)
        settings["env"] = env
        backup(self.target)
        write_json(self.target, settings)
        self.applied_keys = sorted(changed)
        self.save()

    # -- input
    def parse(self, text: str) -> dict:
        """settings.json ({"env": {...}}), flat env JSON, shell env scripts, or generic URL+key."""
        text = text.strip()
        data = load_json_obj(text)
        if data is not None:
            env = data.get("env") if isinstance(data.get("env"), dict) else data
            env = {k: str(v) for k, v in env.items()}
        else:
            env = parse_env_script(text)
        out = _from_env(env) if any(k in ENV_FIELDS for k in env) else parse_generic(text)
        if out.get("base_url"):
            out["base_url"] = normalize_base_url(out["base_url"])
            out.setdefault("name", name_from_url(out["base_url"]))
        return out

    def normalize(self, p: ClaudeProvider) -> ClaudeProvider:
        return dataclasses.replace(p, base_url=normalize_base_url(p.base_url))
