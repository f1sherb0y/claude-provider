// Loads/saves providers.json and wires up the backends.
package main

import (
	"errors"
	"fmt"
	"os"
)

type Store struct {
	LastTab  string
	Backends []*Backend
}

func LoadStore() (*Store, error) {
	path := dataFile()
	data := NewOMap[any]()
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		v, err := decodeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		obj, ok := v.(*JObj)
		if !ok {
			return nil, fmt.Errorf("%s: 不是 JSON 对象", path)
		}
		data = obj
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if data.Has("providers") {
		// v0.1 format: flat Claude-only
		m := NewOMap[any]()
		m.Set("claude", data)
		data = m
	}
	s := &Store{LastTab: "claude"}
	if t, ok := data.m["last_tab"].(string); ok {
		s.LastTab = t
	}
	fresh := false
	for _, t := range allTools {
		state, ok := data.Get(t.ID())
		fresh = fresh || !ok
		b, err := NewBackend(t, state)
		if err != nil {
			return nil, err
		}
		s.Backends = append(s.Backends, b)
	}
	if fresh {
		if err := s.Save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Save() error {
	m := NewOMap[any]()
	m.Set("last_tab", s.LastTab)
	for _, b := range s.Backends {
		m.Set(b.Tool.ID(), b.dumpState())
	}
	return writeJSON(dataFile(), m)
}

func (s *Store) Get(tool Tool) *Backend {
	for _, b := range s.Backends {
		if b.Tool == tool {
			return b
		}
	}
	panic("backend")
}

// -- mutations that persist

// Apply switches the tool to provider idx; the note mentions side effects worth showing.
func (s *Store) Apply(tool Tool, idx int) (string, error) {
	note, err := s.Get(tool).Apply(idx)
	if err != nil {
		return "", err
	}
	// bookkeeping (applied keys, generated ids) changed
	return note, s.Save()
}

func (s *Store) Upsert(tool Tool, p *Provider, original int) (int, error) {
	i := s.Get(tool).Upsert(p, original)
	return i, s.Save()
}

func (s *Store) Remove(tool Tool, idx int) error {
	s.Get(tool).Remove(idx)
	return s.Save()
}

func (s *Store) MoveBy(tool Tool, idx, delta int) (int, error) {
	j := s.Get(tool).MoveBy(idx, delta)
	return j, s.Save()
}
