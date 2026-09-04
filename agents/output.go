package agents

import (
	"fmt"
	"strings"
)

// Decoder returns the canonical format name the definition's output is decoded
// with, resolving the aliases a definition may spell it as. An unrecognised
// value never reaches here: Validate rejects it.
func (o Output) Decoder() string {
	if format, ok := outputFormats[strings.TrimSpace(o.Format)]; ok {
		return format
	}
	return FormatText
}

// validate reports the first thing wrong with the output block.
func (o Output) validate() error {
	format, ok := outputFormats[strings.TrimSpace(o.Format)]
	if !ok {
		return fmt.Errorf(`field "output.format": %q must be one of %s`, o.Format, JoinOr(outputFormatNames()))
	}
	if format != FormatJSONL {
		if o.JSONL != nil {
			return fmt.Errorf(`field "output.jsonl" is only meaningful when "output.format" is %q`, FormatJSONL)
		}
		return nil
	}
	if o.JSONL == nil {
		return fmt.Errorf(`field "output.jsonl" is required when "output.format" is %q`, FormatJSONL)
	}
	return o.JSONL.validate()
}

func (j JSONL) validate() error {
	for field, path := range map[string]string{
		"output.jsonl.type": j.Type, "output.jsonl.text": j.Text,
		"output.jsonl.toolName": j.ToolName, "output.jsonl.toolInput": j.ToolInput,
	} {
		if path == "" {
			continue
		}
		if !jsonlPathPattern.MatchString(path) {
			return fmt.Errorf(`field %q: %q must be dot-separated object keys, each optionally suffixed with [] to step into an array`, field, path)
		}
	}
	if j.Text == "" && j.ToolName == "" {
		return fmt.Errorf(`field "output.jsonl" needs at least one of "text" or "toolName"; a decoder that reads neither renders nothing`)
	}
	if j.ToolInput != "" && j.ToolName == "" {
		return fmt.Errorf(`field "output.jsonl.toolInput" needs "output.jsonl.toolName"; a tool input is rendered as part of the call it belongs to`)
	}
	if j.TextDelta && j.Text == "" {
		return fmt.Errorf(`field "output.jsonl.textDelta" needs "output.jsonl.text"; there is no text to join without it`)
	}
	if len(j.Ignore) > 0 && j.Type == "" {
		return fmt.Errorf(`field "output.jsonl.ignore" needs "output.jsonl.type"; event types cannot be skipped without knowing where the type is`)
	}
	for _, ignored := range j.Ignore {
		if strings.TrimSpace(ignored) == "" {
			return fmt.Errorf(`field "output.jsonl.ignore" cannot contain an empty value`)
		}
	}
	return nil
}

// MissingSessionPatterns are the phrases that mean the agent no longer holds
// the session it was asked to resume, so the work is restarted rather than
// reported as a failure. The shared defaults always apply; a definition's own
// phrases are added to them, which is what keeps one agent's distinctive
// wording out of every other agent's detection.
func (d Definition) MissingSessionPatterns() []string {
	if len(d.MissingSession) == 0 {
		return DefaultMissingSessionPatterns
	}
	patterns := make([]string, 0, len(DefaultMissingSessionPatterns)+len(d.MissingSession))
	patterns = append(patterns, DefaultMissingSessionPatterns...)
	return append(patterns, d.MissingSession...)
}
