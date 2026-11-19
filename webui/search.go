package webui

import (
	"strconv"
	"strings"

	"github.com/lsegal/glorp/core"
)

// searchJobs returns the jobs the dashboard's search box matches: those whose
// fields, including their whole agent Log, contain search, ignoring case
// (issue #851). Searching the server's copy is what lets the dashboard find a
// job by its log while only one page of logs is published. An empty search
// matches every job.
func searchJobs(jobs []core.JobSnapshot, search string) []core.JobSnapshot {
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return jobs
	}
	matches := []core.JobSnapshot{}
	for _, job := range jobs {
		if strings.Contains(strings.ToLower(jobSearchText(job)), search) {
			matches = append(matches, job)
		}
	}
	return matches
}

// jobSearchText is every field of a job the search box looks in, one per
// line, with the issue and pull request numbers also written the way the job
// card shows them ("#851", "PR #853", "o/r#851").
func jobSearchText(job core.JobSnapshot) string {
	fields := []string{
		job.Target, job.Repository, job.Title, job.Status, job.CheckoutDirectory,
		job.SessionID, job.Agent, job.Model, job.Effort, job.WaitReason,
		job.PullRequestRepository, job.Kind, job.ScheduleID, job.Log,
	}
	if job.Number > 0 {
		number := strconv.Itoa(job.Number)
		repository := job.Repository
		if repository == "" {
			repository = job.Target
		}
		fields = append(fields, "#"+number, repository+"#"+number)
	}
	if job.PullRequest > 0 {
		number := strconv.Itoa(job.PullRequest)
		fields = append(fields, "PR "+job.PullRequestRepository+"#"+number)
	}
	return strings.Join(fields, "\n")
}
