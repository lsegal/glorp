package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lsegal/glorp/agents"
)

// agentReferencePath is the docs-site page that documents the agent-definition
// schema. It is the only description of that schema outside the Go structs, so
// the tests below hold it to them.
const agentReferencePath = "site/content/agents.md"

// jsonFieldNames walks a definition struct and returns every JSON field name it
// exposes, descending into the nested blocks the schema is made of.
func jsonFieldNames(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, tag)
		names = append(names, jsonFieldNames(t, field.Type)...)
	}
	return names
}

// TestAgentReferenceDocumentsEverySchemaField keeps the reference page in step
// with the definition structs: a field that ships in the schema and not in the
// docs is exactly the drift the page exists to prevent.
func TestAgentReferenceDocumentsEverySchemaField(t *testing.T) {
	reference := readDoc(t, agentReferencePath)
	for _, name := range jsonFieldNames(t, reflect.TypeOf(agents.Definition{})) {
		if !strings.Contains(reference, "`"+name+"`") && !strings.Contains(reference, "."+name+"`") {
			t.Errorf("%s does not document the schema field %q", agentReferencePath, name)
		}
	}
}

// TestAgentReferenceDocumentsEveryBuiltinAgent keeps the shipped agents listed
// in the reference and in the README's `--agent` row, so neither claims a
// smaller set than glorp actually dispatches to.
func TestAgentReferenceDocumentsEveryBuiltinAgent(t *testing.T) {
	reference := readDoc(t, agentReferencePath)
	var agentRow string
	for _, line := range strings.Split(readDoc(t, "README.md"), "\n") {
		if strings.HasPrefix(line, "| `--agent AGENT") {
			agentRow = line
		}
	}
	if agentRow == "" {
		t.Fatal("README.md has no `--agent` flag row")
	}
	for _, name := range agents.MustBuiltin().Names() {
		if !strings.Contains(reference, "`"+name+"`") {
			t.Errorf("%s does not mention the built-in agent %q", agentReferencePath, name)
		}
		if !strings.Contains(agentRow, "`"+name+"`") {
			t.Errorf("README `--agent` row does not mention the built-in agent %q", name)
		}
	}
}

// TestAgentReferenceQuotesBuiltinDefinitions holds the worked examples on the
// reference page to the documents they claim to quote verbatim, so an edit to a
// built-in definition cannot leave a stale copy in the docs.
func TestAgentReferenceQuotesBuiltinDefinitions(t *testing.T) {
	reference := readDoc(t, agentReferencePath)
	entries, err := os.ReadDir(filepath.Join("agents", "builtin"))
	if err != nil {
		t.Fatalf("read built-in definitions: %v", err)
	}
	for _, entry := range entries {
		document := strings.TrimSpace(readDoc(t, filepath.Join("agents", "builtin", entry.Name())))
		if !strings.Contains(reference, document) {
			t.Errorf("%s does not quote agents/builtin/%s verbatim", agentReferencePath, entry.Name())
		}
	}
}

// prerequisiteLine returns the single line of doc that names every built-in
// agent as a prerequisite, found by the marker the file writes it behind.
func prerequisiteLine(t *testing.T, path, marker string) string {
	t.Helper()
	for _, line := range strings.Split(readDoc(t, path), "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("%s has no line containing %q", path, marker)
	return ""
}

// TestBuiltinAgentsLinkTheirDocumentation holds every place that names the
// shipped agents to naming them as links, so a new built-in cannot land as bare
// text next to five agents a reader can click through to install.
func TestBuiltinAgentsLinkTheirDocumentation(t *testing.T) {
	readme := prerequisiteLine(t, "README.md", "At least one supported coding agent")
	home := prerequisiteLine(t, "site/layouts/index.html", "on your PATH")
	reference := readDoc(t, agentReferencePath)
	for _, name := range agents.MustBuiltin().Names() {
		if !strings.Contains(readme, ") (`"+name+"`)") {
			t.Errorf("README prerequisites do not link a documentation page for the built-in agent %q", name)
		}
		if !strings.Contains(home, "</a> (<code>"+name+"</code>)") {
			t.Errorf("site/layouts/index.html prerequisites do not link a documentation page for the built-in agent %q", name)
		}
		var row string
		for _, line := range strings.Split(reference, "\n") {
			if strings.HasPrefix(line, "| `"+name+"` |") {
				row = line
			}
		}
		if row == "" {
			t.Errorf("%s has no built-in agents table row for %q", agentReferencePath, name)
			continue
		}
		if !strings.Contains(row, "](http") {
			t.Errorf("%s does not link a documentation page for the built-in agent %q", agentReferencePath, name)
		}
	}
}
