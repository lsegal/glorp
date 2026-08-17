package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestDashboardRecordsDirPrefersEnvOverride(t *testing.T) {
	lookup := func(key string) string {
		if key == dashboardDirEnv {
			return " /tmp/custom-dashboards "
		}
		return ""
	}
	if dir := dashboardRecordsDir(lookup); dir != "/tmp/custom-dashboards" {
		t.Fatalf("dir = %q, want the trimmed override", dir)
	}
	fallback := dashboardRecordsDir(func(string) string { return "" })
	if !strings.HasSuffix(filepath.ToSlash(fallback), "glorp/dashboards") {
		t.Fatalf("fallback dir = %q, want it under a glorp/dashboards path", fallback)
	}
}

func TestWriteAndReadDashboardRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	remove, err := writeDashboardRecord(dir, 9000, 4242)
	if err != nil {
		t.Fatalf("writeDashboardRecord: %v", err)
	}
	records := readDashboardRecords(dir)
	if len(records) != 1 || records[0].Port != 9000 || records[0].PID != 4242 {
		t.Fatalf("records = %+v, want one record for port 9000", records)
	}
	remove()
	if records := readDashboardRecords(dir); len(records) != 0 {
		t.Fatalf("records after removal = %+v, want none", records)
	}
}

func TestReadDashboardRecordsSkipsUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "port-bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "port-0.json"), []byte(`{"port":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`{"port":9100}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeDashboardRecord(dir, 9100, 7); err != nil {
		t.Fatal(err)
	}
	records := readDashboardRecords(dir)
	if len(records) != 1 || records[0].Port != 9100 {
		t.Fatalf("records = %+v, want only the valid record", records)
	}
	if records := readDashboardRecords(filepath.Join(dir, "missing")); records != nil {
		t.Fatalf("records for a missing directory = %+v, want nil", records)
	}
}

func TestDashboardCandidatePortsMergesRecordsAndScanRange(t *testing.T) {
	records := []dashboardRecord{{Port: 9000}, {Port: 8766}, {Port: 9000}}
	ports := dashboardCandidatePorts(records, 8765, 3)
	if want := []int{8765, 8766, 8767, 9000}; !reflect.DeepEqual(ports, want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
	if ports := dashboardCandidatePorts([]dashboardRecord{{Port: 65535}}, 65535, 3); !reflect.DeepEqual(ports, []int{65535}) {
		t.Fatalf("ports = %v, want the out-of-range scan tail dropped", ports)
	}
}

// A dashboard recorded well outside the scan range must still be found, which
// is the whole point of the records.
func TestDiscoverDashboardsFindsRecordedPortOutsideScanRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/state" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"snapshot":{"Targets":["owner/repo"],"Running":1},"logs":[]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	if _, err := writeDashboardRecord(dir, 9000, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := writeDashboardRecord(dir, 9500, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	records := readDashboardRecords(dir)
	ports := dashboardCandidatePorts(records, 8765, 4)
	baseURL := func(port int) string {
		if port == 9000 {
			return server.URL
		}
		// An address nothing is listening on stands in for an unused port.
		return "http://127.0.0.1:1"
	}
	found := discoverDashboards(context.Background(), http.DefaultClient, baseURL, ports)
	if len(found) != 1 || found[0].Port != 9000 || found[0].Targets[0] != "owner/repo" {
		t.Fatalf("found = %+v, want the recorded dashboard on port 9000", found)
	}

	// The 9500 record failed the identity probe, so it must not survive.
	pruneDashboardRecords(records, found)
	remaining := readDashboardRecords(dir)
	if len(remaining) != 1 || remaining[0].Port != 9000 {
		t.Fatalf("remaining records = %+v, want only the live one", remaining)
	}
}

// A record for a port that is also inside the scan range must yield one
// candidate, not two.
func TestDiscoverDashboardsDeduplicatesRecordedAndScannedPorts(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"snapshot":{"Targets":["owner/repo"]},"logs":[]}`))
	}))
	defer server.Close()

	port, err := strconv.Atoi(server.URL[strings.LastIndex(server.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	ports := dashboardCandidatePorts([]dashboardRecord{{Port: port}}, port, 1)
	found := discoverDashboards(context.Background(), http.DefaultClient, func(int) string { return server.URL }, ports)
	if len(found) != 1 {
		t.Fatalf("found %d dashboards, want 1 after de-duplication", len(found))
	}
	if hits != 1 {
		t.Fatalf("probed the dashboard %d times, want 1", hits)
	}
}
