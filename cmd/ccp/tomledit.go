// Surgical edits to a TOML file: values are replaced in place and new keys are
// inserted as whole lines, so comments, ordering and formatting elsewhere survive.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type stmtKind int

const (
	stmtOther stmtKind = iota // blank line or comment
	stmtTable
	stmtArrayTable
	stmtKV
)

type tomlStmt struct {
	kind     stmtKind
	start    int      // start of the statement's first line
	end      int      // just past the newline that ends it
	path     []string // full path of the key, or of the table a header opens
	valStart int
	valEnd   int
	inArray  bool // lives in an [[array]] element, so a path does not address it
}

type tomlDoc struct {
	src   string
	stmts []tomlStmt
}

func tomlLoad(src string) (map[string]any, error) {
	m := map[string]any{}
	if err := toml.Unmarshal([]byte(src), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func parseTOML(src string) (*tomlDoc, error) {
	if _, err := tomlLoad(src); err != nil {
		return nil, err
	}
	d := &tomlDoc{src: src}
	return d, d.rescan()
}

func (d *tomlDoc) rescan() error {
	stmts, err := scanTOML(d.src)
	d.stmts = stmts
	return err
}

func tomlGet(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, k := range path {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = t[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// tomlStr is the string at path ("" if absent; other scalars are formatted).
func tomlStr(m map[string]any, path ...string) string {
	v, ok := tomlGet(m, path...)
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case map[string]any, []any:
		return ""
	}
	return fmt.Sprint(v)
}

// ---------------------------------------------------------------- scanner

func skipWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// stringEnd returns the index just past the string literal starting at s[i].
func stringEnd(s string, i int) (int, error) {
	q := s[i]
	if strings.HasPrefix(s[i:], strings.Repeat(string(q), 3)) {
		for k := i + 3; k < len(s); k++ {
			if q == '"' && s[k] == '\\' {
				k++
				continue
			}
			if strings.HasPrefix(s[k:], strings.Repeat(string(q), 3)) {
				k += 3
				// up to two quotes may sit right before the closing delimiter
				for n := 0; n < 2 && k < len(s) && s[k] == q; n++ {
					k++
				}
				return k, nil
			}
		}
		return 0, errors.New("unterminated multi-line string")
	}
	for k := i + 1; k < len(s); k++ {
		switch {
		case q == '"' && s[k] == '\\':
			k++
		case s[k] == q:
			return k + 1, nil
		case s[k] == '\n':
			return 0, errors.New("unterminated string")
		}
	}
	return 0, errors.New("unterminated string")
}

func unescapeBasic(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}
		i++
		hex := func(n int) {
			if i+n < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32); err == nil {
					out.WriteRune(rune(v))
				}
				i += n
			}
		}
		switch s[i] {
		case 'b':
			out.WriteByte('\b')
		case 't':
			out.WriteByte('\t')
		case 'n':
			out.WriteByte('\n')
		case 'f':
			out.WriteByte('\f')
		case 'r':
			out.WriteByte('\r')
		case 'e':
			out.WriteByte(0x1b)
		case 'x':
			hex(2)
		case 'u':
			hex(4)
		case 'U':
			hex(8)
		default:
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

// scanKeyPath reads a (possibly dotted, possibly quoted) key starting at s[i].
func scanKeyPath(s string, i int) ([]string, int, error) {
	var path []string
	for {
		i = skipWS(s, i)
		if i >= len(s) {
			return nil, 0, errors.New("unexpected end of file in key")
		}
		if s[i] == '"' || s[i] == '\'' {
			end, err := stringEnd(s, i)
			if err != nil {
				return nil, 0, err
			}
			part := s[i+1 : end-1]
			if s[i] == '"' {
				part = unescapeBasic(part)
			}
			path = append(path, part)
			i = end
		} else {
			j := i
			for j < len(s) && !strings.ContainsRune(" \t.=[]#\"'\r\n", rune(s[j])) {
				j++
			}
			if j == i {
				return nil, 0, fmt.Errorf("unexpected %q in key", s[i])
			}
			path = append(path, s[i:j])
			i = j
		}
		i = skipWS(s, i)
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		return path, i, nil
	}
}

// scanValue returns where the value starting at s[i] ends and where its statement ends.
func scanValue(s string, i int) (valEnd, end int, err error) {
	depth, last := 0, i
	for i < len(s) {
		switch c := s[i]; c {
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '\n':
			i++
			if depth == 0 {
				return last, i, nil
			}
		case '"', '\'':
			if i, err = stringEnd(s, i); err != nil {
				return 0, 0, err
			}
			last = i
		case '[', '{':
			depth++
			i++
			last = i
		case ']', '}':
			depth--
			i++
			last = i
		case ' ', '\t', '\r':
			i++
		default:
			i++
			last = i
		}
	}
	if depth != 0 {
		return 0, 0, errors.New("unterminated array or inline table")
	}
	return last, len(s), nil
}

func hasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

func scanTOML(src string) ([]tomlStmt, error) {
	var out []tomlStmt
	var table []string
	var arrays [][]string
	inArray := false
	for i := 0; i < len(src); {
		start := i
		j := skipWS(src, i)
		if j >= len(src) || src[j] == '\n' || src[j] == '\r' || src[j] == '#' {
			i = lineEnd(src, j)
			out = append(out, tomlStmt{kind: stmtOther, start: start, end: i})
			continue
		}
		if src[j] == '[' {
			kind, k := stmtTable, j+1
			if strings.HasPrefix(src[j:], "[[") {
				kind, k = stmtArrayTable, j+2
			}
			path, k, err := scanKeyPath(src, k)
			if err != nil {
				return nil, err
			}
			if k >= len(src) || src[k] != ']' {
				return nil, errors.New("malformed table header")
			}
			table, inArray = path, kind == stmtArrayTable
			if inArray {
				arrays = append(arrays, path)
			} else {
				for _, a := range arrays {
					inArray = inArray || hasPrefix(path, a)
				}
			}
			i = lineEnd(src, k)
			out = append(out, tomlStmt{kind: kind, start: start, end: i, path: path, inArray: inArray})
			continue
		}
		key, k, err := scanKeyPath(src, j)
		if err != nil {
			return nil, err
		}
		if k >= len(src) || src[k] != '=' {
			return nil, errors.New("expected '=' after key")
		}
		valStart := skipWS(src, k+1)
		valEnd, end, err := scanValue(src, valStart)
		if err != nil {
			return nil, err
		}
		out = append(out, tomlStmt{kind: stmtKV, start: start, end: end, valStart: valStart, valEnd: valEnd,
			path: append(slices.Clone(table), key...), inArray: inArray})
		i = end
	}
	return out, nil
}

// ---------------------------------------------------------------- editing

// tomlQuote writes a basic string.
func tomlQuote(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&out, `\u%04x`, c)
			} else {
				out.WriteRune(c)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(k string) string {
	if bareKeyRe.MatchString(k) {
		return k
	}
	return tomlQuote(k)
}

func (d *tomlDoc) newline() string {
	if strings.Contains(d.src, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func (d *tomlDoc) find(path []string) *tomlStmt {
	for i := range d.stmts {
		if s := &d.stmts[i]; s.kind == stmtKV && !s.inArray && slices.Equal(s.path, path) {
			return s
		}
	}
	return nil
}

func (d *tomlDoc) header(path []string) int {
	for i, s := range d.stmts {
		if s.kind == stmtTable && !s.inArray && slices.Equal(s.path, path) {
			return i
		}
	}
	return -1
}

// insert puts text at byte offset at, starting it on a fresh line.
func (d *tomlDoc) insert(at int, text string) error {
	if at > 0 && d.src[at-1] != '\n' {
		text = d.newline() + text
	}
	d.src = d.src[:at] + text + d.src[at:]
	return d.rescan()
}

// SetString sets the string at path, creating the key (and its table) if needed.
func (d *tomlDoc) SetString(path []string, value string) error {
	if kv := d.find(path); kv != nil {
		d.src = d.src[:kv.valStart] + tomlQuote(value) + d.src[kv.valEnd:]
		return d.rescan()
	}
	nl := d.newline()
	parent, key := path[:len(path)-1], path[len(path)-1]
	line := tomlKey(key) + " = " + tomlQuote(value) + nl
	if len(parent) == 0 {
		return d.insertRoot(key, line)
	}
	if h := d.header(parent); h >= 0 {
		at := d.stmts[h].end
		for _, s := range d.stmts[h+1:] {
			if s.kind == stmtTable || s.kind == stmtArrayTable {
				break
			}
			if s.kind == stmtKV {
				at = s.end
			}
		}
		return d.insert(at, line)
	}
	// the table can only be added as a [header] if nothing defines it another way
	for _, s := range d.stmts {
		if s.kind == stmtKV && (hasPrefix(s.path, parent) || hasPrefix(parent, s.path)) ||
			s.kind == stmtArrayTable && hasPrefix(parent, s.path) {
			return fmt.Errorf("%s 是内联表、点分键或数组表，无法自动添加 [%s]",
				strings.Join(s.path, "."), strings.Join(parent, "."))
		}
	}
	keys := make([]string, len(parent))
	for i, k := range parent {
		keys[i] = tomlKey(k)
	}
	text := "[" + strings.Join(keys, ".") + "]" + nl + line
	if strings.TrimSpace(d.src) != "" {
		text = nl + text
	}
	return d.insert(len(d.src), text)
}

// insertRoot adds a top-level key: right after model_provider when there is one,
// otherwise ahead of the other top-level keys (or of the first table).
func (d *tomlDoc) insertRoot(key, line string) error {
	if kv := d.find([]string{"model_provider"}); kv != nil && key != "model_provider" {
		return d.insert(kv.end, line)
	}
	first := -1
	for i, s := range d.stmts {
		if s.kind != stmtOther {
			first = i
			break
		}
	}
	if first < 0 {
		return d.insert(len(d.src), line)
	}
	if d.stmts[first].kind == stmtKV {
		return d.insert(d.stmts[first].start, line)
	}
	// keep the comment block attached to the first table with that table
	for first > 0 && strings.TrimSpace(d.src[d.stmts[first-1].start:d.stmts[first-1].end]) != "" {
		first--
	}
	return d.insert(d.stmts[first].start, line+d.newline())
}

// Remove deletes the key at path (comment lines above it stay).
func (d *tomlDoc) Remove(path []string) error {
	kv := d.find(path)
	if kv == nil {
		return nil
	}
	d.src = d.src[:kv.start] + d.src[kv.end:]
	return d.rescan()
}

// childTables lists the names directly under table in document order.
func (d *tomlDoc) childTables(table string, parsed map[string]any) []string {
	var out []string
	add := func(k string) {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	for _, s := range d.stmts {
		if s.kind != stmtOther && len(s.path) >= 2 && s.path[0] == table {
			add(s.path[1])
		}
	}
	// inline tables are invisible to the scanner
	if t, ok := parsed[table].(map[string]any); ok {
		var rest []string
		for k := range t {
			if !slices.Contains(out, k) {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		out = append(out, rest...)
		out = slices.DeleteFunc(out, func(k string) bool { _, ok := t[k].(map[string]any); return !ok })
	}
	return out
}
