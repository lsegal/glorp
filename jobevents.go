package main

import (
	"strconv"
	"sync"
	"time"

	"github.com/lsegal/glorp/core"
)

// jobEventLimit bounds how many events each job keeps. Older events are
// dropped; the Log still holds the whole output.
const jobEventLimit = 2000

// jobEventRecorder receives the typed events an output decoder reads out of an
// agent's stream (issue #715). The job output writer implements it, so the
// runner can find it behind the plain io.Writer it is handed.
type jobEventRecorder interface {
	recordJobEvent(core.JobEvent)
}

// jobEventLog keeps every job's typed events in memory, keyed like the jobs
// themselves (issue #715). The zero value is ready to use.
type jobEventLog struct {
	mu   sync.Mutex
	jobs map[string]*jobEventList
}

type jobEventList struct {
	run  int
	next int64
	// runStart is the Seq the current run's first event gets.
	runStart int64
	events   []core.JobEvent
}

func (l *jobEventLog) list(key string) *jobEventList {
	if l.jobs == nil {
		l.jobs = make(map[string]*jobEventList)
	}
	list := l.jobs[key]
	if list == nil {
		list = &jobEventList{run: 1, runStart: 1}
		l.jobs[key] = list
	}
	return list
}

// record numbers event and appends it to the job's events.
func (l *jobEventLog) record(key string, event core.JobEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.list(key)
	list.next++
	event.Seq = list.next
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	list.events = append(list.events, event)
	if over := len(list.events) - jobEventLimit; over > 0 {
		list.events = append(list.events[:0:0], list.events[over:]...)
	}
}

// startRun begins a new run of the job, whose output starts over the way its
// Log does. Sequence numbers keep counting, so a cursor never repeats.
func (l *jobEventLog) startRun(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	list, ok := l.jobs[key]
	if !ok {
		l.list(key)
		return
	}
	list.run++
	list.runStart = list.next + 1
	list.events = nil
}

// remove forgets a job that left the dashboard.
func (l *jobEventLog) remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.jobs, key)
}

// since returns the job's events after seq, or false for a job it has no
// events for.
func (l *jobEventLog) since(key string, seq int64) (core.JobEvents, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	list, ok := l.jobs[key]
	if !ok {
		return core.JobEvents{}, false
	}
	result := core.JobEvents{Run: list.run, Latest: list.next, Events: []core.JobEvent{}}
	expected := seq + 1
	if expected < list.runStart {
		expected = list.runStart
	}
	for index, event := range list.events {
		if event.Seq > seq {
			result.Events = append(result.Events, list.events[index:]...)
			result.Truncated = index == 0 && event.Seq > expected
			break
		}
	}
	return result, true
}

// JobEvents serves the dashboard's events endpoint for the job on target's
// issue number (issue #715).
func (w *Glorp) JobEvents(target string, number int, since int64) (core.JobEvents, bool) {
	return w.jobEvents.since(target+"#"+strconv.Itoa(number), since)
}
