package agents

import (
	"strings"
	"testing"
)

// TestModelsFromJSONReadsACatalogDocument checks a CLI that prints its whole
// catalog as one JSON object is read through the path the definition names,
// which is how codex answers `codex debug models`.
func TestModelsFromJSONReadsACatalogDocument(t *testing.T) {
	doctor := Doctor{Models: []string{"codex", "debug", "models"}, ModelsJSON: "models[].slug"}
	output := `{"models":[{"slug":"gpt-5.6-sol"},{"slug":"gpt-5.6-terra"}]}`
	if got := strings.Join(doctor.ModelsFrom(output), ","); got != "gpt-5.6-sol,gpt-5.6-terra" {
		t.Errorf("ModelsFrom() = %q, want both slugs in the order printed", got)
	}
}

// TestModelsFromJSONFiltersHiddenCatalogRows checks the key=value selector
// keeps the rows a catalog marks as listed, so a model the CLI hides from its
// own picker is not reported as one to write after --agent.
func TestModelsFromJSONFiltersHiddenCatalogRows(t *testing.T) {
	doctor := Doctor{Models: []string{"codex", "debug", "models"}, ModelsJSON: "models[visibility=list].slug"}
	output := `{"models":[{"slug":"hidden","visibility":"hide"},{"slug":"shown","visibility":"list"}]}`
	if got := strings.Join(doctor.ModelsFrom(output), ","); got != "shown" {
		t.Errorf("ModelsFrom() = %q, want only the listed row", got)
	}
}

// TestModelsFromJSONReadsOneResponseAmongMany checks a probe that talks a
// stdio protocol is read line by line: the answer arrives as one JSON-RPC
// response among a handshake and whatever else the agent prints.
func TestModelsFromJSONReadsOneResponseAmongMany(t *testing.T) {
	doctor := Doctor{
		Models:      []string{"cline", "--acp"},
		ModelsStdin: []string{`{"jsonrpc":"2.0","id":0,"method":"initialize"}`},
		ModelsJSON:  "result.models.availableModels[].modelId",
	}
	output := strings.Join([]string{
		"[acp] starting ACP mode over stdio",
		`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"models":{"availableModels":[{"modelId":"anthropic/claude-opus-5"},{"modelId":"openai/gpt-5.6-sol"}]}}}`,
	}, "\n")
	if got := strings.Join(doctor.ModelsFrom(output), ","); got != "anthropic/claude-opus-5,openai/gpt-5.6-sol" {
		t.Errorf("ModelsFrom() = %q, want the ids out of the one response that carries them", got)
	}
}

// TestModelsFromJSONIgnoresOutputThatIsNotTheAnswer checks noise, errors, and
// a document shaped differently produce no models rather than garbage: the
// report tells "listed nothing" from "listed something" and nothing else.
func TestModelsFromJSONIgnoresOutputThatIsNotTheAnswer(t *testing.T) {
	doctor := Doctor{Models: []string{"gemini", "--acp"}, ModelsJSON: "result.models.availableModels[].modelId"}
	for _, output := range []string{
		"",
		"not json at all",
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"API key is missing"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"models":{"availableModels":[{"name":"no id here"}]}}}`,
	} {
		if models := doctor.ModelsFrom(output); len(models) != 0 {
			t.Errorf("ModelsFrom(%q) = %v, want nothing", output, models)
		}
	}
}

// TestModelsFromLinesStillReadsAPlainList checks the line reader is untouched
// by the JSON path: a CLI that prints one model per line is still read that
// way, filtered by its pattern.
func TestModelsFromLinesStillReadsAPlainList(t *testing.T) {
	doctor := Doctor{Models: []string{"opencode", "models"}, ModelPattern: `^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+$`}
	output := "Models:\nanthropic/claude-opus-5\nopenai/gpt-5.6-sol\n"
	if got := strings.Join(doctor.ModelsFrom(output), ","); got != "anthropic/claude-opus-5,openai/gpt-5.6-sol" {
		t.Errorf("ModelsFrom() = %q, want the decorated list read as before", got)
	}
}

// TestParseModelPathRejectsMalformedExpressions checks the parser names the
// segment it could not read, so a definition author is told which one is wrong
// rather than left with an empty model list.
func TestParseModelPathRejectsMalformedExpressions(t *testing.T) {
	for _, expr := range []string{"", "   ", "models[", "models[=list].slug", "models[bad].id", ".slug"} {
		if _, err := parseModelPath(expr); err == nil {
			t.Errorf("parseModelPath(%q) error = nil, want it rejected", expr)
		}
	}
}
