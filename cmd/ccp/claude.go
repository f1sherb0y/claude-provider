// Claude Code backend: ~/.claude/settings.json -> env.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var claudeKeyVars = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}

// Env vars that always belong to "the provider" and are cleared on every switch.
var claudeManagedKeys = []string{
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

// claudeEnvField maps an env var to a Provider field; anything else lands in extra_env.
func claudeEnvField(k string) string {
	switch k {
	case "ANTHROPIC_BASE_URL":
		return "base_url"
	case "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY":
		return "key"
	case "ANTHROPIC_MODEL":
		return "model"
	case "ANTHROPIC_DEFAULT_HAIKU_MODEL":
		return "haiku_model"
	}
	return ""
}

var claudeFields = []Field{
	{Name: "name", Label: "名称"},
	{Name: "base_url", Label: "Base URL", Placeholder: "https://api.example.com（留空 = 官方登录）"},
	{Name: "key", Label: "Key", Kind: KindPassword},
	{Name: "key_var", Label: "Key 变量", Kind: KindSelect, Options: claudeKeyVars, Default: "ANTHROPIC_AUTH_TOKEN"},
	{Name: "model", Label: "Model", Placeholder: "ANTHROPIC_MODEL（可选）"},
	{Name: "haiku_model", Label: "Haiku", Placeholder: "ANTHROPIC_DEFAULT_HAIKU_MODEL（可选）"},
	{Name: "extra_env", Label: "额外 env", Kind: KindDict, Placeholder: "每行 KEY=VALUE"},
}

const claudePasteHint = "settings.json / env JSON / export / $env: / URL+Key"

func claudeSettingsFile() string {
	return filepath.Join(envDir("CLAUDE_CONFIG_DIR", ".claude"), "settings.json")
}

var claudeURLSuffixRe = regexp.MustCompile(`(?i)(/v1)?(/messages)?$`)

// Claude Code appends /v1/messages itself, so strip API path suffixes.
func claudeNormalizeBaseURL(url string) string {
	url = strings.TrimRight(strip(url), "/")
	if url == "" {
		return ""
	}
	return claudeURLSuffixRe.ReplaceAllString(url, "")
}

func claudeToEnv(p *Provider) *Dict {
	env := NewOMap[string]()
	if p.BaseURL() != "" {
		env.Set("ANTHROPIC_BASE_URL", claudeNormalizeBaseURL(p.BaseURL()))
	}
	if p.Key() != "" {
		env.Set(p.S("key_var"), p.Key())
	}
	if p.Model() != "" {
		env.Set("ANTHROPIC_MODEL", p.Model())
	}
	if p.S("haiku_model") != "" {
		env.Set("ANTHROPIC_DEFAULT_HAIKU_MODEL", p.S("haiku_model"))
	}
	extra := p.Dict("extra_env")
	for _, k := range extra.Keys() {
		v, _ := extra.Get(k)
		env.Set(k, v)
	}
	return env
}

func claudeFromEnv(env *Dict) *Info {
	out := NewOMap[Val]()
	extra := NewOMap[string]()
	for _, k := range env.Keys() {
		v, _ := env.Get(k)
		switch f := claudeEnvField(k); f {
		case "":
			extra.Set(k, v)
		case "key":
			// AUTH_TOKEN wins if both are present
			if !out.Has("key") || k == "ANTHROPIC_AUTH_TOKEN" {
				setInfo(out, "key", v)
				setInfo(out, "key_var", k)
			}
		default:
			setInfo(out, f, v)
		}
	}
	if extra.Len() > 0 {
		out.Set("extra_env", Val{D: extra})
	}
	return out
}

func claudeReadSettings() (*JObj, error) {
	path := claudeSettingsFile()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewOMap[any](), nil
	}
	if err != nil {
		return nil, err
	}
	v, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*JObj)
	if !ok {
		return nil, fmt.Errorf("%s: 不是 JSON 对象", path)
	}
	return obj, nil
}

func claudeSettingsEnv(settings *JObj) *JObj {
	if m, ok := settings.m["env"].(*JObj); ok {
		return m.Clone()
	}
	return NewOMap[any]()
}

// claudeBootstrap runs on first launch: official login entry + whatever is configured right now.
func claudeBootstrap(b *Backend) error {
	official := NewProvider(claudeFields)
	official.Set("name", "Anthropic 官方")
	b.Providers = []*Provider{official}
	settings, err := claudeReadSettings()
	if err != nil {
		return err
	}
	env := claudeSettingsEnv(settings)
	base, _ := env.m["ANTHROPIC_BASE_URL"].(string)
	if base == "" {
		return nil
	}
	subset := NewOMap[string]()
	for _, k := range env.Keys() {
		if claudeEnvField(k) != "" {
			subset.Set(k, jsonText(env.m[k]))
		}
	}
	p := NewProvider(claudeFields)
	p.Set("name", nameFromURL(base))
	info := claudeFromEnv(subset)
	for _, k := range info.Keys() {
		v, _ := info.Get(k)
		p.vals.Set(k, v)
	}
	b.Providers = append([]*Provider{p}, b.Providers...)
	var keys []string
	for _, k := range env.Keys() {
		if slices.Contains(claudeManagedKeys, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	b.AppliedKeys = keys
	return nil
}

// envEq reports whether env[k] is exactly the string want (or absent when !has).
func envEq(env *JObj, k, want string, has bool) bool {
	v, ok := env.Get(k)
	if !ok || !has {
		return !ok && !has
	}
	s, isStr := v.(string)
	return isStr && s == want
}

func claudeActive(b *Backend) (int, error) {
	settings, err := claudeReadSettings()
	if err != nil {
		return -1, err
	}
	env := claudeSettingsEnv(settings)
	matches := func(pe *Dict, keys []string) bool {
		for _, k := range keys {
			v, has := pe.Get(k)
			if !envEq(env, k, v, has) {
				return false
			}
		}
		return true
	}
	// several entries can match exactly (an official login matches whenever no
	// endpoint is set): the one that pins down the most variables wins
	best, bestLen := -1, -1
	for i, p := range b.Providers {
		pe := claudeToEnv(p)
		if matches(pe, claudeManagedKeys) && matches(pe, pe.Keys()) && pe.Len() > bestLen {
			best, bestLen = i, pe.Len()
		}
	}
	if best >= 0 {
		return best, nil
	}
	// looser match: same url + key, other params drifted
	for i, p := range b.Providers {
		if matches(claudeToEnv(p), []string{"ANTHROPIC_BASE_URL", p.S("key_var")}) {
			return i, nil
		}
	}
	return -1, nil
}

func claudeApply(b *Backend, p *Provider) error {
	settings, err := claudeReadSettings()
	if err != nil {
		return err
	}
	env := claudeSettingsEnv(settings)
	for _, k := range claudeManagedKeys {
		env.Del(k)
	}
	for _, k := range b.AppliedKeys {
		env.Del(k)
	}
	for _, k := range b.Shadowed.Keys() {
		env.Set(k, b.Shadowed.m[k]) // restore user values a previous provider overrode
	}
	// Only take ownership of keys we actually change, so a provider that repeats
	// a global setting (e.g. DISABLE_NONESSENTIAL_TRAFFIC=1) won't delete it later.
	want := claudeToEnv(p)
	changed := NewOMap[string]()
	for _, k := range want.Keys() {
		v, _ := want.Get(k)
		if slices.Contains(claudeManagedKeys, k) || !envEq(env, k, v, true) {
			changed.Set(k, v)
		}
	}
	shadowed := NewOMap[any]()
	for _, k := range changed.Keys() {
		if old, ok := env.Get(k); ok && !slices.Contains(claudeManagedKeys, k) {
			shadowed.Set(k, old)
		}
	}
	for _, k := range changed.Keys() {
		v, _ := changed.Get(k)
		env.Set(k, v)
	}
	settings.Set("env", env)
	target := claudeSettingsFile()
	if err := backup(target); err != nil {
		return err
	}
	if err := writeJSON(target, settings); err != nil {
		return err
	}
	b.Shadowed = shadowed
	b.AppliedKeys = slices.Sorted(slices.Values(changed.Keys()))
	return nil
}

// claudeParse reads settings.json ({"env": {...}}), flat env JSON, shell env scripts, or generic URL+key.
func claudeParse(text string) *Info {
	text = strip(text)
	var env *Dict
	if data := loadJSONObj(text); data != nil {
		if m, ok := data.m["env"].(*JObj); ok {
			data = m
		}
		env = NewOMap[string]()
		for _, k := range data.Keys() {
			env.Set(k, jsonText(data.m[k]))
		}
	} else {
		env = parseEnvScript(text)
	}
	known := slices.ContainsFunc(env.Keys(), func(k string) bool { return claudeEnvField(k) != "" })
	var out *Info
	if known {
		out = claudeFromEnv(env)
	} else {
		out = parseGeneric(text)
	}
	if url := infoStr(out, "base_url"); url != "" {
		url = claudeNormalizeBaseURL(url)
		setInfo(out, "base_url", url)
		if !out.Has("name") {
			setInfo(out, "name", nameFromURL(url))
		}
	}
	return out
}
