package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The screenshot fallback exists for one situation only: GitHub changed its
// markup and the DOM extractor in browserissues.go (or browserproject.go) no
// longer recognises a page that plainly does contain items. It is a bridge
// until extraction is fixed in code, not a strategy, so every part of it is
// bounded. In the steady state a watch run makes zero vision calls no matter
// how long it polls, for issue pages and project boards alike.
const (
	// browserVisionCooldown is the minimum time between screenshots of the
	// same target. A poll loop hitting a permanently broken page
	// would otherwise queue an agent call every tick.
	browserVisionCooldown = 10 * time.Minute
	// browserVisionRunLimit is how many vision calls a single run may make
	// before the fallback switches itself off for the rest of the run. The cap
	// is shared across every target and every page kind: if three screenshots
	// did not recover a list, a fourth will not either; the extractor needs
	// fixing.
	browserVisionRunLimit = 3
	// browserVisionTimeout bounds one agent invocation, so a wedged agent
	// cannot stall the poll loop it was called from.
	browserVisionTimeout = 2 * time.Minute
)

// browserScreenshotter is the part of a tab the fallback needs. It is kept
// separate from browserPage because screenshots are only ever taken on the
// failure path: a page source that cannot produce one simply has no fallback.
type browserScreenshotter interface {
	Screenshot() ([]byte, error)
}

// browserVisionRef is one issue the agent read off a screenshot. Repository is
// empty for a repository target, where the page itself fixes the repository
// and a bare number is unambiguous; it is always set for a project board,
// which spans repositories and where a bare number cannot be turned back into
// an addressable issue.
//
// Status is the board's Status column for the item, and is only ever set for a
// project target: it is what the --ready-state gate reads, so a recovered board
// item without it is addressable but undispatchable (issue #398). It is empty
// when the board itself shows the item no status, which is a real answer rather
// than a parse failure.
type browserVisionRef struct {
	Repository string
	Number     int
	Status     string
}

// browserVision is the budget around the screenshot-to-agent fallback. It owns
// the accounting (per-target cooldown, per-run cap) so the issue and board
// sources cannot spend the budget by accident, and it logs every call it does
// make with the reason and the running count so a runaway is obvious in the
// dashboard. One instance is shared by every target, so the per-run cap is a
// budget for the whole run rather than per page kind.
type browserVision struct {
	cooldown time.Duration
	limit    int
	// ask performs one vision call and returns the issues the agent read off
	// the screenshot. qualified selects the answer shape it must ask for and
	// accept. Tests replace it; production wires it to an agent invocation.
	ask  func(ctx context.Context, screenshot []byte, pageURL string, qualified bool) ([]browserVisionRef, error)
	logf func(string, ...interface{})
	now  func() time.Time

	mu sync.Mutex
	// calls counts vision calls attempted this run, including ones that failed
	// or returned nothing usable: a failing call costs the same as a good one.
	calls int
	// disabled latches once the per-run cap is reached and is never cleared.
	disabled bool
	// lastCall is when each target last spent a screenshot.
	lastCall map[string]time.Time
}

// newBrowserVision builds the fallback for a watch run, dispatching screenshots
// to the same agent the run is otherwise configured to use.
func newBrowserVision(runner CommandRunner, logf func(string, ...interface{})) *browserVision {
	agent := browserVisionAgent{Runner: runner}
	return &browserVision{
		cooldown: browserVisionCooldown,
		limit:    browserVisionRunLimit,
		ask:      agent.Ask,
		logf:     logf,
		now:      time.Now,
		lastCall: map[string]time.Time{},
	}
}

// Recover spends one unit of budget, if any is left, to read the issues off a
// screenshot of a page the DOM extractor could not read. qualified asks the
// agent for OWNER/REPO#NUMBER strings, which is what a project board needs;
// without it the agent is asked for bare numbers, which is what an issues page
// needs. It returns nil whenever the budget forbids the call, the screenshot
// fails, or the agent's answer is not the strict shape it was asked for;
// nothing is ever retried.
//
// A nil receiver means the fallback is off, which is the default.
func (v *browserVision) Recover(ctx context.Context, target, pageURL, reason string, screenshot func() ([]byte, error), qualified bool) []browserVisionRef {
	if v == nil || screenshot == nil {
		return nil
	}
	call, last, ok := v.reserve(target)
	if !ok {
		return nil
	}
	v.log("browser vision: reading %s from a screenshot because the page could not be read (%s) [call %d of %d this run]", pageURL, reason, call, v.limit)
	if last {
		v.log("browser vision: that was the last of %d screenshot(s) allowed per run; the fallback is off for the rest of this run and the page extractor needs fixing", v.limit)
	}
	image, err := screenshot()
	if err != nil {
		v.log("browser vision: could not capture a screenshot of %s: %v", pageURL, err)
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, browserVisionTimeout)
	defer cancel()
	refs, err := v.ask(callCtx, image, pageURL, qualified)
	if err != nil {
		v.log("browser vision: discarded the agent's answer for %s: %v", pageURL, err)
		return nil
	}
	if len(refs) == 0 {
		v.log("browser vision: the agent found no issue numbers on %s", pageURL)
		return nil
	}
	v.log("browser vision: recovered %d issue number(s) from %s; fix the page extractor rather than relying on this", len(refs), pageURL)
	return refs
}

// reserve applies the budget and, when a call is allowed, records it before it
// happens. Charging up front is deliberate: a call that panics, times out, or
// returns garbage must still count against the cap and the cooldown, otherwise
// a persistently broken page would be free to retry forever.
// It reports the call's number and whether that call exhausted the run's cap.
func (v *browserVision) reserve(target string) (int, bool, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.disabled {
		return 0, false, false
	}
	now := v.now()
	if last, ok := v.lastCall[target]; ok && now.Sub(last) < v.cooldown {
		return 0, false, false
	}
	v.lastCall[target] = now
	v.calls++
	if v.calls >= v.limit {
		v.disabled = true
	}
	return v.calls, v.disabled, true
}

func (v *browserVision) log(format string, args ...interface{}) {
	if v.logf != nil {
		v.logf(format, args...)
	}
}

// browserVisionAgent turns one screenshot into a list of issues by invoking the
// run's configured agent on it.
type browserVisionAgent struct {
	Runner CommandRunner
}

// browserVisionPrompt asks for a bare JSON array and nothing else. Anything
// else the agent says is discarded rather than re-prompted, so the instruction
// is deliberately narrow. A project board is asked for OWNER/REPO#NUMBER
// strings because its rows come from several repositories and a bare number
// there names no particular issue.
func browserVisionPrompt(imagePath, pageURL string, qualified bool) string {
	if qualified {
		return fmt.Sprintf(`The image at %s is a screenshot of the GitHub project board at %s.

The board's items come from several repositories, so a bare issue number is not enough, and each item's Status column decides whether it is ready to work on. Reply with a strict JSON array of objects, one per issue listed on that page, each with exactly two fields: "ref", the issue as "OWNER/REPO#NUMBER", and "status", the item's Status column copied verbatim as it is written on the board. Reply with that and absolutely nothing else: no prose, no explanation, no markdown code fence. For example: [{"ref":"octocat/hello-world#412","status":"Todo"},{"ref":"octocat/spoon-knife#398","status":"In Progress"}]

If an item shows no status at all, use "" for its status; do not guess one. If the board shows no issues, reply with []. Do not open a browser, run commands, or read anything other than the image.`, imagePath, pageURL)
	}
	return fmt.Sprintf(`The image at %s is a screenshot of the GitHub issue list at %s.

Reply with a strict JSON array of the issue numbers listed on that page, and absolutely nothing else: no prose, no explanation, no markdown code fence. For example: [412,398,377]

If the page shows no issues, reply with []. Do not open a browser, run commands, or read anything other than the image.`, imagePath, pageURL)
}

// Ask writes the screenshot where the agent can read it, runs the agent once,
// and parses its answer. The agent's spec (model and reasoning level) is the
// run's configured one, read without advancing the round-robin cursor that
// load balances real issue dispatch.
func (a browserVisionAgent) Ask(ctx context.Context, screenshot []byte, pageURL string, qualified bool) ([]browserVisionRef, error) {
	dir, err := os.MkdirTemp("", "glorp-vision-")
	if err != nil {
		return nil, fmt.Errorf("write screenshot: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "issues.png")
	if err := os.WriteFile(path, screenshot, 0o600); err != nil {
		return nil, fmt.Errorf("write screenshot: %w", err)
	}
	spec := a.Runner.specForSession(AgentSession{})
	args := browserVisionArgs(spec, a.Runner.Yolo, path, pageURL, qualified)
	cmd := newAgentCommand(ctx, a.Runner.binary(spec.Name), args...)
	cmd.Dir = os.TempDir()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := runChildProcess(cmd); err != nil {
		return nil, fmt.Errorf("agent failed: %w", err)
	}
	return parseBrowserVisionRefs(output.String(), qualified)
}

// browserVisionArgs builds the one-shot agent invocation. Codex takes the image
// as a flag; Claude reads the path named in the prompt. Neither is asked for
// structured streaming output, because the answer is expected to be one line.
func browserVisionArgs(spec agentSpec, yolo bool, imagePath, pageURL string, qualified bool) []string {
	prompt := browserVisionPrompt(imagePath, pageURL, qualified)
	if spec.Name == "codex" {
		args := []string{"exec", "--image", imagePath}
		if yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		if spec.Level != "" {
			args = append(args, "-c", "model_reasoning_effort="+spec.Level)
		}
		return append(args, prompt)
	}
	args := []string{"-p"}
	if yolo {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode", "auto")
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Level != "" {
		args = append(args, "--effort", spec.Level)
	}
	return append(args, prompt)
}

// browserVisionRefPattern is the only qualified answer shape accepted: an
// owner, a repository, and a number, with nothing around them. It is as strict
// as the bare-number parser, so a board answer that drops the repository (or
// buries it in prose) is discarded rather than guessed at.
var browserVisionRefPattern = regexp.MustCompile(`^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#(\d+)$`)

// parseBrowserVisionRefs reads the JSON array the agent was asked for. Only the
// last non-empty line is considered, and it has to be a plain array on its own:
// a model that thinks out loud before answering is still understood, but an
// array buried inside some other structure is not the answer that was
// requested. qualified selects which element shape is accepted, and the two are
// mutually exclusive: a bare-number answer to a board is an error, because the
// numbers it names cannot be resolved to repositories. Anything else is an
// error too, and the caller discards it rather than re-prompting.
func parseBrowserVisionRefs(output string, qualified bool) ([]browserVisionRef, error) {
	answer := lastNonEmptyLine(output)
	answer = strings.TrimSpace(strings.Trim(answer, "`"))
	if !strings.HasPrefix(answer, "[") || !strings.HasSuffix(answer, "]") {
		return nil, fmt.Errorf("the agent answered with something other than a JSON array")
	}
	if !qualified {
		var numbers []int
		if err := json.Unmarshal([]byte(answer), &numbers); err != nil {
			return nil, fmt.Errorf("the agent's answer is not a JSON array of issue numbers: %w", err)
		}
		refs := make([]browserVisionRef, 0, len(numbers))
		for _, number := range numbers {
			if number <= 0 {
				return nil, fmt.Errorf("the agent reported %d, which is not an issue number", number)
			}
			refs = append(refs, browserVisionRef{Number: number})
		}
		return refs, nil
	}
	var items []browserVisionAnswer
	decoder := json.NewDecoder(strings.NewReader(answer))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf(`the agent's answer is not a JSON array of {"ref","status"} objects: %w`, err)
	}
	refs := make([]browserVisionRef, 0, len(items))
	for _, item := range items {
		match := browserVisionRefPattern.FindStringSubmatch(strings.TrimSpace(item.Ref))
		if match == nil {
			return nil, fmt.Errorf("the agent reported %q, which is not an OWNER/REPO#NUMBER issue reference", item.Ref)
		}
		number, err := strconv.Atoi(match[3])
		if err != nil || number <= 0 {
			return nil, fmt.Errorf("the agent reported %q, which is not an issue number", item.Ref)
		}
		status, err := browserVisionStatus(item.Ref, item.Status)
		if err != nil {
			return nil, err
		}
		refs = append(refs, browserVisionRef{Repository: match[1] + "/" + match[2], Number: number, Status: status})
	}
	return refs, nil
}

// browserVisionAnswer is one element of the qualified answer shape. Decoding
// with DisallowUnknownFields keeps the object as strict as the reference
// pattern is: an answer carrying extra fields is a different shape than the one
// that was asked for, and a different shape is discarded rather than
// interpreted.
type browserVisionAnswer struct {
	Ref    string `json:"ref"`
	Status string `json:"status"`
}

// browserVisionStatusLimit is the longest status a board column can plausibly
// be. A status is a short label a person reads at a glance ("Todo", "In
// Progress", "Ready for review"), so anything longer is prose that arrived in
// the status field rather than a column value.
const browserVisionStatusLimit = 40

// browserVisionStatus validates the Status column read off the board. An empty
// status is accepted as-is: boards really do leave items without one, and
// inventing a status would be exactly the guess this fallback must not make.
// Callers are expected to say loudly that such an item cannot dispatch.
func browserVisionStatus(ref, status string) (string, error) {
	status = strings.TrimSpace(status)
	if len(status) > browserVisionStatusLimit {
		return "", fmt.Errorf("the agent reported a %d-character status for %q, which is not a board column", len(status), ref)
	}
	if strings.ContainsFunc(status, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("the agent reported %q with a status that is not a single line", ref)
	}
	return status, nil
}

// lastNonEmptyLine returns the final line of text an agent printed, which is
// where a one-line answer ends up regardless of what preceded it.
func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
