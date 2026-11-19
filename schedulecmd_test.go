package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

func TestScheduleCommandAddListRunRemove(t *testing.T) {
	store := filepath.Join(t.TempDir(), "schedules.json")
	now := func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := scheduleCommand(append(args, "-schedules", store), strings.NewReader("from stdin"), &stdout, &stderr, now)
		return code, stdout.String(), stderr.String()
	}
	if code, _, stderr := run("add", "-id", "sweep", "-name", "Weekly sweep", "-repo", "o/r", "-prompt-file", "-", "-weekly", "09:00", "-days", "mon,thu", "-tz", "UTC", "-overlap", "queue", "-missed", "runOnce"); code != 0 {
		t.Fatalf("add exited %d: %s", code, stderr)
	}
	schedules, err := loadSchedules(store)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("stored %+v, %v", schedules, err)
	}
	got := schedules[0]
	if got.Prompt != "from stdin" || got.Trigger.Kind != core.TriggerWeekly || len(got.Trigger.Weekdays) != 2 || got.Overlap != core.OverlapQueue || got.Missed != core.MissedRunOnce || !got.Enabled {
		t.Fatalf("stored schedule %+v", got)
	}
	if code, stdout, _ := run("list"); code != 0 || !strings.Contains(stdout, "sweep") || !strings.Contains(stdout, "2026-11-19") {
		t.Fatalf("list exited %d:\n%s", code, stdout)
	}
	if code, _, _ := run("run", "sweep"); code != 0 {
		t.Fatal("run failed")
	}
	if schedules, _ := loadSchedules(store); !schedules[0].RunNow {
		t.Fatal("run did not ask the watch to run the schedule")
	}
	if code, _, _ := run("rm", "sweep"); code != 0 {
		t.Fatal("rm failed")
	}
	if schedules, _ := loadSchedules(store); len(schedules) != 0 {
		t.Fatalf("rm left %+v", schedules)
	}
}

func TestScheduleCommandRejectsBadTriggers(t *testing.T) {
	store := filepath.Join(t.TempDir(), "schedules.json")
	for _, args := range [][]string{
		{"add", "-name", "x", "-repo", "o/r", "-prompt", "p"},
		{"add", "-name", "x", "-repo", "o/r", "-prompt", "p", "-daily", "09:00", "-every", "1h"},
		{"add", "-name", "x", "-repo", "o/r", "-prompt", "p", "-weekly", "09:00", "-days", "funday"},
		{"rm", "missing"},
	} {
		var stdout, stderr bytes.Buffer
		if code := scheduleCommand(append(args, "-schedules", store), nil, &stdout, &stderr, time.Now); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
}
