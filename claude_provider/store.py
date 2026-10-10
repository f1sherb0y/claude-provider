"""Loads/saves providers.json and wires up the backends."""
from __future__ import annotations

import json

from .claude import ClaudeBackend
from .codex import CodexBackend
from .common import DATA_FILE, Backend, write_json

BACKENDS = (ClaudeBackend, CodexBackend)


class Store:
    def __init__(self) -> None:
        data = {}
        if DATA_FILE.exists():
            with open(DATA_FILE, encoding="utf-8") as f:
                data = json.load(f)
        if "providers" in data:  # v0.1 format: flat Claude-only
            data = {"claude": data}
        self.last_tab: str = data.get("last_tab", "claude")
        self.backends: dict[str, Backend] = {
            cls.id: cls(data.get(cls.id), self.save) for cls in BACKENDS
        }
        if any(cls.id not in data for cls in BACKENDS):
            self.save()

    def save(self) -> None:
        if not hasattr(self, "backends"):
            return  # still bootstrapping
        write_json(DATA_FILE, {
            "last_tab": self.last_tab,
            **{bid: b.dump_state() for bid, b in self.backends.items()},
        })

    def __getitem__(self, backend_id: str) -> Backend:
        return self.backends[backend_id]
