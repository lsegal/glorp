import {
	jobIssueURL,
	jobPullRequestLabel,
	jobPullRequestURL,
} from "./dashboard";

// JobLink links a job's issue or pull request number to its GitHub page in a
// new tab, or shows it as plain text when there is no page to link to. A
// click stays on the link, so it never reaches the card's other controls
// (issue #767).
function JobLink({ href, children }) {
	if (!href) return children;
	return (
		<a
			className="job-link"
			href={href}
			target="_blank"
			rel="noopener noreferrer"
			onClick={(event) => event.stopPropagation()}
		>
			{children}
		</a>
	);
}

// JobTitle is a job card's heading: its issue number, its pull request when it
// has one (issue #753), and its title, with both numbers linked (issue #767).
export function JobTitle({ job }) {
	const pullRequest = jobPullRequestLabel(job);
	return (
		<h2>
			<JobLink href={jobIssueURL(job)}>#{job.Number}</JobLink>
			{pullRequest && (
				<>
					{" · "}
					<JobLink href={jobPullRequestURL(job)}>{pullRequest}</JobLink>
				</>
			)}{" "}
			{job.Title}
		</h2>
	);
}
