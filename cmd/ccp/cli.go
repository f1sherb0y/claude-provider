// Command line entry point. No subcommand -> TUI.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const usageMain = `快速切换 Claude Code / Codex 的 API provider（不带子命令启动 TUI）

Usage: ccp [OPTIONS] [COMMAND]

Commands:
  list     列出所有 provider [alias: ls]
  current  显示当前 provider
  use      切换 provider（名称/序号/模糊匹配）
  add      新增 provider；支持 JSON / settings.json / TOML / export / $env: 文本
  set      修改已保存的 provider
  rm       删除 provider
  help     显示帮助

Options:
  -x, --codex   操作 Codex
      --claude  操作 Claude Code（默认）
  -h, --help    Print help
`

const usageAdd = `新增 provider；支持 JSON / settings.json / TOML / export / $env: 文本

Usage: ccp [-x] add [OPTIONS] [TEXT]

Arguments:
  [TEXT]  要识别的文本；- 表示从 stdin 读取

Options:
  -c, --clip           从剪贴板读取
      --name <NAME>
      --url <URL>
      --key <KEY>
      --model <MODEL>
      --env <KEY=VALUE>  随 provider 一起写入的环境变量（可重复；有它时可以不给 url / key）
      --config <KEY=VALUE>  Codex：随 provider 写入 config.toml 的键，切走时还原（可重复）
      --use            添加后立即切换
  -h, --help           Print help
`

const usageSet = `修改已保存的 provider（只改给出的项）

Usage: ccp [-x] set <NAME> [OPTIONS]

Options:
      --name <NAME>
      --url <URL>
      --key <KEY>
      --model <MODEL>
      --env <KEY=VALUE>        新增或替换一个环境变量（可重复）
      --unset-env <KEY>        删除一个环境变量（可重复）
      --config <KEY=VALUE>     Codex：新增或替换一个 config.toml 键，值是 TOML（可重复）
      --unset-config <KEY>     删除一个 config.toml 键（可重复）
  -h, --help                   Print help

如果该 provider 正在使用，改完后再执行一次 ccp use <NAME> 才会写入配置文件。
`

type addArgs struct {
	text                  string
	hasText, clip, use    bool
	name, url, key, model string
	env, config           *Dict
	unsetEnv, unsetConfig []string
	// which of name/url/key/model were given (set must tell "" from absent)
	given map[string]bool
}

func printList(store *Store, tool Tool) error {
	b := store.Get(tool)
	active, err := b.Active()
	if err != nil {
		return err
	}
	for i, p := range b.Providers {
		mark := " "
		if i == active {
			mark = "●"
		}
		url := p.BaseURL()
		if url == "" {
			url = loginLabel(p)
		}
		fmt.Printf("%s %d. %-24s %-40s %s  %s\n", mark, i+1, p.Name(), url, mask(p.Key()), p.Model())
	}
	return nil
}

func cliMain(argv []string) int {
	if err := cliRun(argv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func cliRun(argv []string) error {
	var codex, claude, help bool
	var rest []string
	for i, a := range argv {
		switch {
		case a == "--":
			rest = append(rest, argv[i:]...)
		case a == "-x" || a == "--codex":
			codex = true
			continue
		case a == "--claude":
			claude = true
			continue
		case (a == "-h" || a == "--help") && len(rest) == 0:
			help = true
			continue
		default:
			rest = append(rest, a)
			continue
		}
		break
	}
	if codex && claude {
		return errors.New("error: --codex 和 --claude 不能同时使用")
	}
	if help || len(rest) > 0 && rest[0] == "help" {
		if len(rest) > 1 && rest[1] == "add" {
			fmt.Print(usageAdd)
		} else {
			fmt.Print(usageMain)
		}
		return nil
	}
	if len(rest) > 0 && rest[0] == "__render" {
		return renderCommand(rest[1:], codex, claude)
	}
	store, err := LoadStore()
	if err != nil {
		return err
	}
	tool, hasTool := ToolClaude, codex || claude
	if codex {
		tool = ToolCodex
	}
	if len(rest) == 0 {
		return runTUI(store, tool, hasTool)
	}
	cmd, args := rest[0], rest[1:]
	one := func() (string, error) {
		if len(args) != 1 || strings.HasPrefix(args[0], "-") && len(args[0]) > 1 && !isNumber(args[0]) {
			return "", fmt.Errorf("error: 用法: ccp [-x] %s <NAME>", cmd)
		}
		return args[0], nil
	}
	find := func(name string) (int, error) {
		if i := store.Get(tool).Find(name); i >= 0 {
			return i, nil
		}
		return -1, fmt.Errorf("找不到 provider: %s", name)
	}
	switch cmd {
	case "list", "ls":
		return printList(store, tool)
	case "current":
		b := store.Get(tool)
		i, err := b.Active()
		if err != nil {
			return err
		}
		if i < 0 {
			fmt.Println("(未匹配到已保存的 provider)")
		} else {
			fmt.Println(b.Providers[i].Name())
		}
	case "use":
		name, err := one()
		if err != nil {
			return err
		}
		i, err := find(name)
		if err != nil {
			return err
		}
		note, err := store.Apply(tool, i)
		if err != nil {
			return err
		}
		b := store.Get(tool)
		fmt.Printf("✔ %s 已切换到 %s  →  %s（新开会话生效）\n", b.Label(), b.Providers[i].Name(), b.Target())
		if note != "" {
			fmt.Println("  " + note)
		}
	case "add":
		a, err := parseAddArgs(args)
		if err != nil {
			return err
		}
		if a == nil {
			fmt.Print(usageAdd)
			return nil
		}
		return cliAdd(store, tool, a)
	case "set":
		a, err := parseAddArgs(args)
		if err != nil {
			return err
		}
		if a == nil || !a.hasText {
			fmt.Print(usageSet)
			return nil
		}
		i, err := find(a.text)
		if err != nil {
			return err
		}
		return cliSet(store, tool, i, a)
	case "rm":
		name, err := one()
		if err != nil {
			return err
		}
		i, err := find(name)
		if err != nil {
			return err
		}
		pname := store.Get(tool).Providers[i].Name()
		if err := store.Remove(tool, i); err != nil {
			return err
		}
		fmt.Printf("✔ 已删除 %s\n", pname)
	default:
		return fmt.Errorf("error: unrecognized subcommand '%s'\n\n%s", cmd, usageMain)
	}
	return nil
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// parseAddArgs returns nil when help was asked for.
func parseAddArgs(args []string) (*addArgs, error) {
	a := &addArgs{env: NewOMap[string](), config: NewOMap[string](), given: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, inline, hasInline := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "--") {
			name, hasInline = arg, false
		}
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("error: %s 需要一个值", name)
			}
			i++
			return args[i], nil
		}
		var err error
		switch name {
		case "-h", "--help":
			return nil, nil
		case "-c", "--clip":
			a.clip = true
		case "--use":
			a.use = true
		case "--name":
			a.name, err = value()
			a.given["name"] = true
		case "--url":
			a.url, err = value()
			a.given["base_url"] = true
		case "--key":
			a.key, err = value()
			a.given["key"] = true
		case "--model":
			a.model, err = value()
			a.given["model"] = true
		case "--env", "--config":
			var kv string
			if kv, err = value(); err == nil {
				k, v, ok := strings.Cut(kv, "=")
				if k = strip(k); !ok || k == "" {
					return nil, fmt.Errorf("error: %s 的格式是 KEY=VALUE: %s", name, kv)
				}
				if name == "--env" {
					a.env.Set(k, v)
				} else {
					a.config.Set(k, strip(v))
				}
			}
		case "--unset-env", "--unset-config":
			var k string
			if k, err = value(); err == nil {
				if name == "--unset-env" {
					a.unsetEnv = append(a.unsetEnv, strip(k))
				} else {
					a.unsetConfig = append(a.unsetConfig, strip(k))
				}
			}
		default:
			if arg != "-" && strings.HasPrefix(arg, "-") {
				return nil, fmt.Errorf("error: unexpected argument '%s'\n\n%s", arg, usageAdd)
			}
			if a.hasText {
				return nil, fmt.Errorf("error: unexpected argument '%s'\n\n%s", arg, usageAdd)
			}
			a.text, a.hasText = arg, true
		}
		if err != nil {
			return nil, err
		}
	}
	return a, nil
}

func cliAdd(store *Store, tool Tool, a *addArgs) error {
	text := a.text
	if a.clip {
		text = readClipboard()
	} else if a.text == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(data)
	}
	b := store.Get(tool)
	info := NewOMap[Val]()
	if text != "" {
		info = b.Parse(text)
	}
	for _, kv := range [][2]string{{"name", a.name}, {"base_url", a.url}, {"key", a.key}, {"model", a.model}} {
		if kv[1] != "" {
			setInfo(info, kv[0], kv[1])
		}
	}
	if a.env.Len() > 0 {
		env := NewOMap[string]()
		if v, _ := info.Get("extra_env"); v.D != nil {
			env = v.D.Clone()
		}
		for _, k := range a.env.Keys() {
			v, _ := a.env.Get(k)
			env.Set(k, v)
		}
		info.Set("extra_env", Val{D: env})
	}
	if a.config.Len() > 0 {
		info.Set("extra_config", Val{D: a.config.Clone()})
	}
	// an endpoint needs url + key; a login-type provider is defined by its env / settings alone
	if (infoStr(info, "base_url") == "" || infoStr(info, "key") == "") && a.env.Len() == 0 && a.config.Len() == 0 {
		return errors.New("没有识别到 url 和 key（用 --url/--key，或者传入 JSON / TOML / export 文本）")
	}
	p := b.FromInfo(info)
	i := b.FindSame(p)
	if i >= 0 {
		fmt.Printf("已存在相同的 provider: %s\n", b.Providers[i].Name())
	} else {
		var err error
		if i, err = store.Upsert(tool, p, -1); err != nil {
			return err
		}
		fmt.Printf("✔ 已添加 %s 到 %s\n", b.Providers[i].Name(), b.Label())
	}
	if a.use {
		note, err := store.Apply(tool, i)
		if err != nil {
			return err
		}
		fmt.Printf("✔ 已切换到 %s\n", b.Providers[i].Name())
		if note != "" {
			fmt.Println("  " + note)
		}
	}
	return nil
}

// cliSet changes the given fields of a saved provider.
func cliSet(store *Store, tool Tool, idx int, a *addArgs) error {
	b := store.Get(tool)
	p := b.Providers[idx].Clone()
	if a.clip || a.use {
		return errors.New("error: set 不支持 --clip / --use")
	}
	for field, v := range map[string]string{"name": a.name, "base_url": a.url, "key": a.key, "model": a.model} {
		if a.given[field] {
			p.Set(field, v)
		}
	}
	if a.given["name"] {
		if p.Name() == "" {
			return errors.New("名称不能为空")
		}
		if b.UniqueName(p.Name(), idx) != p.Name() {
			return fmt.Errorf("名称 %s 已存在", p.Name())
		}
	}
	merge := func(field string, add *Dict, unset []string) error {
		if add.Len() == 0 && len(unset) == 0 {
			return nil
		}
		if !p.Has(field) {
			return fmt.Errorf("%s 的 provider 没有 %s", b.Label(), field)
		}
		d := p.Dict(field).Clone()
		for _, k := range unset {
			d.Del(k)
		}
		for _, k := range add.Keys() {
			v, _ := add.Get(k)
			d.Set(k, v)
		}
		p.SetDict(field, d)
		return nil
	}
	if err := merge("extra_env", a.env, a.unsetEnv); err != nil {
		return err
	}
	if err := merge("extra_config", a.config, a.unsetConfig); err != nil {
		return err
	}
	active, _ := b.Active()
	if _, err := store.Upsert(tool, p, idx); err != nil {
		return err
	}
	fmt.Println("✔ 已更新 " + p.Name())
	if active == idx {
		flag := ""
		if tool == ToolCodex {
			flag = " -x"
		}
		fmt.Printf("  它正在使用中：执行 ccp%s use %d 让改动生效", flag, idx+1)
		fmt.Println()
	}
	return nil
}
