// Provider model and the backend-independent parts of a backend.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

type Kind int

const (
	KindText Kind = iota
	KindPassword
	KindSelect
	KindDict
)

// Field is one editable provider attribute, rendered by the TUI form.
type Field struct {
	Name        string
	Label       string
	Kind        Kind
	Options     []string
	Placeholder string
	Default     string
}

// Val is a field value: a string, or a dict when D is set.
type Val struct {
	S string
	D *Dict
}

func (v Val) Empty() bool { return v.D == nil && v.S == "" }

// Provider holds every field of its backend, in declaration order.
type Provider struct {
	vals *OMap[Val]
}

func NewProvider(fields []Field) *Provider {
	p := &Provider{vals: NewOMap[Val]()}
	for _, f := range fields {
		if f.Kind == KindDict {
			p.vals.Set(f.Name, Val{D: NewOMap[string]()})
		} else {
			p.vals.Set(f.Name, Val{S: f.Default})
		}
	}
	return p
}

func (p *Provider) Clone() *Provider {
	c := &Provider{vals: NewOMap[Val]()}
	for _, k := range p.vals.Keys() {
		v, _ := p.vals.Get(k)
		if v.D != nil {
			v.D = v.D.Clone()
		}
		c.vals.Set(k, v)
	}
	return c
}

func (p *Provider) Has(key string) bool { return p.vals.Has(key) }

func (p *Provider) S(key string) string {
	v, _ := p.vals.Get(key)
	return v.S
}

// Dict returns the dict stored under key (never nil; do not modify).
func (p *Provider) Dict(key string) *Dict {
	if v, _ := p.vals.Get(key); v.D != nil {
		return v.D
	}
	return NewOMap[string]()
}

func (p *Provider) Set(key, v string)           { p.vals.Set(key, Val{S: v}) }
func (p *Provider) SetDict(key string, d *Dict) { p.vals.Set(key, Val{D: d}) }
func (p *Provider) Name() string                { return p.S("name") }
func (p *Provider) BaseURL() string             { return p.S("base_url") }
func (p *Provider) Key() string                 { return p.S("key") }
func (p *Provider) Model() string               { return p.S("model") }

func providerFromJSON(fields []Field, obj *JObj) *Provider {
	p := NewProvider(fields)
	for _, f := range fields {
		v, ok := obj.Get(f.Name)
		if !ok {
			continue
		}
		if f.Kind == KindDict {
			d := NewOMap[string]()
			if m, ok := v.(*JObj); ok {
				for _, k := range m.Keys() {
					d.Set(k, jsonText(m.m[k]))
				}
			}
			p.SetDict(f.Name, d)
		} else if v == nil {
			p.Set(f.Name, "")
		} else {
			p.Set(f.Name, jsonText(v))
		}
	}
	return p
}

func (p *Provider) toJSON() *JObj {
	obj := NewOMap[any]()
	for _, k := range p.vals.Keys() {
		v, _ := p.vals.Get(k)
		if v.D != nil {
			obj.Set(k, v.D)
		} else {
			obj.Set(k, v.S)
		}
	}
	return obj
}

type Tool int

const (
	ToolClaude Tool = iota
	ToolCodex
)

var allTools = []Tool{ToolClaude, ToolCodex}

func (t Tool) ID() string {
	if t == ToolCodex {
		return "codex"
	}
	return "claude"
}

func toolFromID(id string) (Tool, bool) {
	for _, t := range allTools {
		if t.ID() == id {
			return t, true
		}
	}
	return ToolClaude, false
}

// Backend is a tool whose provider config we manage (Claude Code, Codex).
type Backend struct {
	Tool      Tool
	Providers []*Provider
	// env keys the active provider wrote, and the user values it overrode
	AppliedKeys []string
	Shadowed    *JObj
}

func NewBackend(tool Tool, state any) (*Backend, error) {
	b := &Backend{Tool: tool, Shadowed: NewOMap[any]()}
	switch s := state.(type) {
	case *JObj:
		b.loadState(s)
	case nil:
		if err := b.bootstrap(); err != nil {
			return nil, err
		}
	default:
		b.loadState(NewOMap[any]())
	}
	return b, nil
}

func (b *Backend) Label() string {
	if b.Tool == ToolCodex {
		return "Codex"
	}
	return "Claude Code"
}

func (b *Backend) Fields() []Field {
	if b.Tool == ToolCodex {
		return codexFields
	}
	return claudeFields
}

func (b *Backend) PasteHint() string {
	if b.Tool == ToolCodex {
		return codexPasteHint
	}
	return claudePasteHint
}

func (b *Backend) Target() string {
	if b.Tool == ToolCodex {
		return codexConfigFile()
	}
	return claudeSettingsFile()
}

func (b *Backend) Blank() *Provider { return NewProvider(b.Fields()) }

// -- persistence

func (b *Backend) loadState(state *JObj) {
	b.Providers = nil
	if arr, ok := state.m["providers"].([]any); ok {
		for _, e := range arr {
			if obj, ok := e.(*JObj); ok {
				b.Providers = append(b.Providers, providerFromJSON(b.Fields(), obj))
			}
		}
	}
	if arr, ok := state.m["applied_keys"].([]any); ok {
		for _, e := range arr {
			b.AppliedKeys = append(b.AppliedKeys, jsonText(e))
		}
	}
	if m, ok := state.m["shadowed"].(*JObj); ok {
		b.Shadowed = m.Clone()
	}
}

func (b *Backend) dumpState() *JObj {
	m := NewOMap[any]()
	providers := make([]any, len(b.Providers))
	for i, p := range b.Providers {
		providers[i] = p.toJSON()
	}
	m.Set("providers", providers)
	// Codex only carries this bookkeeping once a provider with extra env was used
	if b.Tool == ToolClaude || len(b.AppliedKeys) > 0 || b.Shadowed.Len() > 0 {
		keys := make([]any, len(b.AppliedKeys))
		for i, k := range b.AppliedKeys {
			keys[i] = k
		}
		m.Set("applied_keys", keys)
		m.Set("shadowed", b.Shadowed)
	}
	return m
}

func (b *Backend) bootstrap() error {
	if b.Tool == ToolCodex {
		return codexBootstrap(b)
	}
	return claudeBootstrap(b)
}

// -- tool specific

// Active returns the index of the provider the tool is using now, or -1.
func (b *Backend) Active() (int, error) {
	if b.Tool == ToolCodex {
		return codexActive(b)
	}
	return claudeActive(b)
}

// Apply writes the provider into the tool's config (caller saves the store).
// The returned note, if any, tells the user about a side effect worth knowing.
func (b *Backend) Apply(idx int) (string, error) {
	if b.Tool == ToolCodex {
		return codexApply(b, idx)
	}
	return "", claudeApply(b, b.Providers[idx])
}

func (b *Backend) Parse(text string) *Info {
	if b.Tool == ToolCodex {
		return codexParse(text)
	}
	return claudeParse(text)
}

func (b *Backend) uniqueID(base string, exclude *Provider) string {
	taken := func(id string) bool {
		for _, q := range b.Providers {
			if q != exclude && q.S("id") == id {
				return true
			}
		}
		return false
	}
	id := base
	for i := 2; taken(id); i++ {
		id = base + "_" + strconv.Itoa(i)
	}
	return id
}

// Normalize cleans up a provider coming out of the editor before storing it.
func (b *Backend) Normalize(p *Provider) *Provider {
	if b.Tool == ToolClaude {
		p.Set("base_url", claudeNormalizeBaseURL(p.BaseURL()))
		return p
	}
	p.Set("base_url", codexNormalizeBaseURL(p.BaseURL()))
	if p.S("id") == "" {
		p.Set("id", b.uniqueID(codexSlug(p.Name()), p))
	}
	return p
}

// -- lookup

// Find resolves a 1-based position, an exact name, or a unique prefix / substring; -1 if none.
func (b *Backend) Find(query string) int {
	if n, err := strconv.Atoi(query); err == nil && strings.Trim(query, "0123456789") == "" {
		if n >= 1 && n <= len(b.Providers) {
			return n - 1
		}
	}
	q := strings.ToLower(query)
	tiers := []func(p *Provider) bool{
		func(p *Provider) bool { return p.Name() == query },
		func(p *Provider) bool { return strings.ToLower(p.Name()) == q },
		func(p *Provider) bool { return strings.HasPrefix(strings.ToLower(p.Name()), q) },
		func(p *Provider) bool {
			return strings.Contains(strings.ToLower(p.Name()), q) || strings.Contains(strings.ToLower(p.BaseURL()), q)
		},
	}
	for _, match := range tiers {
		hit, n := -1, 0
		for i, p := range b.Providers {
			if match(p) {
				hit = i
				n++
			}
		}
		if n == 1 {
			return hit
		}
	}
	return -1
}

// FindSame returns a saved provider that would configure the tool identically, or -1.
func (b *Backend) FindSame(p *Provider) int {
	url := strings.TrimRight(p.BaseURL(), "/")
	for i, q := range b.Providers {
		if strings.TrimRight(q.BaseURL(), "/") == url && q.Key() == p.Key() &&
			dictEqual(q.Dict("extra_env"), p.Dict("extra_env")) {
			return i
		}
	}
	return -1
}

// UniqueName makes name unique among the providers other than index exclude.
func (b *Backend) UniqueName(name string, exclude int) string {
	taken := func(n string) bool {
		for i, p := range b.Providers {
			if i != exclude && p.Name() == n {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	i := 2
	for taken(fmt.Sprintf("%s (%d)", name, i)) {
		i++
	}
	return fmt.Sprintf("%s (%d)", name, i)
}

// -- mutations (caller saves)

// Upsert stores provider at index original, or appends it when original is out of range.
func (b *Backend) Upsert(p *Provider, original int) int {
	if original >= 0 && original < len(b.Providers) {
		// keep the slot's identity out of the uniqueness check
		b.Providers[original] = p
		b.Normalize(p)
		return original
	}
	b.Normalize(p)
	b.Providers = append(b.Providers, p)
	return len(b.Providers) - 1
}

func (b *Backend) Remove(idx int) {
	b.Providers = append(b.Providers[:idx], b.Providers[idx+1:]...)
}

func (b *Backend) MoveBy(idx, delta int) int {
	j := max(0, min(idx+delta, len(b.Providers)-1))
	p := b.Providers[idx]
	b.Remove(idx)
	b.Providers = append(b.Providers[:j], append([]*Provider{p}, b.Providers[j:]...)...)
	return j
}

// FromInfo builds a new provider from parse() output.
func (b *Backend) FromInfo(info *Info) *Provider {
	p := b.Blank()
	for _, k := range info.Keys() {
		v, _ := info.Get(k)
		if !p.Has(k) || v.Empty() {
			continue
		}
		if v.D != nil {
			v.D = v.D.Clone()
		}
		p.vals.Set(k, v)
	}
	name := p.Name()
	if name == "" {
		name = "provider"
	}
	p.Set("name", b.UniqueName(name, -1))
	return b.Normalize(p)
}
