package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("\n got: %v\nwant: %v", got, want)
	}
}

// sandbox points every config directory at a temp dir.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROVIDER_HOME", filepath.Join(dir, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	return dir
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "<missing>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func codexProvider(kv ...string) *Provider {
	p := NewProvider(codexFields)
	for i := 0; i < len(kv); i += 2 {
		p.Set(kv[i], kv[i+1])
	}
	return p
}

func dictOf(kv ...string) *Dict {
	d := NewOMap[string]()
	for i := 0; i < len(kv); i += 2 {
		d.Set(kv[i], kv[i+1])
	}
	return d
}

// ---------------------------------------------------------------- common

func TestMask(t *testing.T) {
	eq(t, mask(""), "-")
	eq(t, mask("abcdef"), "abc…")
	eq(t, mask("sk-RoGTabcdefghijklmnopqrstH0Kg"), "sk-RoGT…H0Kg")
}

func TestNameFromURL(t *testing.T) {
	eq(t, nameFromURL("https://api.foo.com/v1"), "foo.com")
	eq(t, nameFromURL("https://www.fastaitoken.com"), "fastaitoken.com")
	eq(t, nameFromURL("nowcoding.ai:8080/x"), "nowcoding.ai")
	eq(t, nameFromURL("https://user@API.Example.com:1"), "example.com")
}

func TestEnvScript(t *testing.T) {
	env := parseEnvScript("export A=\"1\"\r\n$env:B='x y';\nset C = 3\n# no\n")
	eq(t, strings.Join(env.Keys(), ","), "A,B,C")
	b, _ := env.Get("B")
	eq(t, b, "x y")
}

func TestGeneric(t *testing.T) {
	out := parseGeneric("url: https://a.b/v1, key sk-abcdefghij12345")
	eq(t, infoStr(out, "base_url"), "https://a.b/v1")
	eq(t, infoStr(out, "key"), "sk-abcdefghij12345")
	out = parseGeneric(`{"URL": "https://x", "Token": "t", "model": 1}`)
	eq(t, strings.Join(out.Keys(), ","), "base_url,key")
}

func TestStems(t *testing.T) {
	for _, c := range [][3]string{{"settings.json", "settings", ".json"}, {".env", ".env", ""}, {"a.tar.gz", "a.tar", ".gz"}} {
		stem, suffix := stemSuffix(c[0])
		eq(t, stem, c[1])
		eq(t, suffix, c[2])
	}
}

func TestJSONKeepsOrderAndFormat(t *testing.T) {
	src := "{\n  \"z\": 1,\n  \"a\": {\n    \"名称\": \"<b> & \\\"q\\\"\",\n    \"n\": 1.50,\n    \"e\": [],\n    \"o\": {}\n  },\n  \"l\": [\n    true,\n    null\n  ]\n}\n"
	v, err := decodeJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, toJSON(v), src)
	if _, err := decodeJSON([]byte(`{"a": 1} x`)); err == nil {
		t.Error("trailing data accepted")
	}
}

// ---------------------------------------------------------------- text

func TestTruncate(t *testing.T) {
	eq(t, truncate("OpenAI 官方 (ChatGPT 登录)", 24), "OpenAI 官方 (ChatGPT 登…")
	eq(t, truncate("Now Coding 全球加速", 14), "Now Coding 全…")
	eq(t, truncate("OpenAI 官方 (ChatGPT 登录)", 14), "OpenAI 官方 (…")
	eq(t, truncate("abc", 3), "abc")
}

func TestWrap(t *testing.T) {
	s := []rune("⚡ 粘贴 settings.json / env JSON / export / $env: / URL+Key，自动识别")
	var lines []string
	for _, r := range wrap(s, 46) {
		lines = append(lines, string(s[r[0]:r[1]]))
	}
	eq(t, strings.Join(lines, "|"), "⚡ 粘贴 settings.json / env JSON / export / |$env: / URL+Key，自动识别")
	eq(t, len(wrap([]rune("已识别：base_url, key, key_var, extra_env, name"), 46)), 2)
	if got := wrap([]rune("abcdefgh"), 3); !slices.Equal(got, [][2]int{{0, 3}, {3, 6}, {6, 8}}) {
		t.Errorf("fold: %v", got)
	}
	if got := wrap(nil, 3); !slices.Equal(got, [][2]int{{0, 0}}) {
		t.Errorf("empty: %v", got)
	}
}

// ---------------------------------------------------------------- claude

func TestClaudeNormalize(t *testing.T) {
	eq(t, claudeNormalizeBaseURL(" https://a.b/v1/messages/ "), "https://a.b")
	eq(t, claudeNormalizeBaseURL("https://a.b/V1"), "https://a.b")
	eq(t, claudeNormalizeBaseURL("https://a.b/messages/v1"), "https://a.b/messages")
	eq(t, claudeNormalizeBaseURL(""), "")
}

func TestClaudeParseSettings(t *testing.T) {
	out := claudeParse(`{"env": {"ANTHROPIC_API_KEY": "k1", "ANTHROPIC_AUTH_TOKEN": "k2", "ANTHROPIC_BASE_URL": "https://api.x.com/v1", "T": 5}}`)
	eq(t, strings.Join(out.Keys(), ","), "key,key_var,base_url,extra_env,name")
	eq(t, infoStr(out, "key"), "k2")
	eq(t, infoStr(out, "base_url"), "https://api.x.com")
	eq(t, infoStr(out, "name"), "x.com")
	extra, _ := out.Get("extra_env")
	eq(t, dictText(extra.D), "T=5")
}

func TestClaudeSwitching(t *testing.T) {
	dir := sandbox(t)
	settings := filepath.Join(dir, "claude", "settings.json")
	write(t, settings, "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"KEEP\": \"1\",\n    \"http_proxy\": \"http://user:7890\"\n  }\n}\n")
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	b := store.Get(ToolClaude)
	relay := NewProvider(claudeFields)
	relay.Set("name", "relay")
	relay.Set("base_url", "https://relay.example/v1")
	relay.Set("key", "sk-1")
	proxy := NewProvider(claudeFields)
	proxy.Set("name", "proxy login")
	proxy.SetDict("extra_env", dictOf("http_proxy", "http://127.0.0.1:1", "KEEP", "1", "TOKEN", "t"))
	store.Upsert(ToolClaude, relay, -1)
	store.Upsert(ToolClaude, proxy, -1)

	apply := func(name string) {
		t.Helper()
		if _, err := store.Apply(ToolClaude, b.Find(name)); err != nil {
			t.Fatal(err)
		}
		if i, _ := b.Active(); i != b.Find(name) {
			t.Errorf("active after %s = %d", name, i)
		}
	}
	apply("relay")
	eq(t, read(t, settings), "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"KEEP\": \"1\",\n    \"http_proxy\": \"http://user:7890\",\n    \"ANTHROPIC_BASE_URL\": \"https://relay.example\",\n    \"ANTHROPIC_AUTH_TOKEN\": \"sk-1\"\n  }\n}\n")
	apply("proxy login")
	eq(t, read(t, settings), "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"KEEP\": \"1\",\n    \"http_proxy\": \"http://127.0.0.1:1\",\n    \"TOKEN\": \"t\"\n  }\n}\n")
	// the user's own proxy comes back, the repeated global stays
	apply("Anthropic 官方")
	eq(t, read(t, settings), "{\n  \"theme\": \"dark\",\n  \"env\": {\n    \"KEEP\": \"1\",\n    \"http_proxy\": \"http://user:7890\"\n  }\n}\n")
}

// ---------------------------------------------------------------- dotenv

func TestDotenvLines(t *testing.T) {
	cases := []struct {
		line, key, val string
		ok             bool
	}{
		{`A=1`, "A", "1", true},
		{`export A = "x y" # c`, "A", "x y", true},
		{`SSL_CERT_FILE="C:\\Users\\fish\\.claude\\saiai-ca.crt"`, "SSL_CERT_FILE", `C:\Users\fish\.claude\saiai-ca.crt`, true},
		{`P='C:\Users\a b'`, "P", `C:\Users\a b`, true},
		{`NO_PROXY=localhost,::1,fc00::/7,.local`, "NO_PROXY", "localhost,::1,fc00::/7,.local", true},
		{`E=`, "E", "", true},
		{`N="a\nb\$c"`, "N", "a\nb$c", true},
		{`# A=1`, "", "", false},
		{`A=x y`, "", "", false},
		{`A="open`, "", "", false},
		{``, "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseDotenvLine(c.line)
		if k != c.key || v != c.val || ok != c.ok {
			t.Errorf("%q -> %q %q %v", c.line, k, v, ok)
		}
	}
	for _, v := range []string{"", "plain", `C:\Users\fish\ca.crt`, `a "b" $c`, "x y#z", "two\nlines", "http://127.0.0.1:20759", "名"} {
		_, got, ok := parseDotenvLine("K=" + dotenvQuote(v))
		if !ok || got != v {
			t.Errorf("round trip %q -> %q -> %q %v", v, dotenvQuote(v), got, ok)
		}
	}
	eq(t, dotenvQuote("http://127.0.0.1:20759"), "http://127.0.0.1:20759")
}

// ---------------------------------------------------------------- toml

func TestCodexSlug(t *testing.T) {
	eq(t, codexSlug("Now Coding 全球加速"), "now_coding")
	eq(t, codexSlug("全球"), "provider")
}

func TestCodexParse(t *testing.T) {
	out := codexParse("model = \"m\"\n[model_providers.foo]\nname = \"Foo\"\nbase_url = \"https://x/v1/responses\"\n")
	eq(t, strings.Join(out.Keys(), ","), "id,name,base_url,model")
	eq(t, infoStr(out, "base_url"), "https://x/v1")
	out = codexParse("export OPENAI_BASE_URL=https://api.foo.com/v1\nexport OPENAI_API_KEY=sk-1")
	eq(t, infoStr(out, "name"), "foo.com")
	eq(t, infoStr(out, "key"), "sk-1")
}

func TestTomlScan(t *testing.T) {
	src := "# top\n\"model\" = \"a # not a comment\" # real\nnotify = [ \"x\",\n  \"[not.a.table]\", # c\n]\ns = \"\"\"\n[also.not]\n\"\"\"\n[a.\"b.c\"]\nk = { x = 1 }\n[[arr]]\nk = 1\n[arr.sub]\nk = 2\n"
	doc, err := parseTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range doc.stmts {
		if s.kind == stmtOther {
			continue
		}
		desc := strings.Join(s.path, "/")
		if s.kind == stmtKV {
			desc += "=" + src[s.valStart:s.valEnd]
		}
		if s.inArray {
			desc += " (array)"
		}
		got = append(got, desc)
	}
	eq(t, strings.Join(got, "\n"), strings.Join([]string{
		`model="a # not a comment"`,
		"notify=[ \"x\",\n  \"[not.a.table]\", # c\n]",
		"s=\"\"\"\n[also.not]\n\"\"\"",
		"a/b.c",
		"a/b.c/k={ x = 1 }",
		"arr (array)",
		"arr/k=1 (array)",
		"arr/sub (array)",
		"arr/sub/k=2 (array)",
	}, "\n"))
}

func TestCodexEditConfig(t *testing.T) {
	relay := codexProvider("name", "New \"q\" 全球", "id", "nowcoding", "base_url", "https://nowcoding.ai/v1", "key", `sk-abc\x`, "model", "gpt-5-codex")
	envKey := codexProvider("name", "Old2", "id", "old", "base_url", "https://old2/v1", "key", "k2", "env_key", "OLD_KEY")
	login := codexProvider("name", "OpenAI 官方", "id", "openai")
	table := "[model_providers.nowcoding]\nname = \"New \\\"q\\\" 全球\"\nbase_url = \"https://nowcoding.ai/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"sk-abc\\\\x\"\n"
	cases := []struct {
		name, src string
		p         *Provider
		want      string
	}{
		{"empty/relay", "", relay, "model_provider = \"nowcoding\"\nmodel = \"gpt-5-codex\"\n\n" + table},
		{"empty/login", "", login, "model_provider = \"openai\"\n"},
		{"comment only", "# just a comment\n", login, "# just a comment\nmodel_provider = \"openai\"\n"},
		{"keeps comments and other tables",
			"# my codex config\nmodel = \"gpt-5\" # pinned\n\n[features]\nshell_tool = true\n", relay,
			"# my codex config\nmodel_provider = \"nowcoding\"\nmodel = \"gpt-5-codex\" # pinned\n\n[features]\nshell_tool = true\n\n" + table},
		{"no root keys",
			"# header\n\n# about features\n[features]\nshell_tool = true\n", login,
			"# header\n\nmodel_provider = \"openai\"\n\n# about features\n[features]\nshell_tool = true\n"},
		{"updates a table in place",
			"model_provider = \"old\" # current\nmodel = \"gpt-5\"\n\n[model_providers.old]\nname = \"Old\"\nbase_url = \"https://old/v1\"\nwire_api = \"chat\"\nexperimental_bearer_token = \"sk-old\"\nheaders = { a = \"b\" }\n\n[features]\nx = 1\n", envKey,
			"model_provider = \"old\" # current\nmodel = \"gpt-5\"\n\n[model_providers.old]\nname = \"Old2\"\nbase_url = \"https://old2/v1\"\nwire_api = \"responses\"\nheaders = { a = \"b\" }\nenv_key = \"OLD_KEY\"\n\n[features]\nx = 1\n"},
		{"login leaves tables alone",
			"model_provider = \"old\" # current\n\n[model_providers.old]\nname = \"Old\"\n", login,
			"model_provider = \"openai\" # current\n\n[model_providers.old]\nname = \"Old\"\n"},
		{"quoted headers and CRLF",
			"\"model_provider\" = \"x\"\r\n\r\n[\"model_providers\".\"nowcoding\"]\r\n\"name\" = \"n\"\r\n", relay,
			"\"model_provider\" = \"nowcoding\"\r\nmodel = \"gpt-5-codex\"\r\n\r\n[\"model_providers\".\"nowcoding\"]\r\n\"name\" = \"New \\\"q\\\" 全球\"\r\nbase_url = \"https://nowcoding.ai/v1\"\r\nwire_api = \"responses\"\r\nexperimental_bearer_token = \"sk-abc\\\\x\"\r\n"},
		{"no trailing newline", "approval = \"never\"", login, "model_provider = \"openai\"\napproval = \"never\""},
	}
	for _, c := range cases {
		got, err := codexEditConfig(c.src, c.p)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if got != c.want {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, got, c.want)
		}
	}
	if _, err := codexEditConfig("model_providers = { x = { name = \"X\", base_url = \"u\" } }\n", relay); err == nil {
		t.Error("inline model_providers: expected an error")
	}
	if _, err := codexEditConfig("not toml [", relay); err == nil {
		t.Error("invalid file: expected an error")
	}
}

// ---------------------------------------------------------------- codex switching

const saiaiBlock = `# BEGIN SAIAI CODEX IDE (managed)
SSL_CERT_FILE="C:\\Users\\fish\\.claude\\saiai-ca.crt"
HTTP_PROXY="http://127.0.0.1:20759"
http_proxy="http://127.0.0.1:20759"
# END SAIAI CODEX IDE (managed)
`

func saiaiEnv() *Dict {
	return dictOf("SSL_CERT_FILE", `C:\Users\fish\.claude\saiai-ca.crt`,
		"HTTP_PROXY", "http://127.0.0.1:20759", "http_proxy", "http://127.0.0.1:20759")
}

// A setup like the real one: SAIAI installed itself over the built-in provider.
func saiaiSandbox(t *testing.T) (*Store, *Backend, string) {
	dir := sandbox(t)
	codex := filepath.Join(dir, "codex")
	write(t, filepath.Join(codex, "config.toml"), "model_provider = \"openai\"\nmodel = \"gpt-6\"\n\n[features]\napps = false\n")
	write(t, filepath.Join(codex, ".env"), "# keys\nTOKEN_X_API_KEY=abc\n\n"+saiaiBlock)
	write(t, filepath.Join(codex, "auth.json"), `{"auth_mode":"chatgptAuthTokens","tokens":{"account_id":"saiai-local-proxy-account","access_token":"p1"}}`)
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	b := store.Get(ToolCodex)
	saiai := codexProvider("name", "SAIAI")
	saiai.SetDict("extra_env", saiaiEnv())
	store.Upsert(ToolCodex, saiai, -1)
	store.Upsert(ToolCodex, codexProvider("name", "kiov", "base_url", "https://api.kiov.xin/v1", "key", "sk-k", "model", "gpt-6-astra"), -1)
	return store, b, codex
}

func use(t *testing.T, store *Store, name string) string {
	t.Helper()
	b := store.Get(ToolCodex)
	i := b.Find(name)
	if i < 0 {
		t.Fatalf("no provider %s", name)
	}
	note, err := store.Apply(ToolCodex, i)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := b.Active(); a != i {
		t.Errorf("active after switching to %s is %d, want %d", name, a, i)
	}
	return note
}

func TestCodexSaiaiRoundTrip(t *testing.T) {
	store, b, codex := saiaiSandbox(t)
	cfg, env, auth := filepath.Join(codex, "config.toml"), filepath.Join(codex, ".env"), filepath.Join(codex, "auth.json")
	cfg0, env0, auth0 := read(t, cfg), read(t, env), read(t, auth)

	// what is on disk is recognised as SAIAI, not as the plain official login
	if a, _ := b.Active(); a != b.Find("SAIAI") {
		t.Fatalf("active = %d, want SAIAI", a)
	}
	// re-applying the active provider changes nothing
	use(t, store, "SAIAI")
	eq(t, read(t, cfg), cfg0)
	eq(t, read(t, env), env0)
	eq(t, read(t, auth), auth0)

	// an endpoint: SAIAI's proxy variables leave, everything else stays
	use(t, store, "kiov")
	eq(t, read(t, env), "# keys\nTOKEN_X_API_KEY=abc\n\n# BEGIN SAIAI CODEX IDE (managed)\n# END SAIAI CODEX IDE (managed)\n")
	eq(t, read(t, cfg), "model_provider = \"kiov\"\nmodel = \"gpt-6-astra\"\n\n[features]\napps = false\n\n[model_providers.kiov]\nname = \"kiov\"\nbase_url = \"https://api.kiov.xin/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"sk-k\"\n")
	eq(t, read(t, auth), auth0)

	// and back
	use(t, store, "SAIAI")
	eq(t, read(t, env), "# keys\nTOKEN_X_API_KEY=abc\n\n# BEGIN SAIAI CODEX IDE (managed)\n# END SAIAI CODEX IDE (managed)\n\n"+
		"# BEGIN ccp (managed)\nSSL_CERT_FILE=\"C:\\\\Users\\\\fish\\\\.claude\\\\saiai-ca.crt\"\nHTTP_PROXY=http://127.0.0.1:20759\nhttp_proxy=http://127.0.0.1:20759\n# END ccp (managed)\n")
	if !strings.HasPrefix(read(t, cfg), "model_provider = \"openai\"\nmodel = \"gpt-6-astra\"\n") {
		t.Errorf("config: %s", read(t, cfg))
	}
	eq(t, read(t, auth), auth0)

	// away again: our block disappears completely
	use(t, store, "kiov")
	eq(t, read(t, env), "# keys\nTOKEN_X_API_KEY=abc\n\n# BEGIN SAIAI CODEX IDE (managed)\n# END SAIAI CODEX IDE (managed)\n")
}

func TestCodexLoginsKeepTheirOwnAuth(t *testing.T) {
	store, b, codex := saiaiSandbox(t)
	auth := filepath.Join(codex, "auth.json")
	placeholder := read(t, auth)
	official := b.Providers[b.Find("OpenAI")]
	snap := func(p *Provider) string { return read(t, snapshotPath(p)) }

	// the official login was never used through ccp: Codex must ask for a login
	// instead of sending SAIAI's placeholder to OpenAI
	note := use(t, store, "OpenAI")
	if !strings.Contains(note, "codex login") {
		t.Errorf("note = %q", note)
	}
	eq(t, read(t, auth), "<missing>")
	eq(t, snap(b.Providers[b.Find("SAIAI")]), placeholder)
	eq(t, read(t, filepath.Join(codex, ".env")), "# keys\nTOKEN_X_API_KEY=abc\n\n# BEGIN SAIAI CODEX IDE (managed)\n# END SAIAI CODEX IDE (managed)\n")

	// the user logs in
	real1 := `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-real","refresh_token":"r1"}}`
	write(t, auth, real1)

	note = use(t, store, "SAIAI")
	if !strings.Contains(note, "已恢复") {
		t.Errorf("note = %q", note)
	}
	eq(t, read(t, auth), placeholder)
	eq(t, snap(official), real1)

	// Codex refreshed the real tokens while an endpoint was active? Not possible,
	// but SAIAI rewriting its placeholder is: the newer file wins.
	placeholder2 := strings.Replace(placeholder, "p1", "p2", 1)
	write(t, auth, placeholder2)
	use(t, store, "kiov")
	eq(t, read(t, auth), placeholder2)

	eq(t, use(t, store, "OpenAI"), "已恢复「OpenAI 官方 (ChatGPT 登录)」的登录状态 (auth.json)")
	eq(t, read(t, auth), real1)
	eq(t, snap(b.Providers[b.Find("SAIAI")]), placeholder2)

	// tokens refreshed in place are saved before leaving
	real2 := strings.Replace(real1, "r1", "r2", 1)
	write(t, auth, real2)
	use(t, store, "SAIAI")
	eq(t, snap(official), real2)
	use(t, store, "OpenAI")
	eq(t, read(t, auth), real2)
}

func TestCodexEnvShadowing(t *testing.T) {
	dir := sandbox(t)
	env := filepath.Join(dir, "codex", ".env")
	write(t, env, "HTTP_PROXY=http://127.0.0.1:7890\nOLD_KEY=stale\n")
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	saiai := codexProvider("name", "SAIAI")
	saiai.SetDict("extra_env", dictOf("HTTP_PROXY", "http://127.0.0.1:20759", "CA", `C:\x y\ca.crt`))
	store.Upsert(ToolCodex, saiai, -1)
	store.Upsert(ToolCodex, codexProvider("name", "Old2", "id", "old", "base_url", "https://old2/v1", "key", "k2", "env_key", "OLD_KEY"), -1)

	use(t, store, "SAIAI")
	eq(t, read(t, env), "OLD_KEY=stale\n\n# BEGIN ccp (managed)\nHTTP_PROXY=http://127.0.0.1:20759\nCA=\"C:\\\\x y\\\\ca.crt\"\n# END ccp (managed)\n")
	// the user's own proxy comes back; the env_key provider's key is written in place
	use(t, store, "Old2")
	eq(t, read(t, env), "OLD_KEY=k2\nHTTP_PROXY=http://127.0.0.1:7890\n")
	// state survives a reload
	store, err = LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	use(t, store, "SAIAI")
	use(t, store, "OpenAI")
	eq(t, read(t, env), "OLD_KEY=k2\nHTTP_PROXY=http://127.0.0.1:7890\n")
}

func TestCodexBootstrap(t *testing.T) {
	dir := sandbox(t)
	write(t, filepath.Join(dir, "codex", "config.toml"), "model_provider = \"b\"\nmodel = \"m\"\n[model_providers.a]\nname = \"A\"\nbase_url = \"https://a/v1\"\nenv_key = \"A_KEY\"\n[model_providers.b]\nbase_url = \"https://b/v1\"\nexperimental_bearer_token = \"kb\"\nwire_api = \"chat\"\n")
	write(t, filepath.Join(dir, "codex", ".env"), "A_KEY=ka\n")
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	b := store.Get(ToolCodex)
	var got []string
	for _, p := range b.Providers {
		got = append(got, strings.Join([]string{p.Name(), p.S("id"), p.BaseURL(), p.Key(), p.S("wire_api"), p.Model(), p.S("env_key")}, "|"))
	}
	eq(t, strings.Join(got, "\n"), "b|b|https://b/v1|kb|chat|m|\nA|a|https://a/v1|ka|responses||A_KEY\nOpenAI 官方 (ChatGPT 登录)|openai|||responses||")
	if a, _ := b.Active(); a != 0 {
		t.Errorf("active = %d", a)
	}
	// loading again reads the saved state instead of importing twice
	store, _ = LoadStore()
	eq(t, len(store.Get(ToolCodex).Providers), 3)
}

// ---------------------------------------------------------------- tui

func TestHeadlessAddAndSwitch(t *testing.T) {
	store, _, codex := saiaiSandbox(t)
	screen := headless(store, ToolCodex, true, 100, 12, nil).String()
	for _, want := range []string{"Claude Provider", "OpenAI 官方 (ChatGPT 登…", "●  2  SAIAI", "(官方登录 + 环境变量)", "https://api.kiov.xin/v1", " a 新增"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks %q:\n%s", want, screen)
		}
	}
	// add a provider through the form, then switch to it with its number
	keys := []string{"a", "paste:export OPENAI_BASE_URL=https://api.foo.com/v1/responses\nexport OPENAI_API_KEY=sk-foo-123456", "ctrl+s", "4"}
	screen = headless(store, ToolCodex, true, 100, 14, keys).String()
	if !strings.Contains(screen, "●  4  foo.com") || !strings.Contains(screen, "切换成功") {
		t.Errorf("after add + switch:\n%s", screen)
	}
	if !strings.Contains(read(t, filepath.Join(codex, "config.toml")), "[model_providers.foo_com]\nname = \"foo.com\"\nbase_url = \"https://api.foo.com/v1\"") {
		t.Errorf("config:\n%s", read(t, filepath.Join(codex, "config.toml")))
	}
	// edit: tab to the env box, type a variable, save
	keys = []string{"up", "up", "e", "tab", "tab", "tab", "tab", "tab", "tab", "tab", "text:X=1", "enter", "ctrl+s"}
	headless(store, ToolCodex, true, 100, 30, keys)
	p := store.Get(ToolCodex).Providers[store.Get(ToolCodex).Find("SAIAI")]
	if v, _ := p.Dict("extra_env").Get("X"); v != "1" || p.Dict("extra_env").Len() != 4 {
		t.Errorf("extra_env after edit: %q", dictText(p.Dict("extra_env")))
	}
}
