import sys

p = 'main.go'
s = open(p, encoding='utf-8').read()

old = '''	if index := strings.LastIndex(name, ":"); index >= 0 {
		spec.Level = strings.TrimSpace(name[index+1:])
		name = strings.TrimSpace(name[:index])
		if spec.Level != "low" && spec.Level != "medium" && spec.Level != "high" {
			return agentSpec{}, fmt.Errorf("agent level must be low, medium, or high")
		}
	}
	if base, model, ok := strings.Cut(name, "/"); ok {
		name, spec.Model = strings.TrimSpace(base), strings.TrimSpace(model)
		if spec.Model == "" {
			return agentSpec{}, fmt.Errorf("agent model cannot be empty")
		}
	}
	if name != "codex" && name != "claude" {
		return agentSpec{}, fmt.Errorf("agent must be codex or claude")
	}
	spec.Name = name
	return spec, nil
}'''
new = '''	if index := strings.LastIndex(name, ":"); index >= 0 {
		spec.Level = strings.TrimSpace(name[index+1:])
		name = strings.TrimSpace(name[:index])
		if spec.Level == "" {
			return agentSpec{}, fmt.Errorf("agent level cannot be empty")
		}
	}
	if base, model, ok := strings.Cut(name, "/"); ok {
		name, spec.Model = strings.TrimSpace(base), strings.TrimSpace(model)
		if spec.Model == "" {
			return agentSpec{}, fmt.Errorf("agent model cannot be empty")
		}
	}
	// Which agents exist, and which models and levels each accepts, comes from
	// the definition registry rather than a hardcoded pair of names, so an
	// agent added by a config file is accepted here without a code change.
	definition, ok := agentDefinition(name)
	if !ok {
		return agentSpec{}, agentRegistry().UnknownAgentError(name)
	}
	if !definition.AllowsLevel(spec.Level) {
		return agentSpec{}, fmt.Errorf("agent %s level must be %s", name, strings.Join(definition.Levels, ", "))
	}
	if !definition.AllowsModel(spec.Model) {
		return agentSpec{}, fmt.Errorf("agent %s model must be %s", name, strings.Join(definition.Models, ", "))
	}
	spec.Name = name
	return spec, nil
}'''
assert old in s
s = s.replace(old, new)

old2 = '''	spec := r.specForSession(session)
	if spec.Name == "codex" {
		args := []string{"exec"}
		if session.Resume {
			args = append(args, "resume")
		}
		if r.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if !session.Resume && spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		if !session.Resume && spec.Level != "" {
			args = append(args, "-c", "model_reasoning_effort="+spec.Level)
		}
		if session.Resume {
			args = append(args, session.ID)
		}
		return append(args, prompt)
	}
	args := []string{"-p"}
	if session.Resume {
		args = append(args, "--resume", session.ID)
	} else if session.ID != "" {
		args = append(args, "--session-id", session.ID)
	}
	if r.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		// Print mode cannot prompt for tool approval. Let Claude make its normal
		// permission decisions autonomously instead of silently denying the
		// shell commands the issue workflow needs and exiting successfully.
		args = append(args, "--permission-mode", "auto")
	}
	if r.RemoteControl {
		// Claude only reads --remote-control on its interactive startup path,
		// so under -p the flag alone does nothing. The bridge itself is not
		// gated on interactive mode: it starts from remoteControlAtStartup,
		// and --settings is an accepted source for that setting. Name the
		// session after the issue so a run is identifiable in the app rather
		// than appearing under a bare hostname.
		args = append(args, "--settings", remoteControlSettings, "--rc", remoteControlSessionName(target, issue))
	}
	if !session.Resume && spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if !session.Resume && spec.Level != "" {
		args = append(args, "--effort", spec.Level)
	}
	// Claude's default text output only prints once the full response is
	// ready. Stream JSON events instead so the dashboard shows live progress
	// the same way Codex's plain-text output already does.
	args = append(args, "--output-format", "stream-json", "--verbose")
	return append(args, prompt)
}'''
new2 = '''	spec := r.specForSession(session)
	definition := agentDefinitionOrDefault(spec.Name)
	if definition == nil {
		return nil
	}
	mode := agent.ModeRun
	if session.Resume {
		mode = agent.ModeResume
	}
	values := agent.Values{
		Prompt:  prompt,
		Session: session.ID,
		Yolo:    r.Yolo,
		// Claude only reads --remote-control on its interactive startup path,
		// so under -p the flag alone does nothing. The bridge itself is not
		// gated on interactive mode: it starts from remoteControlAtStartup,
		// and --settings is an accepted source for that setting. Name the
		// session after the issue so a run is identifiable in the app rather
		// than appearing under a bare hostname.
		RemoteControl:         r.RemoteControl,
		RemoteControlSettings: remoteControlSettings,
		RemoteControlName:     remoteControlSessionName(target, issue),
	}
	// The model and the level are withheld from a resume: the session already
	// runs with the ones it was started with, and its template has no fragment
	// that would take them.
	if !session.Resume {
		values.Model, values.Level = spec.Model, spec.Level
	}
	return definition.RenderArgs(mode, values)
}'''
assert old2 in s
s = s.replace(old2, new2)

old3 = '''func (r CommandRunner) binary(agent string) string {
	if agent == "codex" && r.CodexBinary != "" {
		return r.CodexBinary
	}
	if agent == "claude" && r.ClaudeBinary != "" {
		return r.ClaudeBinary
	}
	return r.Binary
}'''
new3 = '''func (r CommandRunner) binary(name string) string {
	switch name {
	case "codex":
		if r.CodexBinary != "" {
			return r.CodexBinary
		}
	case "claude":
		if r.ClaudeBinary != "" {
			return r.ClaudeBinary
		}
	default:
		// An agent that arrived from a definition has no --<agent>-binary flag
		// of its own yet (issue #489), so it runs from the executable its
		// definition names rather than inheriting the codex default.
		if definition, ok := agentDefinition(name); ok && definition.Binary != "" {
			return definition.Binary
		}
	}
	return r.Binary
}'''
assert old3 in s
s = s.replace(old3, new3)

old4 = '''	session.Resume = false
	if r.specForSession(session).Name == "codex" {
		// Codex assigns its own session IDs and reports the new one on stdout.
		session.ID = ""
	}'''
new4 = '''	session.Resume = false
	if definition := agentDefinitionOrDefault(r.specForSession(session).Name); definition != nil && definition.Session.ClearOnResumeFailure {
		// The agent assigns its own session IDs and reports the new one on
		// stdout, so keeping the old one would ask again for the session that
		// has just proved to be gone.
		session.ID = ""
	}'''
assert old4 in s
s = s.replace(old4, new4)

old5 = '''	agent := r.specForSession(session).Name
	args := commandArgsForSession(r, issue, session)
	cmd := newAgentCommand(ctx, r.binary(agent), args...)
	if agent == "claude" {
		// Claude Code's headless print mode (-p) caps how long it waits for
		// in-flight background shell tasks before terminating them (10
		// minutes by default). glorp dispatches claude for long-lived
		// autonomous work, so disable the ceiling to prevent it from killing
		// legitimate background tasks mid-run (issue #330).
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0")
	}'''
new5 = '''	name := r.specForSession(session).Name
	definition := agentDefinitionOrDefault(name)
	if definition == nil {
		return agentRegistry().UnknownAgentError(name), false
	}
	args := commandArgsForSession(r, issue, session)
	cmd := newAgentCommand(ctx, r.binary(name), args...)
	// The definition supplies whatever the agent needs on top of glorp's own
	// environment, such as Claude's CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0,
	// which stops headless print mode from killing the long-lived background
	// shell tasks the issue workflow depends on (issue #330).
	if len(definition.Env) > 0 {
		cmd.Env = append(os.Environ(), definition.EnvPairs()...)
	}'''
assert old5 in s
s = s.replace(old5, new5)

old6 = '''		metadataOutput = &sessionMetadataCaptureWriter{
			output: agentOutput, onUpdate: updateSession,
			captureSession: agent == "codex" && !session.Resume,
		}
		agentOutput = metadataOutput
	}
	var claudeOutput *claudeJSONOutputWriter
	if agent == "claude" {
		claudeOutput = newClaudeJSONOutputWriter(agentOutput)
		agentOutput = claudeOutput
	}'''
new6 = '''		metadataOutput = &sessionMetadataCaptureWriter{
			output: agentOutput, onUpdate: updateSession,
			// An agent that reports its own session ID on stdout is read for it
			// on the run that creates the session; a resume already carries the
			// ID glorp recorded.
			captureSession: definition.SessionPattern() != nil && !session.Resume,
			sessionPattern: definition.SessionPattern(),
		}
		agentOutput = metadataOutput
	}
	var claudeOutput *claudeJSONOutputWriter
	if definition.Output.Format == agent.OutputClaudeStreamJSON {
		claudeOutput = newClaudeJSONOutputWriter(agentOutput)
		agentOutput = claudeOutput
	}'''
assert old6 in s
s = s.replace(old6, new6)

s = s.replace('\t"github.com/lsegal/glorp/browser"\n', '\t"github.com/lsegal/glorp/agent"\n\t"github.com/lsegal/glorp/browser"\n', 1)

open(p, 'w', encoding='utf-8', newline='').write(s)
print('main.go patched')
