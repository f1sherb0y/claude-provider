// Reading and writing dotenv lines the way Codex (the dotenvy crate) understands them.
package main

import (
	"regexp"
	"strings"
)

var (
	dotenvLineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(.*)$`)
	dotenvBareRe = regexp.MustCompile(`^[A-Za-z0-9_\-./:,@+=%~]+$`)
)

// parseDotenvLine returns the assignment a line makes; ok is false for comments,
// blank lines and anything dotenvy would reject.
func parseDotenvLine(line string) (key, val string, ok bool) {
	m := dotenvLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	var out strings.Builder
	var strong, weak, escaped, ending bool
	for _, c := range strings.TrimRight(m[2], " \t\r") {
		switch {
		case ending:
			// `k=v #comment` is fine, `k=v w` is not
			if c == ' ' || c == '\t' {
				continue
			}
			if c != '#' {
				return "", "", false
			}
			return m[1], out.String(), true
		case escaped:
			switch c {
			case '\\', '\'', '"', '$', ' ':
				out.WriteRune(c)
			case 'n':
				out.WriteByte('\n')
			default:
				return "", "", false
			}
			escaped = false
		case strong:
			if c == '\'' {
				strong = false
			} else {
				out.WriteRune(c)
			}
		case weak:
			switch c {
			case '"':
				weak = false
			case '\\':
				escaped = true
			default:
				out.WriteRune(c)
			}
		case c == '\'':
			strong = true
		case c == '"':
			weak = true
		case c == '\\':
			escaped = true
		case c == ' ' || c == '\t':
			ending = true
		default:
			out.WriteRune(c)
		}
	}
	if strong || weak || escaped {
		return "", "", false
	}
	return m[1], out.String(), true
}

// dotenvQuote writes a value so that dotenvy reads back exactly v.
func dotenvQuote(v string) string {
	if dotenvBareRe.MatchString(v) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// parseDotenv returns the variables a dotenv file sets (the last assignment wins).
func parseDotenv(text string) *Dict {
	env := NewOMap[string]()
	for _, line := range splitLines(text) {
		if k, v, ok := parseDotenvLine(line); ok {
			env.Set(k, v)
		}
	}
	return env
}
