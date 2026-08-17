package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dashboardDirEnv overrides where dashboard port records are kept. Tests use it,
// and it gives a user with an unusual cache directory a way to point both
// `glorp watch` and `glorp ui` at the same place.
const dashboardDirEnv = "GLORP_DASHBOARD_DIR"

// dashboardRecord is one running instance's note of the port its web UI bound,
// so `glorp ui` can find dashboards outside the port scan range.
type dashboardRecord struct {
	Port int    `json:"port"`
	PID  int    `json:"pid"`
	path string `json:"-"`
}

// dashboardRecordsDir resolves the directory holding the port records. It falls
// back to the temporary directory when the user cache directory is unavailable,
// since a missing record only costs a port scan.
func dashboardRecordsDir(lookup func(string) string) string {
	if dir := strings.TrimSpace(lookup(dashboardDirEnv)); dir != "" {
		return dir
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "glorp", "dashboards")
	}
	return filepath.Join(cache, "glorp", "dashboards")
}

// dashboardRecordPath is the file one instance writes. Naming it after the port
// means a new instance that binds a port left behind by a crashed one replaces
// the stale record instead of adding a second one.
func dashboardRecordPath(dir string, port int) string {
	return filepath.Join(dir, fmt.Sprintf("port-%d.json", port))
}

// writeDashboardRecord records that this process serves a dashboard on port,
// returning a function that removes the record on shutdown.
func writeDashboardRecord(dir string, port, pid int) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, fmt.Errorf("create dashboard record directory: %w", err)
	}
	path := dashboardRecordPath(dir, port)
	body, err := json.Marshal(dashboardRecord{Port: port, PID: pid})
	if err != nil {
		return func() {}, fmt.Errorf("encode dashboard record: %w", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return func() {}, fmt.Errorf("write dashboard record: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// readDashboardRecords lists the recorded dashboards, ordered by port. Records
// that cannot be read or parsed are skipped: they only ever supplement the port
// scan, so a damaged one must not fail the lookup.
func readDashboardRecords(dir string) []dashboardRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var records []dashboardRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record dashboardRecord
		if err := json.Unmarshal(body, &record); err != nil {
			continue
		}
		if record.Port < 1 || record.Port > 65535 {
			continue
		}
		record.path = path
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Port < records[j].Port })
	return records
}

// removeDashboardRecord deletes a record whose dashboard is gone.
func removeDashboardRecord(record dashboardRecord) {
	if record.path != "" {
		_ = os.Remove(record.path)
	}
}

// dashboardCandidatePorts merges recorded ports with the contiguous scan range,
// de-duplicated and ordered so the picker and the non-interactive "lowest port"
// choice stay predictable.
func dashboardCandidatePorts(records []dashboardRecord, startPort, count int) []int {
	seen := make(map[int]bool)
	var ports []int
	add := func(port int) {
		if port < 1 || port > 65535 || seen[port] {
			return
		}
		seen[port] = true
		ports = append(ports, port)
	}
	for _, record := range records {
		add(record.Port)
	}
	for port := startPort; port < startPort+count; port++ {
		add(port)
	}
	sort.Ints(ports)
	return ports
}
