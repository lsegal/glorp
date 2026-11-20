package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

// eventCollector records the job events a decoder sends it.
type eventCollector struct {
	mu     sync.Mutex
	events []core.JobEvent
}

func (c *eventCollector) recordJobEvent(event core.JobEvent) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

func (c *eventCollector) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := make([]string, len(c.events))
	for index, event := range c.events {
		kinds[index] = event.Kind
	}
	return kinds
}

func TestJobEventLogServesOnlyEventsAfterTheCursor(t *testing.T) {
	var log jobEventLog
	if _, ok := log.since("o/r#1", 0); ok {
		t.Fatal("a job with no events was found")
	}
	for _, text := range []string{"one", "two", "three"} {
		log.record("o/r#1", core.JobEvent{Kind: core.JobEventText, Text: text})
	}
	all, ok := log.since("o/r#1", 0)
	if !ok || len(all.Events) != 3 || all.Latest != 3 || all.Run != 1 || all.Truncated {
		t.Fatalf("since 0 = %+v, %v", all, ok)
	}
	newer, _ := log.since("o/r#1", 2)
	if len(newer.Events) != 1 || newer.Events[0].Seq != 3 || newer.Events[0].Text != "three" {
		t.Fatalf("since 2 = %+v", newer)
	}
	if none, _ := log.since("o/r#1", 3); len(none.Events) != 0 || none.Latest != 3 {
		t.Fatalf("since latest = %+v", none)
	}
}

func TestJobEventLogStartsEachRunOverWithoutReusingSequenceNumbers(t *testing.T) {
	var log jobEventLog
	log.startRun("o/r#1")
	log.record("o/r#1", core.JobEvent{Kind: core.JobEventText, Text: "first run"})
	log.startRun("o/r#1")
	log.record("o/r#1", core.JobEvent{Kind: core.JobEventText, Text: "second run"})
	events, _ := log.since("o/r#1", 0)
	if events.Run != 2 || len(events.Events) != 1 || events.Events[0].Seq != 2 || events.Truncated {
		t.Fatalf("second run = %+v", events)
	}
	log.remove("o/r#1")
	if _, ok := log.since("o/r#1", 0); ok {
		t.Fatal("a removed job's events were still served")
	}
}

func TestJobEventLogBoundsRetentionAndReportsTheGap(t *testing.T) {
	var log jobEventLog
	for range jobEventLimit + 5 {
		log.record("o/r#1", core.JobEvent{Kind: core.JobEventOutput, Text: "line"})
	}
	events, _ := log.since("o/r#1", 2)
	if len(events.Events) != jobEventLimit || events.Events[0].Seq != 6 || !events.Truncated {
		t.Fatalf("retained %d events from %d, truncated %v", len(events.Events), events.Events[0].Seq, events.Truncated)
	}
	if recent, _ := log.since("o/r#1", 10); recent.Truncated {
		t.Fatal("a cursor inside the retained events was reported truncated")
	}
}

func TestClaudeJSONOutputWriterRecordsTypedEvents(t *testing.T) {
	var output strings.Builder
	events := &eventCollector{}
	w := newClaudeJSONOutputWriter(&output)
	w.recordEventsTo(events)
	lines := []string{
		`not json at all`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the issue."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"FAIL"}]}]}}`,
		`{"type":"result","is_error":false,"result":"done","total_cost_usd":0.25,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`,
	}
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	want := []string{core.JobEventOutput, core.JobEventText, core.JobEventTool, core.JobEventToolResult, core.JobEventUsage}
	if got := events.kinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	tool, result, usage := events.events[2], events.events[3], events.events[4]
	if tool.Tool != "Bash" || tool.ToolID != "t1" || tool.Summary != "go test ./..." || !strings.Contains(string(tool.Input), "go test") {
		t.Fatalf("tool event = %+v", tool)
	}
	if result.ToolID != "t1" || !result.IsError || result.Text != "FAIL" {
		t.Fatalf("tool result event = %+v", result)
	}
	if *usage.Usage != (core.TokenUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 40, CostUSD: 0.25}) {
		t.Fatalf("usage event = %+v", usage.Usage)
	}
	if got := output.String(); got != "not json at all\nReading the issue.\nRunning: go test ./...\n" {
		t.Fatalf("rendered output changed: %q", got)
	}
}

func TestClaudeJSONOutputWriterRecordsAgentErrors(t *testing.T) {
	events := &eventCollector{}
	w := newClaudeJSONOutputWriter(io.Discard)
	w.recordEventsTo(events)
	if _, err := io.WriteString(w, `{"type":"result","is_error":true,"result":"out of credits"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 1 || events.events[0].Kind != core.JobEventError || events.events[0].Text != "out of credits" {
		t.Fatalf("events = %+v", events.events)
	}
}

func TestJSONLOutputWriterRecordsTypedEvents(t *testing.T) {
	events := &eventCollector{}
	w := newJSONLOutputWriter(io.Discard, museJSONL)
	w.recordEventsTo(events)
	lines := []string{
		`{"event":"message","delta":{"text":"reading the issue"}}`,
		`{"event":"message","delta":{"tool":{"name":"Read","arguments":{"file_path":"main.go"}}}}`,
		`a banner`,
	}
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	want := []string{core.JobEventText, core.JobEventTool, core.JobEventOutput}
	if got := events.kinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	if tool := events.events[1]; tool.Tool != "Read" || tool.Summary != "Read main.go" || !strings.Contains(string(tool.Input), "main.go") {
		t.Fatalf("tool event = %+v", tool)
	}
}

func TestJSONLOutputWriterRecordsWholeDeltaMessages(t *testing.T) {
	events := &eventCollector{}
	config := museJSONL
	config.TextDelta = true
	w := newJSONLOutputWriter(io.Discard, config)
	w.recordEventsTo(events)
	lines := []string{
		`{"event":"message","delta":{"text":"a.txt "}}`,
		`{"event":"message","delta":{"text":"contains hi"}}`,
	}
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 1 || events.events[0].Text != "a.txt contains hi" {
		t.Fatalf("events = %+v", events.events)
	}
}

func TestPassthroughEventWriterRecordsLinesAndForwardsOutput(t *testing.T) {
	var output strings.Builder
	events := &eventCollector{}
	w := newPassthroughEventWriter(&output)
	w.recordEventsTo(events)
	if _, err := io.WriteString(w, "first\n\nsecond"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "first\n\nsecond" {
		t.Fatalf("forwarded output = %q", output.String())
	}
	if len(events.events) != 2 || events.events[0].Text != "first" || events.events[1].Text != "second" {
		t.Fatalf("events = %+v", events.events)
	}
}

func TestCommentRouteJobEventSaysWhereTheCommentWas(t *testing.T) {
	onIssue := commentRoute{Repository: "o/r", Number: 7, Comment: routedComment{Kind: ConversationComment, Repository: "o/r", Number: 7, ID: 3, Author: "alice"}}.jobEvent()
	if onIssue.Kind != core.JobEventComment || onIssue.Comment.PullRequest || onIssue.Comment.Author != "alice" || onIssue.Comment.URL != "https://github.com/o/r/issues/7#issuecomment-3" {
		t.Fatalf("issue comment event = %+v", onIssue.Comment)
	}
	onPull := commentRoute{Repository: "o/r", Number: 7, Comment: routedComment{Kind: ConversationComment, Repository: "o/r", Number: 9, ID: 4}}.jobEvent()
	if !onPull.Comment.PullRequest || onPull.Comment.Number != 9 {
		t.Fatalf("pull request comment event = %+v", onPull.Comment)
	}
}

// fakeEventRunner records a typed event through the output it is handed, the
// way the command runner's decoders do.
type fakeEventRunner struct{ fakeOutputRunner }

func (fakeEventRunner) RunWithOutput(_ context.Context, _ Issue, output io.Writer) error {
	if recorder, ok := output.(jobEventRecorder); ok {
		recorder.recordJobEvent(core.JobEvent{Kind: core.JobEventText, Text: "typed output"})
	}
	_, err := io.WriteString(output, "typed output\n")
	return err
}

func (f fakeEventRunner) RunSessionWithOutput(ctx context.Context, issue Issue, _ AgentSession, _ func(AgentSession), output io.Writer) error {
	return f.RunWithOutput(ctx, issue, output)
}

func TestGlorpServesAJobsTypedEvents(t *testing.T) {
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 1, Title: "bug"}}}},
		Runner: fakeEventRunner{}, UI: &snapshotReporter{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if events, ok := w.JobEvents("o/r", 1, 0); ok && len(events.Events) == 1 && events.Events[0].Text == "typed output" {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("the job's typed events were not served")
}
