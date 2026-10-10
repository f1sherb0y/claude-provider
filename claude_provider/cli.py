"""Command line entry point. No subcommand -> TUI."""
from __future__ import annotations

import argparse
import sys

from .common import mask, read_clipboard
from .store import Store


def _print_list(backend) -> None:
    active = backend.active()
    for i, p in enumerate(backend.providers, 1):
        mark = "●" if p is active else " "
        url = p.base_url or "(官方登录)"
        print(f"{mark} {i}. {p.name:<24} {url:<40} {mask(p.key)}  {p.model}")


def main(argv: list[str] | None = None) -> None:
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(errors="replace")
        except Exception:
            pass

    ap = argparse.ArgumentParser(
        prog="claude-provider",
        description="快速切换 Claude Code / Codex 的 API provider（不带子命令启动 TUI）",
    )
    tool = ap.add_mutually_exclusive_group()
    tool.add_argument("-x", "--codex", dest="tool", action="store_const", const="codex", help="操作 Codex")
    tool.add_argument("--claude", dest="tool", action="store_const", const="claude", help="操作 Claude Code（默认）")
    sub = ap.add_subparsers(dest="cmd")
    sub.add_parser("list", aliases=["ls"], help="列出所有 provider")
    sub.add_parser("current", help="显示当前 provider")
    p_use = sub.add_parser("use", help="切换 provider（名称/序号/模糊匹配）")
    p_use.add_argument("name")
    p_add = sub.add_parser("add", help="新增 provider；支持 JSON / settings.json / TOML / export / $env: 文本")
    p_add.add_argument("text", nargs="?", help="要识别的文本；- 表示从 stdin 读取")
    p_add.add_argument("-c", "--clip", action="store_true", help="从剪贴板读取")
    p_add.add_argument("--name")
    p_add.add_argument("--url")
    p_add.add_argument("--key")
    p_add.add_argument("--model", default="")
    p_add.add_argument("--use", action="store_true", help="添加后立即切换")
    p_rm = sub.add_parser("rm", help="删除 provider")
    p_rm.add_argument("name")
    args = ap.parse_args(argv)

    store = Store()

    if args.cmd is None:
        from .tui import ProviderApp
        ProviderApp(store, args.tool).run()
        return

    backend = store[args.tool or "claude"]

    if args.cmd in ("list", "ls"):
        _print_list(backend)
    elif args.cmd == "current":
        p = backend.active()
        print(p.name if p else "(未匹配到已保存的 provider)")
    elif args.cmd == "use":
        p = backend.find(args.name)
        if not p:
            sys.exit(f"找不到 provider: {args.name}")
        backend.apply(p)
        print(f"✔ {backend.label} 已切换到 {p.name}  →  {backend.target}（新开会话生效）")
    elif args.cmd == "add":
        text = read_clipboard() if args.clip else sys.stdin.read() if args.text == "-" else args.text
        info = backend.parse(text) if text else {}
        overrides = {k: v for k, v in (("name", args.name), ("base_url", args.url),
                                       ("key", args.key), ("model", args.model)) if v}
        info = {**info, **overrides}
        if not info.get("base_url") or not info.get("key"):
            sys.exit("没有识别到 url 和 key（用 --url/--key，或者传入 JSON / TOML / export 文本）")
        new = backend.from_info(info)
        existing = backend.find_same(new.base_url, new.key)
        if existing:
            print(f"已存在相同的 provider: {existing.name}")
            p = existing
        else:
            p = backend.upsert(new)
            print(f"✔ 已添加 {p.name} 到 {backend.label}")
        if args.use:
            backend.apply(p)
            print(f"✔ 已切换到 {p.name}")
    elif args.cmd == "rm":
        p = backend.find(args.name)
        if not p:
            sys.exit(f"找不到 provider: {args.name}")
        backend.remove(p)
        print(f"✔ 已删除 {p.name}")


if __name__ == "__main__":
    main()
