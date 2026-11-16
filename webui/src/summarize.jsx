import { NotebookText } from "lucide-react";
import { jobIssueKey } from "./dashboard";

// summarizeRequest is the message the Summarize button sends the prompt agent
// for a job: a request to summarize the current session log of work on the
// job's issue, named by its OWNER/REPO#NUMBER job key (issue #840).
export function summarizeRequest(job) {
	return `Summarize the current session log of work on ${jobIssueKey(job)}`;
}

// summarizeJob opens the prompt agent, or keeps it open, and hands it the
// job's summary request to send at once (issue #840).
export function summarizeJob(job, { open, send }) {
	open();
	send(summarizeRequest(job));
}

// SummarizeButton sits with a job card's log controls and asks the prompt
// agent for a summary of that job's session log (issue #840). It works for
// running and finished jobs alike.
export function SummarizeButton({ job, onSummarize }) {
	const key = jobIssueKey(job);
	return (
		<button
			type="button"
			className="summarize-button"
			disabled={!key}
			onClick={() => onSummarize(job)}
			aria-label="Summarize"
			title={
				key
					? `Ask the prompt agent to summarize the session log of ${key}`
					: "This job's issue is unknown"
			}
		>
			<NotebookText /> <span className="viewport-action-label">Summarize</span>
		</button>
	);
}
