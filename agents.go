package main

import (
	"strings"
	"sync"

	"github.com/lsegal/glorp/agent"
)

// defaultAgentName is the agent a run falls back to when nothing names one.
// It is the historical default rather than anything the registry decides, so
// adding a definition alphabetically before it does not silently move it.
const defaultAgentName = "codex"

// The registry is process-wide because the places that need it -- flag
// parsing, --agent validation, session dispatch, argv rendering -- are spread
// across the program and none of them is reached with a runner in hand. It is
// replaced once at startup, after --config is read, and only read afterwards,
// so the mutex is what keeps `go test -race` clean rather than a hot path.
var (
	agentRegistryMu    sync.RWMutex
	agentRegistryValue *agent.Registry
)

// agentRegistry returns the definitions this process runs on, loading the
// built-ins the first time it is asked. A run that never reads a config file
// -- every test, and `glorp watch` with no config present -- gets exactly the
// embedded definitions.
func agentRegistry() *agent.Registry {
	agentRegistryMu.RLock()
	registry := agentRegistryValue
	agentRegistryMu.RUnlock()
	if registry != nil {
		return registry
	}
	agentRegistryMu.Lock()
	defer agentRegistryMu.Unlock()
	if agentRegistryValue == nil {
		builtins, err := agent.Builtins()
		if err != nil {
			// The definitions are embedded in the binary, so this cannot fail
			// on a build that passed its own tests. An empty registry still
			// reports every agent as unknown rather than crashing a watch.
			builtins = &agent.Registry{}
		}
		agentRegistryValue = builtins
	}
	return agentRegistryValue
}

// setAgentRegistry installs the registry a config file produced.
func setAgentRegistry(registry *agent.Registry) {
	agentRegistryMu.Lock()
	agentRegistryValue = registry
	agentRegistryMu.Unlock()
}

// agentDefinition looks an agent up by the exact name --agent used.
func agentDefinition(name string) (*agent.Definition, bool) {
	return agentRegistry().Lookup(name)
}

// agentDefinitionOrDefault resolves the definition to run with, falling back to
// the default agent when the name is empty or names something the registry no
// longer defines -- work state written before a definition was removed, for
// instance. It returns nil only when there are no definitions at all.
func agentDefinitionOrDefault(name string) *agent.Definition {
	registry := agentRegistry()
	if definition, ok := registry.Lookup(name); ok {
		return definition
	}
	if definition, ok := registry.Lookup(defaultAgentName); ok {
		return definition
	}
	for _, known := range registry.Names() {
		if definition, ok := registry.Lookup(known); ok {
			return definition
		}
	}
	return nil
}

// agentNames lists every defined agent, for error messages and help text.
func agentNames() []string { return agentRegistry().Names() }

// configPathFromArgs finds --config before the flag set is parsed. --agent is
// validated against the registry as the flag is read, and Go's flag package
// reads flags in the order they were typed, so the config file has to be
// loaded before parsing rather than during it; otherwise `glorp watch --agent
// muse --config .glorp.config.json` would reject the agent the file defines.
func configPathFromArgs(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return "", false
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		if name == arg {
			continue
		}
		if value, ok := strings.CutPrefix(name, "config="); ok {
			return value, true
		}
		if name == "config" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// loadAgentRegistry reads the agent definitions a run starts with: the
// built-ins, with the config file merged over them. An explicitly named
// --config that does not exist is still not an error, matching the flag's
// documented default, but anything wrong inside one is.
func loadAgentRegistry(args []string) error {
	path, _ := configPathFromArgs(args)
	if path == "" {
		path = agent.DefaultConfigPath
	}
	registry, err := agent.Load(path)
	if err != nil {
		return err
	}
	setAgentRegistry(registry)
	return nil
}
