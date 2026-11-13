// Shared helpers: ordered maps, paths, atomic writes, backups, clipboard, pasted-text parsing.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
)

const maxBackups = 20

// ---------------------------------------------------------------- ordered map

// OMap is a string-keyed map that remembers insertion order.
type OMap[V any] struct {
	keys []string
	m    map[string]V
}

func NewOMap[V any]() *OMap[V] { return &OMap[V]{m: map[string]V{}} }

func (o *OMap[V]) Get(k string) (V, bool) {
	if o == nil {
		var zero V
		return zero, false
	}
	v, ok := o.m[k]
	return v, ok
}

func (o *OMap[V]) Has(k string) bool {
	_, ok := o.Get(k)
	return ok
}

// Set replaces an existing value in place or appends a new key.
func (o *OMap[V]) Set(k string, v V) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *OMap[V]) Del(k string) {
	if o == nil {
		return
	}
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *OMap[V]) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys returns the keys in insertion order (do not modify).
func (o *OMap[V]) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

func (o *OMap[V]) Clone() *OMap[V] {
	c := NewOMap[V]()
	for _, k := range o.Keys() {
		c.Set(k, o.m[k])
	}
	return c
}

// Dict is an ordered string -> string map (env vars and the like).
type Dict = OMap[string]

func dictEqual(a, b *Dict) bool {
	if a.Len() != b.Len() {
		return false
	}
	for _, k := range a.Keys() {
		av, _ := a.Get(k)
		bv, ok := b.Get(k)
		if !ok || av != bv {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- ordered JSON

// JObj is a JSON object that keeps its key order. JSON values are
// nil, bool, json.Number, string, []any or *JObj.
type JObj = OMap[any]

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing characters after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		obj := NewOMap[any]()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, ok := kt.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.Set(k, v)
		}
		_, err := dec.Token()
		return obj, err
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	}
	return nil, fmt.Errorf("unexpected %q", d)
}

func jsonString(sb *strings.Builder, s string) {
	const hexdigits = "0123456789abcdef"
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				sb.WriteString(`\u00`)
				sb.WriteByte(hexdigits[r>>4])
				sb.WriteByte(hexdigits[r&0xf])
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}

func encodeValue(sb *strings.Builder, v any, indent int, pretty bool) {
	nl := func(n int) {
		if pretty {
			sb.WriteByte('\n')
			sb.WriteString(strings.Repeat("  ", n))
		}
	}
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case json.Number:
		sb.WriteString(x.String())
	case string:
		jsonString(sb, x)
	case []string:
		arr := make([]any, len(x))
		for i, s := range x {
			arr[i] = s
		}
		encodeValue(sb, arr, indent, pretty)
	case []any:
		if len(x) == 0 {
			sb.WriteString("[]")
			return
		}
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			nl(indent + 1)
			encodeValue(sb, e, indent+1, pretty)
		}
		nl(indent)
		sb.WriteByte(']')
	case *JObj:
		if x.Len() == 0 {
			sb.WriteString("{}")
			return
		}
		sb.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				sb.WriteByte(',')
			}
			nl(indent + 1)
			jsonString(sb, k)
			sb.WriteByte(':')
			if pretty {
				sb.WriteByte(' ')
			}
			e, _ := x.Get(k)
			encodeValue(sb, e, indent+1, pretty)
		}
		nl(indent)
		sb.WriteByte('}')
	case *Dict:
		obj := NewOMap[any]()
		for _, k := range x.Keys() {
			s, _ := x.Get(k)
			obj.Set(k, s)
		}
		encodeValue(sb, obj, indent, pretty)
	default:
		sb.WriteString("null")
	}
}

// toJSON renders like `json.dumps(data, indent=2, ensure_ascii=False) + "\n"`.
func toJSON(v any) string {
	var sb strings.Builder
	encodeValue(&sb, v, 0, true)
	sb.WriteByte('\n')
	return sb.String()
}

func compactJSON(v any) string {
	var sb strings.Builder
	encodeValue(&sb, v, 0, false)
	return sb.String()
}

func writeJSON(path string, v any) error {
	return atomicWrite(path, []byte(toJSON(v)))
}

func loadJSONObj(text string) *JObj {
	v, err := decodeJSON([]byte(text))
	if err != nil {
		return nil
	}
	obj, _ := v.(*JObj)
	return obj
}

// jsonText is the text a JSON scalar stands for (strings as they are).
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return x.String()
	}
	return compactJSON(v)
}

// ---------------------------------------------------------------- paths

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return string(filepath.Separator)
}

// envDir is `os.environ.get(name) or ~/def`.
func envDir(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return filepath.Join(homeDir(), def)
}

func configDir() string { return envDir("CLAUDE_PROVIDER_HOME", ".claude-provider") }
func dataFile() string  { return filepath.Join(configDir(), "providers.json") }
func backupDir() string { return filepath.Join(configDir(), "backups") }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------- small helpers

func mask(secret string) string {
	chars := []rune(secret)
	if len(chars) == 0 {
		return "-"
	}
	if len(chars) <= 12 {
		return string(chars[:min(3, len(chars))]) + "…"
	}
	return string(chars[:7]) + "…" + string(chars[len(chars)-4:])
}

// hostname works like urllib's `urlparse(url).hostname`.
func hostname(url string) string {
	colon := strings.Index(url, ":")
	if colon < 0 {
		return ""
	}
	scheme := url[:colon]
	if scheme == "" || !unicode.IsLetter(rune(scheme[0])) || scheme[0] > 127 {
		return ""
	}
	for _, c := range scheme {
		if !(c < 128 && (unicode.IsLetter(c) || unicode.IsDigit(c)) || strings.ContainsRune("+-.", c)) {
			return ""
		}
	}
	rest, ok := strings.CutPrefix(url[colon+1:], "//")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	var host string
	if b, ok := strings.CutPrefix(rest, "["); ok {
		host, _, _ = strings.Cut(b, "]")
	} else {
		host, _, _ = strings.Cut(rest, ":")
	}
	return strings.ToLower(host)
}

func nameFromURL(url string) string {
	full := url
	if !strings.Contains(url, "://") {
		full = "https://" + url
	}
	host := hostname(full)
	if host == "" {
		host = url
	}
	host = strings.TrimPrefix(host, "www.")
	return strings.TrimPrefix(host, "api.")
}

// splitLines is Python's str.splitlines().
func splitLines(text string) []string {
	var out []string
	start := 0
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\r', '\x0b', '\x0c', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			out = append(out, string(rs[start:i]))
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// strip is Python's str.strip().
func strip(s string) string { return strings.TrimFunc(s, isSpace) }

// ---------------------------------------------------------------- files

func tmpSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789_"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		n := uint64(time.Now().UnixNano()) ^ uint64(os.Getpid())<<32
		for i := range b {
			b[i] = byte(n >> (8 * i))
		}
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// atomicWrite writes via a private (0600) temp file in the same directory, then renames over path.
func atomicWrite(path string, data []byte) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("%s: %w", parent, err)
	}
	var tmp string
	var f *os.File
	for {
		tmp = filepath.Join(parent, filepath.Base(path)+"."+tmpSuffix()+".cp-tmp")
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s: %w", tmp, err)
		}
	}
	_, err := f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameRetry(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// On Windows a rename over a file that another process (editor, antivirus, the tool
// itself) briefly has open fails with "access denied"; retry for a moment.
func renameRetry(from, to string) error {
	for tries := 0; ; tries++ {
		err := os.Rename(from, to)
		if err == nil || runtime.GOOS != "windows" || !errors.Is(err, os.ErrPermission) || tries >= 20 {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stemSuffix splits a file name with pathlib semantics.
func stemSuffix(name string) (string, string) {
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		return name[:i], name[i:]
	}
	return name, ""
}

// backup copies path into the backup dir as `<stem>.<timestamp><suffix>`, keeping the newest maxBackups.
func backup(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dir := backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	stem, suffix := stemSuffix(filepath.Base(path))
	now := time.Now()
	stamp := fmt.Sprintf("%s.%06d", now.Format("20060102_150405"), now.Nanosecond()/1000)
	if err := atomicWrite(filepath.Join(dir, stem+"."+stamp+suffix), data); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := stem + "."
	var old []string
	for _, e := range entries {
		n := e.Name()
		if len(n) >= len(prefix)+len(suffix) && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			old = append(old, n)
		}
	}
	sort.Strings(old)
	for i := 0; i < len(old)-maxBackups; i++ {
		os.Remove(filepath.Join(dir, old[i]))
	}
	return nil
}

// ---------------------------------------------------------------- clipboard

func runWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return out, err == nil
}

func readClipboard() string {
	var commands [][]string
	switch runtime.GOOS {
	case "windows":
		// default console encoding would mangle non-ASCII text
		commands = [][]string{{"powershell", "-NoProfile", "-Command",
			"[Console]::OutputEncoding = [Text.Encoding]::UTF8; Get-Clipboard -Raw"}}
	case "darwin":
		commands = [][]string{{"pbpaste"}}
	default:
		wayland := [][]string{{"wl-paste", "--no-newline"}}
		x11 := [][]string{{"xclip", "-o", "-selection", "clipboard"}, {"xsel", "--clipboard", "--output"}}
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			commands = append(wayland, x11...)
		} else {
			commands = append(x11, wayland...)
		}
	}
	for _, cmd := range commands {
		if _, err := exec.LookPath(cmd[0]); err != nil {
			continue
		}
		if out, ok := runWithTimeout(5*time.Second, cmd[0], cmd[1:]...); ok {
			text := strings.TrimPrefix(string(out), "\ufeff")
			text = strings.ReplaceAll(text, "\r\n", "\n")
			return strings.ReplaceAll(text, "\r", "\n")
		}
	}
	return ""
}

// ---------------------------------------------------------------- pasted-text parsing

var envLineRe = regexp.MustCompile(`^\s*(?:export\s+|set\s+|\$env:)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*;?\s*$`)

// parseEnvScript reads bash `export K=V`, PowerShell `$env:K="V"`, cmd `set K=V`, or plain K=V lines.
func parseEnvScript(text string) *Dict {
	env := NewOMap[string]()
	for _, line := range splitLines(text) {
		m := envLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := m[2]
		if n := len(v); n >= 2 && v[0] == v[n-1] && (v[0] == '"' || v[0] == '\'') {
			v = v[1 : n-1]
		}
		env.Set(m[1], v)
	}
	return env
}

// Info is parse() output: field name -> value (`extra_env` carries a dict).
type Info = OMap[Val]

func infoStr(info *Info, key string) string {
	v, _ := info.Get(key)
	return v.S
}

func setInfo(info *Info, key, value string) { info.Set(key, Val{S: value}) }

var (
	genericURLRe = regexp.MustCompile(`https?://[^\s"',]+`)
	genericKeyRe = regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{8,}|[A-Za-z0-9_\-]{32,})\b`)
)

// parseGeneric reads NewAPI-style JSON ({"url":..,"key":..}) or free text containing a URL and a key.
func parseGeneric(text string) *Info {
	out := NewOMap[Val]()
	if data := loadJSONObj(text); data != nil {
		lower := NewOMap[string]()
		for _, k := range data.Keys() {
			if s, ok := data.m[k].(string); ok {
				lower.Set(strings.ToLower(k), s)
			}
		}
		pick := func(keys ...string) string {
			for _, k := range keys {
				if v, _ := lower.Get(k); v != "" {
					return v
				}
			}
			return ""
		}
		if v := pick("url", "base_url", "baseurl", "endpoint", "host"); v != "" {
			setInfo(out, "base_url", v)
		}
		if v := pick("key", "api_key", "apikey", "token", "auth_token"); v != "" {
			setInfo(out, "key", v)
		}
		for _, k := range []string{"name", "model"} {
			if v, _ := lower.Get(k); v != "" {
				setInfo(out, k, v)
			}
		}
		return out
	}
	if m := genericURLRe.FindString(text); m != "" {
		setInfo(out, "base_url", m)
	}
	rest := text
	if u := infoStr(out, "base_url"); u != "" {
		rest = strings.ReplaceAll(text, u, "")
	}
	if m := genericKeyRe.FindStringSubmatch(rest); m != nil {
		setInfo(out, "key", m[1])
	}
	return out
}
