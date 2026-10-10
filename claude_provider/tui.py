"""Textual TUI. ←/→ switches between Claude Code and Codex."""
from __future__ import annotations

import dataclasses
import time
from typing import Any

from rich.cells import cell_len
from rich.markup import escape
from rich.text import Text
from textual import events
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Horizontal, VerticalScroll
from textual.screen import ModalScreen
from textual.timer import Timer
from textual.widgets import Button, DataTable, Footer, Header, Input, Label, Select, Static, Tab, Tabs, TextArea

from .common import Backend, mask, read_clipboard
from .store import Store


def _parse_env_lines(text: str) -> dict[str, str]:
    env = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        env[k.strip()] = v.strip()
    return env


class EditScreen(ModalScreen[Any]):
    BINDINGS = [
        Binding("escape", "cancel", "取消"),
        Binding("ctrl+s", "save", "保存"),
        Binding("ctrl+r", "reveal", "显示/隐藏 Key"),
    ]

    def __init__(self, backend: Backend, provider: Any, title: str,
                 original: Any = None, paste: str = "") -> None:
        super().__init__()
        self.backend = backend
        self.original = original
        self.p = provider or backend.provider_cls(name="")
        self.title_text = title
        self.paste = paste

    def compose(self) -> ComposeResult:
        with VerticalScroll(id="dialog"):
            yield Label(f"{self.title_text}  ·  {self.backend.label}", id="dialog-title")
            yield Label(f"⚡ 快速粘贴：{self.backend.paste_hint}，自动识别")
            yield TextArea(id="quick")
            for f in self.backend.fields:
                value = getattr(self.p, f.name)
                yield Label(f.label)
                if f.kind == "select":
                    yield Select([(o, o) for o in f.options], value=value, allow_blank=False, id=f"f_{f.name}")
                elif f.kind == "dict":
                    yield TextArea("\n".join(f"{k}={v}" for k, v in value.items()), id=f"f_{f.name}", classes="dict")
                else:
                    yield Input(value, password=f.kind == "password", placeholder=f.placeholder, id=f"f_{f.name}")
            with Horizontal(id="buttons"):
                yield Button("保存 (Ctrl+S)", variant="primary", id="save")
                yield Button("取消 (Esc)", id="cancel")

    def on_mount(self) -> None:
        if self.paste:
            self.query_one("#quick", TextArea).text = self.paste
        self.query_one("#quick" if self.original is None else "#f_name").focus()

    def on_text_area_changed(self, event: TextArea.Changed) -> None:
        if event.text_area.id == "quick" and event.text_area.text.strip():
            self.fill(self.backend.parse(event.text_area.text))

    def fill(self, info: dict) -> None:
        for f in self.backend.fields:
            value = info.get(f.name)
            if not value or (f.name == "name" and self.query_one("#f_name", Input).value):
                continue
            widget = self.query_one(f"#f_{f.name}")
            if f.kind == "dict":
                widget.text = "\n".join(f"{k}={v}" for k, v in value.items())
            elif f.kind == "select":
                if value in f.options:
                    widget.value = value
            else:
                widget.value = self.backend.unique_name(value) if f.name == "name" else value
        if info:
            self.notify("已识别：" + ", ".join(info), timeout=2)

    def on_button_pressed(self, event: Button.Pressed) -> None:
        self.action_save() if event.button.id == "save" else self.action_cancel()

    def action_reveal(self) -> None:
        key = self.query_one("#f_key", Input)
        key.password = not key.password

    def action_cancel(self) -> None:
        self.dismiss(None)

    def action_save(self) -> None:
        values: dict[str, Any] = {}
        for f in self.backend.fields:
            widget = self.query_one(f"#f_{f.name}")
            if f.kind == "dict":
                values[f.name] = _parse_env_lines(widget.text)
            elif f.kind == "select":
                values[f.name] = widget.value
            else:
                values[f.name] = widget.value.strip()
        if not values["name"] and values.get("base_url"):
            values["name"] = self.backend.parse(values["base_url"]).get("name", "")
        if not values["name"]:
            self.notify("名称不能为空", severity="error")
            return
        if any(p.name == values["name"] and p is not self.original for p in self.backend.providers):
            self.notify(f"名称 {values['name']} 已存在", severity="error")
            return
        self.dismiss(dataclasses.replace(self.p, **values))


class ProviderTable(DataTable):
    """DataTable whose ←/→ switch tool tabs instead of moving a column cursor."""

    BINDINGS = [
        Binding("left", "app.prev_tab", "Claude/Codex", show=False),
        Binding("right", "app.next_tab", "Claude/Codex", show=False),
    ]


class ProviderApp(App):
    TITLE = "Claude Provider"
    CSS = """
    Tabs { dock: top; margin-top: 1; }
    #status { padding: 0 1; color: $text-muted; }
    ProviderTable { height: 1fr; }
    EditScreen { align: center middle; }
    #dialog { width: 90%; max-width: 84; min-width: 30; max-height: 100%;
              border: thick $accent; background: $surface; padding: 1 2; }
    #dialog-title { text-style: bold; color: $accent; margin-bottom: 1; }
    #dialog Label { margin-top: 1; width: 100%; }
    #quick { height: 4; border: tall $success; }
    #dialog .dict { height: 5; }
    #buttons { height: auto; margin-top: 1; align-horizontal: right; }
    #buttons Button { margin-left: 1; }
    """
    BINDINGS = [
        Binding("enter", "use", "切换"),
        Binding("left,right", "noop", "←/→ Claude/Codex", key_display="←→"),
        Binding("a", "add", "新增"),
        Binding("v", "paste", "剪贴板新增"),
        Binding("e", "edit", "编辑"),
        Binding("c", "copy", "复制"),
        Binding("d", "delete", "删除"),
        Binding("K", "move(-1)", "上移", show=False),
        Binding("J", "move(1)", "下移", show=False),
        Binding("q", "quit", "退出"),
    ]

    def __init__(self, store: Store, tab: str | None = None) -> None:
        super().__init__()
        self.store = store
        self.tab = tab or store.last_tab
        if self.tab not in store.backends:
            self.tab = "claude"
        self._pending_delete: tuple[Any, float] | None = None
        self._active: Any = None
        self._relayout_timer: Timer | None = None

    @property
    def backend(self) -> Backend:
        return self.store[self.tab]

    def compose(self) -> ComposeResult:
        yield Header()
        yield Tabs(*[Tab(b.label, id=bid) for bid, b in self.store.backends.items()], active=self.tab)
        yield Static(id="status")
        yield ProviderTable(cursor_type="row", zebra_stripes=True)
        yield Footer()

    def on_mount(self) -> None:
        self.query_one(Tabs).can_focus = False
        self.query_one(ProviderTable).focus()
        self.refresh_table(focus=self.backend.active())

    def on_resize(self, event: events.Resize) -> None:
        # Resize events arrive in bursts while dragging; re-layout once it settles.
        # (Named _relayout_timer: App already uses _resize_timer internally.)
        if self._relayout_timer is not None:
            self._relayout_timer.stop()
        self._relayout_timer = self.set_timer(0.05, self.render_table)

    # ---------------------------------------------------------------- helpers
    def refresh_table(self, focus: Any = None) -> None:
        """Re-read the active provider from disk, then redraw."""
        backend = self.backend
        try:
            self._active = backend.active()
        except Exception as e:
            self._active = None
            self.notify(f"读取 {backend.target} 失败: {e}", severity="error")
        row = None
        if focus is not None and focus in backend.providers:
            row = backend.providers.index(focus)
        self.render_table(row)

    def render_table(self, row: int | None = None) -> None:
        """Lay the table out for the current terminal width (no disk access)."""
        table = self.query_one(ProviderTable)
        if row is None:
            row = table.cursor_row
        backend, active = self.backend, self._active
        width = self.size.width
        providers = backend.providers

        def longest(values, cap: int, floor: int) -> int:
            return max([floor, *(min(cell_len(v), cap) for v in values)])

        show_key = width >= 80
        show_model = width >= 110 and any(p.model for p in providers)
        name_w = longest((p.name for p in providers), 24 if width >= 80 else 14, 4)
        key_w, model_w = 12, longest((p.model for p in providers), 18, 5)
        cols = [(" ", 1), ("#", max(1, len(str(len(providers))))), ("名称", name_w)]
        # every column gets 1 cell of padding on each side; leave 2 for the scrollbar
        fixed = sum(w for _, w in cols) + (key_w if show_key else 0) + (model_w if show_model else 0)
        n_cols = len(cols) + 1 + show_key + show_model
        url_w = max(8, width - fixed - 2 * n_cols - 2)
        cols.append(("Base URL", url_w))
        if show_key:
            cols.append(("Key", key_w))
        if show_model:
            cols.append(("Model", model_w))

        table.clear(columns=True)
        for label, w in cols:
            table.add_column(label, width=w)

        def cell(text: str, w: int, style: str = "") -> Text:
            t = Text(text, style=style, no_wrap=True)
            t.truncate(w, overflow="ellipsis")
            return t

        for i, p in enumerate(providers, 1):
            is_active = p is active
            cells = [
                Text("●", style="bold green") if is_active else Text(""),
                Text(str(i)),
                cell(p.name, name_w, "bold" if is_active else ""),
                cell(p.base_url, url_w) if p.base_url else cell("(官方登录)", url_w, "dim"),
            ]
            if show_key:
                cells.append(cell(mask(p.key), key_w))
            if show_model:
                cells.append(cell(p.model or "-", model_w, "" if p.model else "dim"))
            table.add_row(*cells)
        if providers:
            table.move_cursor(row=max(0, min(row, len(providers) - 1)), animate=False)

        name = active.name if active else "未知/自定义"
        status = f"当前 {backend.label}: [b green]{escape(name)}[/]"
        if width >= 120:
            status += f"   [dim]{escape(str(backend.target))}  ·  1-9/Enter 切换  ·  v 从剪贴板新增[/]"
        elif width >= 70:
            status += "   [dim]1-9/Enter 切换  ·  v 剪贴板新增[/]"
        self.query_one("#status", Static).update(status)

    def selected(self) -> Any:
        providers = self.backend.providers
        if not providers:
            return None
        return providers[self.query_one(ProviderTable).cursor_row]

    def switch_to(self, p: Any) -> None:
        try:
            self.backend.apply(p)
        except Exception as e:  # surface IO / parse errors instead of crashing
            self.notify(f"写入失败: {e}", severity="error")
            return
        self.refresh_table(focus=p)
        self.notify(f"{self.backend.label} → {p.name}，新开会话生效", title="✔ 切换成功")

    def _set_tab(self, tab: str) -> None:
        if tab == self.tab:
            return
        self.tab = tab
        self.store.last_tab = tab
        self.store.save()
        self.query_one(Tabs).active = tab
        self.query_one(ProviderTable).move_cursor(row=0)
        self.refresh_table(focus=self.backend.active())

    # ---------------------------------------------------------------- events
    def on_tabs_tab_activated(self, event: Tabs.TabActivated) -> None:
        if event.tab and event.tab.id:
            self._set_tab(event.tab.id)

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        self.action_use()

    def on_key(self, event) -> None:
        if len(self.screen_stack) == 1 and event.character and event.character in "123456789":
            idx = int(event.character) - 1
            if idx < len(self.backend.providers):
                event.stop()
                self.switch_to(self.backend.providers[idx])

    # ---------------------------------------------------------------- actions
    def action_noop(self) -> None:
        pass

    def _cycle_tab(self, delta: int) -> None:
        ids = list(self.store.backends)
        self._set_tab(ids[(ids.index(self.tab) + delta) % len(ids)])

    def action_prev_tab(self) -> None:
        self._cycle_tab(-1)

    def action_next_tab(self) -> None:
        self._cycle_tab(1)

    def action_use(self) -> None:
        p = self.selected()
        if p:
            self.switch_to(p)

    def _open_editor(self, provider: Any, title: str, original: Any, paste: str = "") -> None:
        backend = self.backend

        def done(result: Any) -> None:
            if result is None:
                return
            saved = backend.upsert(result, original)
            self.refresh_table(focus=saved)
            self.notify(f"已保存 {saved.name}")
        self.push_screen(EditScreen(backend, provider, title, original, paste), done)

    def action_add(self) -> None:
        self._open_editor(None, "新增 Provider", None)

    def action_paste(self) -> None:
        text = read_clipboard()
        if not self.backend.parse(text).get("base_url"):
            self.notify("剪贴板里没有识别到 Base URL", severity="warning")
            return
        self._open_editor(None, "从剪贴板新增", None, paste=text)

    def action_edit(self) -> None:
        p = self.selected()
        if p:
            self._open_editor(p, f"编辑 {p.name}", p)

    def action_copy(self) -> None:
        p = self.selected()
        if p:
            clone = dataclasses.replace(p, name=self.backend.unique_name(p.name))
            if hasattr(clone, "id"):
                clone.id = ""
            self._open_editor(clone, f"复制 {p.name}", None)

    def action_delete(self) -> None:
        p = self.selected()
        if not p:
            return
        pending = self._pending_delete
        if pending and pending[0] is p and time.monotonic() - pending[1] < 3:
            self._pending_delete = None
            self.backend.remove(p)
            self.refresh_table()
            self.notify(f"已删除 {p.name}")
        else:
            self._pending_delete = (p, time.monotonic())
            self.notify(f"再按一次 d 删除 {p.name}", severity="warning", timeout=3)

    def action_move(self, delta: int) -> None:
        p = self.selected()
        if p:
            self.backend.move(p, delta)
            self.refresh_table(focus=p)
