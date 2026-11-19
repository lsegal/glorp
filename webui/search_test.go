package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/lsegal/glorp/core"
)

func searchState(t *testing.T, ui *Server, query url.Values) State {
	t.Helper()
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state?"+query.Encode(), nil))
	var state State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func jobNumbers(jobs []core.JobSnapshot) []int {
	numbers := []int{}
	for _, job := range jobs {
		numbers = append(numbers, job.Number)
	}
	return numbers
}

func TestStateSearchMatchesEveryJobField(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]core.JobSnapshot, 14)
	for i := range jobs {
		jobs[i] = core.JobSnapshot{Number: 100 + i, Target: "o/r", Title: "Job " + strconv.Itoa(i), Status: "running", Log: "output " + strconv.Itoa(i)}
	}
	jobs[12].PullRequest = 351
	jobs[13].Number = 351
	jobs[13].SessionID = "019a2b-session"
	jobs[11].Log = "panic: needle in the transcript"
	jobs[10].Title = "Dashboard Search Bar"
	jobs[9].CheckoutDirectory = "/tmp/glorp-gh-fix-77"
	jobs[8].Agent = "codex"
	ui.Snapshot(core.Snapshot{Jobs: jobs, VisibleJobs: 6})

	cases := []struct {
		search string
		want   []int
	}{
		{"351", []int{112, 351}},
		{"PR #351", []int{112}},
		{"o/r#351", []int{351}},
		{"019A2B", []int{351}},
		{"needle", []int{111}},
		{"search bar", []int{110}},
		{"gh-fix-77", []int{109}},
		{"codex", []int{108}},
		{"zzzzz", []int{}},
	}
	for _, tc := range cases {
		state := searchState(t, ui, url.Values{"search": {tc.search}})
		got := jobNumbers(state.Snapshot.Jobs)
		if len(got) != len(tc.want) {
			t.Fatalf("search %q matched %v, want %v", tc.search, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("search %q matched %v, want %v", tc.search, got, tc.want)
			}
		}
		if state.Search != tc.search || state.TotalJobs != len(jobs) {
			t.Fatalf("search %q reported search %q of %d jobs, want %d", tc.search, state.Search, state.TotalJobs, len(jobs))
		}
		for _, job := range state.Snapshot.Jobs {
			if job.Log == "" {
				t.Fatalf("search %q dropped the Log of matching job #%d on the first page", tc.search, job.Number)
			}
		}
	}
}

func TestStateSearchPagesTheMatches(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]core.JobSnapshot, 20)
	for i := range jobs {
		jobs[i] = core.JobSnapshot{Number: i + 1, Target: "o/r", Log: "output"}
		if i%2 == 0 {
			jobs[i].Log = "output match"
		}
	}
	ui.Snapshot(core.Snapshot{Jobs: jobs, VisibleJobs: 6})

	state := searchState(t, ui, url.Values{"search": {"MATCH"}, "page": {"1"}})
	if len(state.Snapshot.Jobs) != 10 {
		t.Fatalf("search matched %d jobs, want 10", len(state.Snapshot.Jobs))
	}
	for i, job := range state.Snapshot.Jobs {
		if onPage := i >= 6; onPage != (job.Log != "") {
			t.Fatalf("job #%d published Log %q; want Logs only on the second page of matches", job.Number, job.Log)
		}
	}

	state = searchState(t, ui, url.Values{"search": {"  "}})
	if len(state.Snapshot.Jobs) != len(jobs) || state.Search != "" || state.TotalJobs != 0 {
		t.Fatalf("blank search published %d jobs, search %q, total %d; want the unfiltered list", len(state.Snapshot.Jobs), state.Search, state.TotalJobs)
	}
}
