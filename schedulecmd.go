package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lsegal/glorp/core"
)

// scheduleUsage is the help text of `glorp schedule` (issue #848).
const scheduleUsage = `Usage: glorp schedule add|list|rm|run [flags] [ID]

Manage the prompts glorp runs on a timer. A running "glorp watch" picks up
every change to the schedule store at once, the same store the web
dashboard's Schedules modal edits.

  glorp schedule add -name NAME -repo OWNER/REPO -prompt TEXT TRIGGER [flags]
  glorp schedule list
  glorp schedule rm ID
  glorp schedule run ID

TRIGGER is one of -at TIME (once), -every DURATION, -daily HH:MM,
-weekly HH:MM with -days, or -cron EXPRESSION. "run" asks the watching
instance to start a run now, whatever the trigger says; with no watch running,
the run starts when the next watch does.

Pass the same -state (or -schedules) as "glorp watch" so both read one store.

Flags:`

func scheduleFlagSet() *flag.FlagSet {
	flags := flag.NewFlagSet("schedule", flag.ContinueOnError)
	flags.String("state", ".glorp.json", "work state file of the glorp watch; the store is kept beside it")
	flags.String("schedules", "", "schedule store (default: .schedules.json beside -state)")
	flags.String("id", "", "add: schedule ID (default: derived from -name)")
	flags.String("name", "", "add: schedule name")
	flags.String("repo", "", "add: OWNER/REPO the run clones and works in")
	flags.String("prompt", "", "add: prompt the agent runs")
	flags.String("prompt-file", "", "add: file holding the prompt, - for stdin")
	flags.String("at", "", "add: run once at this time, as 2006-01-02 15:04 or RFC 3339")
	flags.String("every", "", "add: run every DURATION, such as 30m or 6h")
	flags.String("daily", "", "add: run daily at HH:MM")
	flags.String("weekly", "", "add: run weekly at HH:MM on -days")
	flags.String("days", "", "add: weekdays of -weekly, such as mon,wed,fri")
	flags.String("cron", "", "add: run on a five-field cron expression")
	flags.String("tz", "", "add: IANA timezone of the trigger (default: local)")
	flags.String("agent", "", "add: agent/model:level to run (default: the watch's agents)")
	flags.String("overlap", core.OverlapSkip, "add: skip or queue a fire while the previous run is active")
	flags.String("missed", core.MissedSkip, "add: skip or runOnce for fires missed while asleep or stopped")
	flags.Bool("disabled", false, "add: create the schedule disabled")
	return flags
}

func runScheduleCommand(args []string) int {
	return scheduleCommand(args, os.Stdin, os.Stdout, os.Stderr, time.Now)
}

func scheduleCommand(args []string, stdin io.Reader, stdout, stderr io.Writer, now func() time.Time) int {
	flags := scheduleFlagSet()
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, scheduleUsage)
		flags.PrintDefaults()
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		flags.Usage()
		return 2
	}
	action := args[0]
	// Flags may come before or after the schedule ID.
	var positional []string
	for rest := args[1:]; ; {
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	path := flagValue[string](flags, "schedules")
	if path == "" {
		path = schedulesPathFor(flagValue[string](flags, "state"))
	}
	schedules, err := loadSchedules(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch action {
	case "list", "ls":
		printSchedules(stdout, schedules, now())
		return 0
	case "add":
		schedule, err := scheduleFromFlags(flags, stdin, now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if slices.ContainsFunc(schedules, func(existing core.Schedule) bool { return existing.ID == schedule.ID }) {
			fmt.Fprintf(stderr, "schedule %q already exists\n", schedule.ID)
			return 1
		}
		schedules = append(schedules, schedule)
		if err := saveSchedules(path, schedules); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "added schedule %s (%s, %s)\n", schedule.ID, schedule.Repository, schedule.Trigger.Describe())
		if next, ok := schedule.Next(now()); ok && schedule.Enabled {
			fmt.Fprintf(stdout, "next run %s\n", next.Local().Format("2006-01-02 15:04 MST"))
		}
		return 0
	case "rm", "remove", "delete", "run":
		if len(positional) != 1 {
			fmt.Fprintf(stderr, "glorp schedule %s takes one schedule ID\n", action)
			return 2
		}
		index := slices.IndexFunc(schedules, func(schedule core.Schedule) bool { return schedule.ID == positional[0] })
		if index < 0 {
			fmt.Fprintf(stderr, "schedule %q not found in %s\n", positional[0], path)
			return 1
		}
		if action == "run" {
			schedules[index].RunNow = true
		} else {
			schedules = slices.Delete(schedules, index, index+1)
		}
		if err := saveSchedules(path, schedules); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if action == "run" {
			fmt.Fprintf(stdout, "requested a run of schedule %s; a running glorp watch starts it at once\n", positional[0])
		} else {
			fmt.Fprintf(stdout, "removed schedule %s\n", positional[0])
		}
		return 0
	}
	fmt.Fprintf(stderr, "unknown schedule command %q: use add, list, rm, or run\n", action)
	return 2
}

// scheduleFromFlags builds the schedule `glorp schedule add` describes.
func scheduleFromFlags(flags *flag.FlagSet, stdin io.Reader, now time.Time) (core.Schedule, error) {
	prompt := flagValue[string](flags, "prompt")
	if file := flagValue[string](flags, "prompt-file"); file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return core.Schedule{}, fmt.Errorf("read prompt: %w", err)
		}
		prompt = string(data)
	}
	trigger := core.ScheduleTrigger{Timezone: flagValue[string](flags, "tz")}
	kinds := 0
	if at := flagValue[string](flags, "at"); at != "" {
		kinds++
		location, err := trigger.Location()
		if err != nil {
			return core.Schedule{}, err
		}
		parsed, err := time.Parse(time.RFC3339, at)
		if err != nil {
			parsed, err = time.ParseInLocation("2006-01-02 15:04", at, location)
		}
		if err != nil {
			return core.Schedule{}, fmt.Errorf("invalid -at %q: use 2006-01-02 15:04 or RFC 3339", at)
		}
		trigger.Kind, trigger.At = core.TriggerOnce, parsed
	}
	if every := flagValue[string](flags, "every"); every != "" {
		kinds++
		trigger.Kind, trigger.Every = core.TriggerInterval, every
	}
	if daily := flagValue[string](flags, "daily"); daily != "" {
		kinds++
		trigger.Kind, trigger.Time = core.TriggerDaily, daily
	}
	if weekly := flagValue[string](flags, "weekly"); weekly != "" {
		kinds++
		trigger.Kind, trigger.Time = core.TriggerWeekly, weekly
		for _, day := range strings.Split(flagValue[string](flags, "days"), ",") {
			day = strings.ToLower(strings.TrimSpace(day))
			if day == "" {
				continue
			}
			index := slices.IndexFunc(cronWeekdayNames, func(name string) bool { return strings.HasPrefix(day, name) })
			if index < 0 {
				return core.Schedule{}, fmt.Errorf("unknown weekday %q", day)
			}
			trigger.Weekdays = append(trigger.Weekdays, index)
		}
	}
	if cron := flagValue[string](flags, "cron"); cron != "" {
		kinds++
		trigger.Kind, trigger.Cron = core.TriggerCron, cron
	}
	if kinds != 1 {
		return core.Schedule{}, fmt.Errorf("give exactly one trigger: -at, -every, -daily, -weekly, or -cron")
	}
	schedule := core.Schedule{
		ID:         flagValue[string](flags, "id"),
		Name:       flagValue[string](flags, "name"),
		Repository: flagValue[string](flags, "repo"),
		Prompt:     prompt,
		Trigger:    trigger,
		Agent:      flagValue[string](flags, "agent"),
		Enabled:    !flagValue[bool](flags, "disabled"),
		Overlap:    flagValue[string](flags, "overlap"),
		Missed:     flagValue[string](flags, "missed"),
		Created:    now,
	}
	if strings.TrimSpace(schedule.ID) == "" {
		schedule.ID = newScheduleID(schedule.Name)
	}
	schedule.Normalize()
	if schedule.Agent != "" {
		if _, err := parseAgentSpec(schedule.Agent); err != nil {
			return core.Schedule{}, err
		}
	}
	return schedule, schedule.Validate()
}

var cronWeekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func printSchedules(out io.Writer, schedules []core.Schedule, now time.Time) {
	if len(schedules) == 0 {
		fmt.Fprintln(out, "no schedules")
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tNAME\tREPOSITORY\tTRIGGER\tENABLED\tNEXT\tLAST")
	for _, schedule := range schedules {
		next := "-"
		if at, ok := schedule.Next(now); ok && schedule.Enabled {
			next = at.Local().Format("2006-01-02 15:04")
		}
		last := "-"
		if run, ok := schedule.LastRun(); ok {
			last = run.Status + " " + run.Fired.Local().Format("2006-01-02 15:04")
		}
		enabled := "yes"
		if !schedule.Enabled {
			enabled = "no"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", schedule.ID, schedule.Name, schedule.Repository, schedule.Trigger.Describe(), enabled, next, last)
	}
	table.Flush()
}
