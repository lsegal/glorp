package agent

import (
	"sort"
	"sync"
)

// EnvPairs renders the definition's extra environment as NAME=VALUE entries,
// sorted so a child process is built the same way every run and a test can
// compare the whole slice.
func (d *Definition) EnvPairs() []string {
	if len(d.Env) == 0 {
		return nil
	}
	names := make([]string, 0, len(d.Env))
	for name := range d.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+d.Env[name])
	}
	return pairs
}

// builtins is the embedded registry, loaded once. Builtin reads it for callers
// that hold only an agent name and no registry of their own -- the browser
// driver's vision fallback, which is handed a definition by the run but has to
// stay usable without one.
var builtins = sync.OnceValue(func() *Registry {
	registry, err := Builtins()
	if err != nil {
		return &Registry{}
	}
	return registry
})

// Builtin looks up one of the embedded definitions by name.
func Builtin(name string) (*Definition, bool) { return builtins().Lookup(name) }
