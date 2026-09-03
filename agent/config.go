package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

// DefaultConfigPath is the config file glorp reads agent definitions from when
// --config names nothing else. It is deliberately not the --state file: glorp
// never writes this one, so hand-edited definitions cannot be clobbered by a
// work-state save.
const DefaultConfigPath = ".glorp.config.json"

// config is the whole shape of that file. The top level is an object rather
// than a bare array of agents so later config sections have somewhere to go.
type config struct {
	Agents json.RawMessage `json:"agents"`
}

// Load returns the built-in definitions with the config file at path merged
// over them. A missing file is not an error: glorp runs on the built-ins
// alone. Anything else wrong with the file is, and the error names the file,
// the agent, and the field, because a definition that is silently dropped is
// indistinguishable from a typo in --agent.
//
// Merging is field by field. A definition whose name matches a built-in
// overrides only the fields it states; everything it leaves out keeps the
// built-in value, so a file that changes nothing but the binary does not have
// to restate the argv. A name the built-ins do not define registers a new
// agent, which then has to supply the fields a definition needs on its own.
//
// An inherited field is nulled out by stating its empty value rather than by
// omitting it, since omission is what "keep the built-in" means: "binary": ""
// (rejected, an agent needs one), "levels": [] to drop an allow-list,
// "env": null to drop every inherited variable, "args": {"vision": []} to say
// the agent has no such invocation, and "capturePattern": "" to stop reading a
// session ID off stdout. Maps merge key by key, so "env" is the one field
// where a partial object adds to what the built-in set rather than replacing
// it; null replaces it with nothing.
func Load(path string) (*Registry, error) {
	registry, err := Builtins()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return registry, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return registry, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent config %s: %w", path, err)
	}
	if err := registry.Apply(path, data); err != nil {
		return nil, err
	}
	return registry, nil
}

// Apply merges the contents of one config file into the registry.
func (r *Registry) Apply(source string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var parsed config
	if err := decodeStrict(data, &parsed); err != nil {
		if hint := workStateHint(source, data); hint != nil {
			return hint
		}
		return fmt.Errorf("decode agent config %s: %w", source, err)
	}
	if len(parsed.Agents) == 0 {
		return nil
	}
	entries, err := agentEntries(source, parsed.Agents)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := r.Merge(source, entry.name, entry.raw); err != nil {
			return err
		}
	}
	return nil
}

type agentEntry struct {
	name string
	raw  json.RawMessage
}

// agentEntries accepts either shape the schema allows: an array of definitions
// that each name themselves, or an object keyed by agent name.
func agentEntries(source string, raw json.RawMessage) ([]agentEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("decode agent config %s: field %q: %w", source, "agents", err)
		}
		entries := make([]agentEntry, 0, len(list))
		for i, item := range list {
			// The array form carries each agent's name inside the definition,
			// so it has to be read before the merge rather than after: the name
			// is what decides whether this overrides a built-in or adds an
			// agent.
			var named struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &named); err != nil {
				return nil, fmt.Errorf("decode agent config %s: field %q: entry %d: %w", source, "agents", i, err)
			}
			entries = append(entries, agentEntry{name: named.Name, raw: item})
		}
		return entries, nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keyed); err != nil {
		return nil, fmt.Errorf("decode agent config %s: field %q: must be an array of agent definitions or an object keyed by agent name: %w", source, "agents", err)
	}
	names := make([]string, 0, len(keyed))
	for name := range keyed {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]agentEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, agentEntry{name: name, raw: keyed[name]})
	}
	return entries, nil
}

// workStateKeyPattern matches the keys saveScopedWorkState writes: a bare
// issue number, or a target-scoped "owner/repo#number".
var workStateKeyPattern = regexp.MustCompile(`^(?:[^#]+#)?\d+$`)

// workStateHint recognises a --state work-state file handed to --config and
// says which file it belongs in, rather than reporting it as an unknown field.
// The two files are easy to mix up and the generic error explains nothing.
func workStateHint(source string, data []byte) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || len(raw) == 0 {
		return nil
	}
	for key, value := range raw {
		if !workStateKeyPattern.MatchString(key) {
			return nil
		}
		var record struct {
			Status string `json:"status"`
		}
		var legacy bool
		if json.Unmarshal(value, &legacy) == nil {
			continue
		}
		if json.Unmarshal(value, &record) != nil || record.Status == "" {
			return nil
		}
	}
	return fmt.Errorf("%s holds work-state records, not agent definitions: work state belongs in the --state file (default %s) and agent definitions in --config (default %s)", source, "\".glorp.json\"", DefaultConfigPath)
}

// ErrWorkStateHoldsDefinitions is the mirror of the guard above, reported by
// the work-state loader when agent definitions turn up in the --state file.
func ErrWorkStateHoldsDefinitions(source string) error {
	return fmt.Errorf("%s holds agent definitions, not work state: agent definitions belong in the --config file (default %s) and work state in --state", source, DefaultConfigPath)
}
