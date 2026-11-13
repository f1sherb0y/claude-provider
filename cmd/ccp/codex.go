// Codex backend: ~/.codex/config.toml (model_provider + [model_providers.<id>]),
// ~/.codex/.env (per-provider variables) and ~/.codex/auth.json (per-login snapshots).
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

var codexWireAPIs = []string{"responses", "chat"}

var codexFields = []Field{
	{Name: "name", Label: "名称"},
	{Name: "id", Label: "配置 ID", Placeholder: "留空自动生成（model_providers 的键 / 登录存档名）"},
	{Name: "base_url", Label: "Base URL", Placeholder: "https://api.example.com/v1（留空 = ChatGPT 登录）"},
	{Name: "key", Label: "Key", Kind: KindPassword},
	{Name: "wire_api", Label: "wire_api", Kind: KindSelect, Options: codexWireAPIs, Default: "responses"},
	{Name: "model", Label: "model", Placeholder: "可选，留空 = 不改当前模型"},
	{Name: "env_key", Label: "env_key", Placeholder: "可选，填了则 Key 写入 ~/.codex/.env"},
	{Name: "extra_env", Label: "额外 env", Kind: KindDict, Placeholder: "每行 KEY=VALUE，写入 ~/.codex/.env（代理、证书等）"},
	{Name: "extra_config", Label: "额外配置", Kind: KindDict, Placeholder: "每行 键=TOML值，写入 config.toml，切走时还原，如 features.x.enabled=true"},
}

const codexPasteHint = "[model_providers.x] TOML / export OPENAI_* / JSON / URL+Key"

// The built-in provider every login-type entry selects.
const codexBuiltin = "openai"

const (
	ccpBlockBegin = "# BEGIN ccp (managed)"
	ccpBlockEnd   = "# END ccp (managed)"
)

func codexHome() string       { return envDir("CODEX_HOME", ".codex") }
func codexConfigFile() string { return filepath.Join(codexHome(), "config.toml") }
func codexDotenvFile() string { return filepath.Join(codexHome(), ".env") }
func codexAuthFile() string   { return filepath.Join(codexHome(), "auth.json") }
func codexAuthDir() string    { return filepath.Join(configDir(), "codex-auth") }

// isLogin: no endpoint of its own, so Codex signs in through auth.json.
func isLogin(p *Provider) bool { return p.BaseURL() == "" }

var codexEndpointRe = regexp.MustCompile(`(?i)/(responses|chat/completions)$`)

// Codex wants the API root (usually ending in /v1); drop endpoint paths people paste.
func codexNormalizeBaseURL(url string) string {
	url = strings.TrimRight(strip(url), "/")
	if url == "" {
		return ""
	}
	return codexEndpointRe.ReplaceAllString(url, "")
}

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func codexSlug(text string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(text), "_"), "_")
	if s == "" {
		return "provider"
	}
	return s
}

func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

func codexReadConfig() (string, map[string]any, error) {
	src, err := readText(codexConfigFile())
	if err != nil {
		return "", nil, err
	}
	m, err := tomlLoad(src)
	return src, m, err
}

// ensureIDs gives every provider an id: the model_providers key for an endpoint,
// the name of the auth.json snapshot for a login.
func ensureIDs(b *Backend) {
	for _, p := range b.Providers {
		if p.S("id") == "" {
			p.Set("id", b.uniqueID(codexSlug(p.Name()), p))
		}
	}
}

// ---------------------------------------------------------------- backend

// codexBootstrap runs on first launch: import every [model_providers.*] table plus an official entry.
func codexBootstrap(b *Backend) error {
	src, m, err := codexReadConfig()
	if err != nil {
		return err
	}
	doc, err := parseTOML(src)
	if err != nil {
		return err
	}
	envText, _ := readText(codexDotenvFile())
	dotenv := parseDotenv(envText)
	current := tomlStr(m, "model_provider")
	b.Providers = nil
	for _, pid := range doc.childTables("model_providers", m) {
		envKey := tomlStr(m, "model_providers", pid, "env_key")
		key := tomlStr(m, "model_providers", pid, "experimental_bearer_token")
		if key == "" && envKey != "" {
			if key, _ = dotenv.Get(envKey); key == "" {
				key = os.Getenv(envKey)
			}
		}
		p := NewProvider(codexFields)
		name := tomlStr(m, "model_providers", pid, "name")
		if name == "" {
			name = pid
		}
		p.Set("name", name)
		p.Set("id", pid)
		p.Set("base_url", tomlStr(m, "model_providers", pid, "base_url"))
		p.Set("key", key)
		if w := tomlStr(m, "model_providers", pid, "wire_api"); w != "" {
			p.Set("wire_api", w)
		}
		if pid == current {
			p.Set("model", tomlStr(m, "model"))
		}
		p.Set("env_key", envKey)
		b.Providers = append(b.Providers, p)
	}
	slices.SortStableFunc(b.Providers, func(x, y *Provider) int {
		rank := func(p *Provider) int {
			if p.S("id") == current {
				return 0
			}
			return 1
		}
		return rank(x) - rank(y)
	})
	official := NewProvider(codexFields)
	official.Set("name", "OpenAI 官方 (ChatGPT 登录)")
	official.Set("id", b.uniqueID(codexBuiltin, nil))
	b.Providers = append(b.Providers, official)
	return nil
}

// envMatches: every variable the provider declares is set to that value.
func envMatches(p *Provider, env *Dict) bool {
	want := p.Dict("extra_env")
	for _, k := range want.Keys() {
		v, _ := want.Get(k)
		if cur, ok := env.Get(k); !ok || cur != v {
			return false
		}
	}
	return true
}

func codexActive(b *Backend) (int, error) {
	_, m, err := codexReadConfig()
	if err != nil {
		return -1, err
	}
	current := tomlStr(m, "model_provider")
	if current != "" && current != codexBuiltin {
		for i, p := range b.Providers {
			if !isLogin(p) && p.S("id") == current {
				return i, nil
			}
		}
		return -1, nil
	}
	// Several logins share the built-in provider; what tells them apart is the
	// environment they need (the most specific match wins), then whose auth.json is in place.
	envText, _ := readText(codexDotenvFile())
	env := parseDotenv(envText)
	best, bestScore := []int{}, -1
	for i, p := range b.Providers {
		if !isLogin(p) || !envMatches(p, env) {
			continue
		}
		switch score := p.Dict("extra_env").Len(); {
		case score > bestScore:
			best, bestScore = []int{i}, score
		case score == bestScore:
			best = append(best, i)
		}
	}
	if len(best) == 0 {
		return -1, nil
	}
	if len(best) > 1 {
		if auth, err := os.ReadFile(codexAuthFile()); err == nil {
			if id := authIdentity(auth); id != "" {
				for _, i := range best {
					if snap, err := os.ReadFile(snapshotPath(b.Providers[i])); err == nil && authIdentity(snap) == id {
						return i, nil
					}
				}
			}
		}
	}
	return best[0], nil
}

// codexApply points Codex at provider idx.
func codexApply(b *Backend, idx int) (string, error) {
	ensureIDs(b)
	p := b.Providers[idx]
	src, _, err := codexReadConfig()
	if err != nil {
		return "", err
	}
	active, err := codexActive(b)
	if err != nil {
		return "", err
	}

	// work everything out first, so a problem leaves all three files untouched
	newSrc, cfgApplied, cfgShadowed, err := codexEditConfig(src, b, p)
	if err != nil {
		return "", err
	}
	envPath := codexDotenvFile()
	oldEnv, err := readText(envPath)
	if err != nil {
		return "", err
	}
	newEnv, applied, shadowed := codexEditEnv(oldEnv, b, p)

	note, err := codexSyncAuth(b, active, p)
	if err != nil {
		return "", err
	}
	if newEnv != codexEditEnvNormal(oldEnv) {
		if err := backup(envPath); err != nil {
			return "", err
		}
		if err := atomicWrite(envPath, []byte(newEnv)); err != nil {
			return "", err
		}
	}
	if newSrc != src {
		target := codexConfigFile()
		if err := backup(target); err != nil {
			return "", err
		}
		if err := atomicWrite(target, []byte(newSrc)); err != nil {
			return "", err
		}
	}
	b.AppliedKeys, b.Shadowed = applied, shadowed
	b.ConfigApplied, b.ConfigShadowed = cfgApplied, cfgShadowed
	return note, nil
}

// codexWant lists what an endpoint's provider table must say, and the key it must not have.
func codexWant(p *Provider) (id string, want map[string]string, drop string) {
	id, want = codexBuiltin, map[string]string{}
	if isLogin(p) {
		return id, want, ""
	}
	id = p.S("id")
	want["name"], want["base_url"], want["wire_api"] = p.Name(), p.BaseURL(), p.S("wire_api")
	drop = "env_key"
	if envKey := p.S("env_key"); envKey != "" {
		want["env_key"], drop = envKey, "experimental_bearer_token"
	} else {
		want["experimental_bearer_token"] = p.Key()
	}
	return id, want, drop
}

// codexEditConfig returns config.toml with the provider selected, plus the
// bookkeeping for its extra settings: which keys it set and what they held before
// (TOML source, or nil when the key did not exist).
func codexEditConfig(src string, b *Backend, p *Provider) (string, []string, *JObj, error) {
	fail := func(err error) (string, []string, *JObj, error) { return "", nil, nil, err }
	doc, err := parseTOML(src)
	if err != nil {
		return fail(err)
	}

	// 1. put back what the previous provider's extra settings replaced
	for i := len(b.ConfigApplied) - 1; i >= 0; i-- {
		key := b.ConfigApplied[i]
		path, err := parseKeyPath(key)
		if err != nil {
			continue // cannot have been applied
		}
		if prev, ok := b.ConfigShadowed.m[key].(string); ok {
			err = doc.SetRaw(path, prev)
		} else {
			err = doc.RemovePruning(path)
		}
		if err != nil {
			return fail(err)
		}
	}

	// 2. the provider itself
	id, want, drop := codexWant(p)
	if !isLogin(p) {
		table := []string{"model_providers", id}
		// update in place so unrelated keys (env_key_instructions, headers, ...) survive
		for _, k := range []string{"name", "base_url", "wire_api", "env_key", "experimental_bearer_token"} {
			if v, ok := want[k]; ok {
				if err := doc.SetString(append(slices.Clone(table), k), v); err != nil {
					return fail(err)
				}
			}
		}
		if err := doc.Remove(append(slices.Clone(table), drop)); err != nil {
			return fail(err)
		}
	}
	// written out even for the built-in provider: tools that manage a login
	// (and people reading the file) want to see which provider is selected
	if err := doc.SetString([]string{"model_provider"}, id); err != nil {
		return fail(err)
	}
	if p.Model() != "" {
		if err := doc.SetString([]string{"model"}, p.Model()); err != nil {
			return fail(err)
		}
	}

	// 3. its extra settings: any key, any TOML value; we do not interpret them
	extra := p.Dict("extra_config")
	shadowed := NewOMap[any]()
	wantExtra := map[string]any{}
	for _, key := range extra.Keys() {
		path, err := parseKeyPath(key)
		if err != nil {
			return fail(fmt.Errorf("额外配置 %s: %w", key, err))
		}
		if path[0] == "model_provider" || path[0] == "model_providers" {
			return fail(fmt.Errorf("额外配置不能设置 %s（由 provider 本身决定）", key))
		}
		text, _ := extra.Get(key)
		raw, value := tomlValue(text)
		if prev, ok := doc.Raw(path); ok {
			shadowed.Set(key, prev)
		} else {
			shadowed.Set(key, nil)
		}
		if err := doc.SetRaw(path, raw); err != nil {
			return fail(fmt.Errorf("额外配置 %s: %w", key, err))
		}
		wantExtra[key] = value
	}

	// never write a file that does not say what we meant
	m, err := tomlLoad(doc.src)
	if err != nil {
		return fail(fmt.Errorf("生成的 config.toml 无效，未写入: %w", err))
	}
	ok := tomlStr(m, "model_provider") == id && (p.Model() == "" || tomlStr(m, "model") == p.Model())
	for k, v := range want {
		ok = ok && tomlStr(m, "model_providers", id, k) == v
	}
	for key, value := range wantExtra {
		path, _ := parseKeyPath(key)
		got, found := tomlGet(m, path...)
		ok = ok && found && reflect.DeepEqual(got, value)
	}
	if !ok {
		return fail(errors.New("生成的 config.toml 与预期不符，未写入"))
	}
	return doc.src, slices.Clone(extra.Keys()), shadowed, nil
}

// ---------------------------------------------------------------- .env

type envLine struct {
	raw, key, val string
	ok            bool
}

func codexEditEnvNormal(text string) string {
	lines := splitLines(text)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// codexEditEnv returns the .env for target: variables that belonged to another
// provider are taken out, values they had overridden come back, and target's own
// variables go into a marked block. Also returns the bookkeeping to store.
func codexEditEnv(text string, b *Backend, target *Provider) (string, []string, *JObj) {
	var lines []envLine
	for _, raw := range splitLines(text) {
		l := envLine{raw: raw}
		l.key, l.val, l.ok = parseDotenvLine(raw)
		lines = append(lines, l)
	}
	want := target.Dict("extra_env")
	declared := func(k, v string) bool {
		for _, q := range b.Providers {
			if x, ok := q.Dict("extra_env").Get(k); ok && x == v {
				return true
			}
		}
		return false
	}
	last := func(k string) (string, bool) {
		for i := len(lines) - 1; i >= 0; i-- {
			if lines[i].ok && lines[i].key == k {
				return lines[i].val, true
			}
		}
		return "", false
	}
	drop := func(k string) {
		lines = slices.DeleteFunc(lines, func(l envLine) bool { return l.ok && l.key == k })
	}
	// new lines go ahead of our block (or at the end when there is none)
	appendPlain := func(k, v string) {
		l := envLine{raw: k + "=" + dotenvQuote(v), key: k, val: v, ok: true}
		at := slices.IndexFunc(lines, func(l envLine) bool { return l.raw == ccpBlockBegin })
		if at < 0 {
			at = len(lines)
		} else if at > 0 && strings.TrimSpace(lines[at-1].raw) == "" {
			at--
		}
		lines = slices.Insert(lines, at, l)
	}

	// 1. variables a provider put there (ours, or the same value written by the
	//    provider's own installer) go, unless target wants exactly that
	lines = slices.DeleteFunc(lines, func(l envLine) bool {
		if !l.ok {
			return false
		}
		if v, ok := want.Get(l.key); ok && v == l.val {
			return false
		}
		return slices.Contains(b.AppliedKeys, l.key) || declared(l.key, l.val)
	})
	// 2. give back what the previous provider had overridden
	for _, k := range b.Shadowed.Keys() {
		if _, ok := last(k); !ok {
			appendPlain(k, jsonText(b.Shadowed.m[k]))
		}
	}
	// 3. target's variables
	shadowed := NewOMap[any]()
	var add []envLine
	for _, k := range want.Keys() {
		v, _ := want.Get(k)
		if cur, ok := last(k); ok {
			if cur == v {
				continue
			}
			shadowed.Set(k, cur)
			drop(k)
		}
		add = append(add, envLine{raw: k + "=" + dotenvQuote(v), key: k, val: v, ok: true})
	}
	// 4. the key of an env_key provider
	if envKey := target.S("env_key"); envKey != "" && target.Key() != "" && !isLogin(target) {
		raw := envKey + "=" + dotenvQuote(target.Key())
		set := false
		for i := len(lines) - 1; i >= 0 && !set; i-- {
			if lines[i].ok && lines[i].key == envKey {
				lines[i].raw, lines[i].val, set = raw, target.Key(), true
			}
		}
		if !set {
			appendPlain(envKey, target.Key())
		}
	}
	// 5. our block
	begin := slices.IndexFunc(lines, func(l envLine) bool { return l.raw == ccpBlockBegin })
	end := slices.IndexFunc(lines, func(l envLine) bool { return l.raw == ccpBlockEnd })
	if begin >= 0 && end > begin {
		lines = slices.Insert(lines, end, add...)
		if end+len(add) == begin+1 { // nothing left inside
			lines = slices.Delete(lines, begin, begin+2)
			if begin > 0 && strings.TrimSpace(lines[begin-1].raw) == "" {
				lines = slices.Delete(lines, begin-1, begin)
			}
		}
	} else if len(add) > 0 {
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1].raw) != "" {
			lines = append(lines, envLine{})
		}
		lines = append(lines, envLine{raw: ccpBlockBegin})
		lines = append(lines, add...)
		lines = append(lines, envLine{raw: ccpBlockEnd})
	}

	var out strings.Builder
	for _, l := range lines {
		out.WriteString(l.raw)
		out.WriteByte('\n')
	}
	return out.String(), slices.Clone(want.Keys()), shadowed
}

// ---------------------------------------------------------------- auth.json

var unsafeFileRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func snapshotPath(p *Provider) string {
	return filepath.Join(codexAuthDir(), unsafeFileRe.ReplaceAllString(p.S("id"), "_")+".json")
}

// authIdentity names the account an auth.json signs in as ("" if it cannot tell).
func authIdentity(data []byte) string {
	obj := loadJSONObj(string(data))
	if obj == nil {
		return ""
	}
	if tokens, ok := obj.m["tokens"].(*JObj); ok {
		if id, _ := tokens.m["account_id"].(string); id != "" {
			return "account:" + id
		}
	}
	if key, _ := obj.m["OPENAI_API_KEY"].(string); key != "" {
		return fmt.Sprintf("apikey:%x", sha256.Sum256([]byte(key)))
	}
	return ""
}

// saveSnapshot stores data as p's login, keeping a backup when it replaces another account's.
func saveSnapshot(p *Provider, data []byte) error {
	path := snapshotPath(p)
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err == nil && authIdentity(old) != authIdentity(data) {
		if err := backup(path); err != nil {
			return err
		}
	}
	return atomicWrite(path, data)
}

// codexSyncAuth keeps one auth.json per login-type provider. Whatever is in
// ~/.codex/auth.json is first saved to the login it belongs to (Codex refreshes
// tokens in place, so the file is always newer than our copy); then, if target is
// a login that is not the one in place, its own snapshot is put there.
// active is the provider in use before the switch (-1 if unknown).
func codexSyncAuth(b *Backend, active int, target *Provider) (string, error) {
	authPath := codexAuthFile()
	cur, err := os.ReadFile(authPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	have := err == nil
	var owner *Provider
	if have {
		if id := authIdentity(cur); id != "" {
			for _, p := range b.Providers {
				if !isLogin(p) {
					continue
				}
				if snap, err := os.ReadFile(snapshotPath(p)); err == nil && authIdentity(snap) == id {
					if err := saveSnapshot(p, cur); err != nil {
						return "", err
					}
					if owner == nil {
						owner = p
					}
				}
			}
		}
		// nobody recognises it: it is the login of the provider that was in use
		if owner == nil && active >= 0 && isLogin(b.Providers[active]) {
			owner = b.Providers[active]
			if err := saveSnapshot(owner, cur); err != nil {
				return "", err
			}
		}
	}
	if !isLogin(target) || owner == target {
		return "", nil
	}
	snap, err := os.ReadFile(snapshotPath(target))
	switch {
	case err == nil:
		if have && bytes.Equal(snap, cur) {
			return "", nil
		}
		if err := backup(authPath); err != nil {
			return "", err
		}
		if err := atomicWrite(authPath, snap); err != nil {
			return "", err
		}
		return fmt.Sprintf("已恢复「%s」的登录状态 (auth.json)", target.Name()), nil
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	case !have:
		return "", nil
	case owner == nil:
		// first use of this login: what is there now is its login
		return "", saveSnapshot(target, cur)
	}
	// the file in place is another provider's login and target has none saved
	if err := backup(authPath); err != nil {
		return "", err
	}
	if err := os.Remove(authPath); err != nil {
		return "", err
	}
	return fmt.Sprintf("「%s」还没有保存过登录状态；当前 auth.json 属于「%s」，已存档并移走，请运行 codex login",
		target.Name(), owner.Name()), nil
}

// ---------------------------------------------------------------- parsing

// codexParse reads `[model_providers.x]` TOML, `export OPENAI_*`, JSON, or generic URL+key.
func codexParse(text string) *Info {
	text = strip(text)
	out := NewOMap[Val]()
	handled := false
	if data, err := tomlLoad(text); err == nil && len(data) > 0 {
		var table map[string]any
		if doc, err := parseTOML(text); err == nil {
			if ids := doc.childTables("model_providers", data); len(ids) > 0 {
				setInfo(out, "id", ids[0])
				t, _ := tomlGet(data, "model_providers", ids[0])
				table, _ = t.(map[string]any)
			}
		}
		if table == nil && !out.Has("id") {
			if _, ok := data["base_url"]; ok {
				table = data
			}
		}
		if table != nil {
			handled = true
			for _, pair := range [][2]string{
				{"name", "name"},
				{"base_url", "base_url"},
				{"wire_api", "wire_api"},
				{"env_key", "env_key"},
				{"experimental_bearer_token", "key"},
			} {
				if s, ok := table[pair[0]].(string); ok {
					setInfo(out, pair[1], s)
				}
			}
			if s, ok := data["model"].(string); ok {
				setInfo(out, "model", s)
			}
		}
	}
	if !handled {
		var env *Dict
		if obj := loadJSONObj(text); obj != nil && obj.Len() > 0 {
			env = NewOMap[string]()
			for _, k := range obj.Keys() {
				env.Set(k, jsonText(obj.m[k]))
			}
		} else {
			env = parseEnvScript(text)
		}
		if url, _ := env.Get("OPENAI_BASE_URL"); url != "" {
			out = NewOMap[Val]()
			setInfo(out, "base_url", url)
			for _, pair := range [][2]string{{"OPENAI_API_KEY", "key"}, {"OPENAI_MODEL", "model"}} {
				if v, _ := env.Get(pair[0]); v != "" {
					setInfo(out, pair[1], v)
				}
			}
		} else {
			out = parseGeneric(text)
		}
	}
	if url := infoStr(out, "base_url"); url != "" {
		url = codexNormalizeBaseURL(url)
		setInfo(out, "base_url", url)
		if !out.Has("name") {
			setInfo(out, "name", nameFromURL(url))
		}
	}
	return out
}
