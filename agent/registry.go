package agent

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed builtin/*.json
var builtinFS embed.FS

// BuiltinSource names the built-in definitions in error messages, so a broken
// embedded definition is not mistaken for something the user wrote.
const BuiltinSource = "built-in agent definition"

// Registry holds the agent definitions a run knows about: the built-ins glorp
// ships, plus whatever a config file overrode or added.
type Registry struct {
	definitions map[string]*Definition
}

// Builtins loads the embedded definitions. It is the registry every run starts
// from, before any config file is merged over it.
func Builtins() (*Registry, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read built-in agent definitions: %w", err)
	}
	registry := &Registry{definitions: make(map[string]*Definition, len(entries))}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := builtinFS.ReadFile(path.Join("builtin", name))
		if err != nil {
			return nil, fmt.Errorf("read built-in agent definition %s: %w", name, err)
		}
		definition := &Definition{}
		if err := decodeStrict(data, definition); err != nil {
			return nil, fmt.Errorf("%s %s: %w", BuiltinSource, name, err)
		}
		if err := definition.Validate(BuiltinSource + " " + name); err != nil {
			return nil, err
		}
		registry.definitions[definition.Name] = definition
	}
	return registry, nil
}

// Lookup returns the definition for an agent name.
func (r *Registry) Lookup(name string) (*Definition, bool) {
	if r == nil {
		return nil, false
	}
	definition, ok := r.definitions[name]
	return definition, ok
}

// Names lists every known agent in sorted order, for validation errors and for
// the UI lists that offer the configured agents.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Clone copies the registry so a caller can merge config over it without
// disturbing the built-ins another run reads.
func (r *Registry) Clone() *Registry {
	clone := &Registry{definitions: make(map[string]*Definition, len(r.definitions))}
	for name, definition := range r.definitions {
		copied := *definition
		copied.Env = copyEnv(definition.Env)
		clone.definitions[name] = &copied
	}
	return clone
}

func copyEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	copied := make(map[string]string, len(env))
	for key, value := range env {
		copied[key] = value
	}
	return copied
}

// Merge applies one definition over the registry. A definition whose name
// matches an existing one overrides it field by field: fields the override
// leaves out keep the value they had, so a config file that only changes the
// binary does not have to restate the argv. An unknown name registers a new
// agent. source names the file for the error message.
func (r *Registry) Merge(source string, name string, raw json.RawMessage) error {
	merged := &Definition{}
	if existing, ok := r.definitions[name]; ok {
		copied := *existing
		copied.Env = copyEnv(existing.Env)
		merged = &copied
	} else if name != "" {
		merged.Name = name
	}
	if err := decodeStrict(raw, merged); err != nil {
		return fmt.Errorf("%s: agent %q: %w", source, displayName(name, merged.Name), err)
	}
	if name != "" && merged.Name != "" && merged.Name != name {
		return fmt.Errorf("%s: agent %q: field %q: definition is keyed by %q but names itself %q", source, name, "name", name, merged.Name)
	}
	if merged.Name == "" {
		merged.Name = name
	}
	if err := merged.Validate(source); err != nil {
		return err
	}
	r.definitions[merged.Name] = merged
	return nil
}

func displayName(keyed, declared string) string {
	if keyed != "" {
		return keyed
	}
	return declared
}

// decodeStrict decodes JSON into an existing value, rejecting fields the
// schema does not define. Decoding over a value already holding a built-in is
// what gives the field-by-field merge: absent fields keep what was there.
func decodeStrict(data []byte, into interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	// Reject trailing content so a file with two concatenated objects is an
	// error rather than a silently dropped second definition.
	if decoder.More() {
		return fmt.Errorf("unexpected trailing JSON after the definition")
	}
	return nil
}

// UnknownAgentError reports an --agent value the registry does not define,
// listing what it does define. A silently dropped definition looks exactly
// like a typo in --agent, so the message always names the alternatives.
func (r *Registry) UnknownAgentError(name string) error {
	known := r.Names()
	if len(known) == 0 {
		return fmt.Errorf("unknown agent %q; no agents are defined", name)
	}
	return fmt.Errorf("unknown agent %q; known agents are %s", name, strings.Join(known, ", "))
}
