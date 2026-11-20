// The job card's structured transcript reads a job's typed agent events from
// the events endpoint, a cursor at a time, instead of re-reading its whole Log
// (issue #715).

// jobEventsURL is the events endpoint for a job's events after since. A
// project target's job is keyed by its own target too, so the target is used
// as it is, falling back to the issue's repository only when it is empty, the
// way glorp builds job keys.
export function jobEventsURL(job, since = 0) {
	const target = job.Target || job.Repository;
	if (!target || !(job.Number > 0)) return "";
	const path = target.split("/").map(encodeURIComponent).join("/");
	return `/api/jobs/${path}/${job.Number}/events?since=${since}`;
}

// mergeJobEvents adds an events response to what a card already holds. A new
// run, or a gap the server's retention bound left, starts the transcript over
// from the response alone.
export function mergeJobEvents(current, response) {
	const events = response.events || [];
	if (!current || current.run !== response.run || response.truncated) {
		return { run: response.run, latest: response.latest, events };
	}
	const seen = current.events.length
		? current.events[current.events.length - 1].seq
		: 0;
	const fresh = events.filter((event) => event.seq > seen);
	if (fresh.length === 0 && current.latest === response.latest) return current;
	return {
		run: response.run,
		latest: response.latest,
		events: current.events.concat(fresh),
	};
}

function lineCount(text) {
	if (typeof text !== "string" || text === "") return 0;
	return text.replace(/\n$/, "").split("\n").length;
}

// editCounts is the lines a file-editing tool call adds and removes, or null
// for a call that edits no file. Claude's and opencode's spellings of the
// same fields are both read.
export function editCounts(input) {
	if (!input || typeof input !== "object") return null;
	const edits = Array.isArray(input.edits) ? input.edits : [input];
	let added = 0;
	let removed = 0;
	let found = false;
	for (const edit of edits) {
		const before = edit.old_string ?? edit.oldString;
		const after = edit.new_string ?? edit.newString;
		if (typeof before === "string" || typeof after === "string") {
			found = true;
			added += lineCount(after);
			removed += lineCount(before);
		}
	}
	if (!found && typeof input.content === "string") {
		found = true;
		added = lineCount(input.content);
	}
	return found ? { added, removed } : null;
}

// toolOutcome is "running" until a tool call's result arrives, then "ok" or
// "failed".
function toolOutcome(result) {
	if (!result) return "running";
	return result.isError ? "failed" : "ok";
}

// transcriptEntries turns a job's events into what the transcript draws: each
// tool call is one entry carrying its result's outcome, and results are not
// drawn on their own.
export function transcriptEntries(events) {
	const results = new Map();
	for (const event of events) {
		if (event.kind === "tool_result" && event.toolID) {
			results.set(event.toolID, event);
		}
	}
	const entries = [];
	for (const event of events) {
		switch (event.kind) {
			case "tool": {
				const result = event.toolID ? results.get(event.toolID) : undefined;
				entries.push({
					key: event.seq,
					kind: "tool",
					tool: event.tool,
					summary: event.summary || event.tool,
					outcome: event.toolID ? toolOutcome(result) : "",
					detail: result?.isError ? result.text || "" : "",
					edit: editCounts(event.input),
				});
				break;
			}
			case "tool_result":
				break;
			default:
				entries.push({ key: event.seq, ...event });
		}
	}
	return entries;
}

function formatCount(value) {
	if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
	if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
	return String(value || 0);
}

// usageLabel describes a usage event's tokens and cost.
export function usageLabel(usage) {
	const parts = [
		`${formatCount(usage.inputTokens)} in`,
		`${formatCount(usage.outputTokens)} out`,
	];
	if (usage.cacheReadTokens)
		parts.push(`${formatCount(usage.cacheReadTokens)} cached`);
	if (usage.cacheCreationTokens)
		parts.push(`${formatCount(usage.cacheCreationTokens)} cache write`);
	let label = `tokens: ${parts.join(" · ")}`;
	if (usage.costUSD) label += ` · $${usage.costUSD.toFixed(2)}`;
	return label;
}

// commentLabel says who commented where, for a comment sent to the job.
export function commentLabel(comment) {
	const where = comment.pullRequest ? "pull request" : "issue";
	const who = comment.author || "someone";
	return `${who} commented on ${where} #${comment.number}`;
}

// fetchJobEvents reads a job's events after since, or null for a job the
// server has no events for.
export async function fetchJobEvents(job, since, fetcher = fetch) {
	const url = jobEventsURL(job, since);
	if (!url) return null;
	const response = await fetcher(url, { cache: "no-store" });
	if (response.status === 404) return null;
	if (!response.ok) throw new Error(`HTTP ${response.status}`);
	return response.json();
}
