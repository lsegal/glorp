package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The screenshot fallback exists for one situation only: GitHub changed its
// markup and the DOM extractor in browserissues.go no longer recognises a page
// that plainly does contain items. It is a bridge until extraction is fixed in
// code, not a strategy, so every part of it is bounded. In the steady state a
// watch run makes zero vision calls no matter how long it polls.
const (
	// browserVisionCooldown is the minimum time between screenshots of the
	// same target. A five-second poll loop hitting a permanently broken page
	// would otherwise queue an agent call every tick.
	browserVisionCooldown = 10 * time.Minute
	// browserVisionRunLimit is how many vision calls a single run may make
	// before the fallback switches itself off for the rest of the run. If
	// three screenshots did not recover the list, a fourth will not either;
	// the extractor needs fixing.
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

// browserVision is the budget around the screenshot-to-agent fallback. It owns
// the accounting (per-target cooldown, per-run cap) so the issue source cannot
// spend the budget by accident, and it logs every call it does make with the
// reason and the running count so a runaway is obvious in the dashboard.
type browserVision struct {
	cooldown time.Duration
	limit    int
	// ask performs one vision call and returns the issue numbers the agent
	// read off the screenshot. Tests replace it; production wires it to an
	// agent invocation.
	ask  func(ctx context.Context, screenshot []byte, pageURL string) ([]int, error)
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

// Recover spends one unit of budget, if any is left, to read the issue numbers
// off a screenshot of a page the DOM extractor could not read. It returns nil
// whenever the budget forbids the call, the screenshot fails, or the agent's
// answer is not the strict JSON list it was asked for; nothing is ever retried.
//
// A nil receiver means the fallback is off, which is the default.
func (v *browserVision) Recover(ctx context.Context, target, pageURL, reason string, screenshot func() ([]byte, error)) []int {
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
	numbers, err := v.ask(callCtx, image, pageURL)
	if err != nil {
		v.log("browser vision: discarded the agent's answer for %s: %v", pageURL, err)
		return nil
	}
	if len(numbers) == 0 {
		v.log("browser vision: the agent found no issue numbers on %s", pageURL)
		return nil
	}
	v.log("browser vision: recovered %d issue number(s) from %s; fix the page extractor rather than relying on this", len(numbers), pageURL)
	return numbers
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

// browserVisionAgent turns one screenshot into a list of issue numbers by
// invoking the run's configured agent on it.
type browserVisionAgent struct {
	Runner CommandRunner
}

// browserVisionPrompt asks for a bare JSON array and nothing else. Anything
// else the agent says is discarded rather than re-prompted, so the instruction
// is deliberately narrow.
func browserVisionPrompt(imagePath, pageURL string) string {
	return fmt.Sprintf(`The image at %s is a screenshot of the GitHub issue list at %s.

Reply with a strict JSON array of the issue numbers listed on that page, and absolutely nothing else: no prose, no explanation, no markdown code fence. For example: [412,398,377]

If the page shows no issues, reply with []. Do not open a browser, run commands, or read anything other than the image.`, imagePath, pageURL)
}

// Ask writes the screenshot where the agent can read it, runs the agent once,
// and parses its answer. The agent's spec (model and reasoning level) is the
// run's configured one, read without advancing the round-robin cursor that
// load balances real issue dispatch.
func (a browserVisionAgent) Ask(ctx context.Context, screenshot []byte, pageURL string) ([]int, error) {
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
	args := browserVisionArgs(spec, a.Runner.Yolo, path, pageURL)
	cmd := newAgentCommand(ctx, a.Runner.binary(spec.Name), args...)
	cmd.Dir = os.TempDir()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := runChildProcess(cmd); err != nil {
		return nil, fmt.Errorf("agent failed: %w", err)
	}
	return parseBrowserVisionNumbers(output.String())
}

// browserVisionArgs builds the one-shot agent invocation. Codex takes the image
// as a flag; Claude reads the path named in the prompt. Neither is asked for
// structured streaming output, because the answer is expected to be one line.
func browserVisionArgs(spec agentSpec, yolo bool, imagePath, pageURL string) []string {
	prompt := browserVisionPrompt(imagePath, pageURL)
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

// parseBrowserVisionNumbers reads the JSON array the agent was asked for. The
// last bracketed span is used so a model that narrates before answering is
// still understood, but anything that is not a plain array of positive integers
// is an error: a half-understood answer is worse than no answer, and it is
// discarded rather than retried.
func parseBrowserVisionNumbers(output string) ([]int, error) {
	start := strings.LastIndex(output, "[")
	end := strings.LastIndex(output, "]")
	if start < 0 || end < start {
		return nil, fmt.Errorf("no JSON array in the agent's answer")
	}
	var numbers []int
	if err := json.Unmarshal([]byte(output[start:end+1]), &numbers); err != nil {
		return nil, fmt.Errorf("the agent's answer is not a JSON array of issue numbers: %w", err)
	}
	for _, number := range numbers {
		if number <= 0 {
			return nil, fmt.Errorf("the agent reported %d, which is not an issue number", number)
		}
	}
	return numbers, nil
}
