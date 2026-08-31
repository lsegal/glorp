// lastPollLabel renders the time the run last finished a poll of GitHub. A run
// that has not completed one yet reports nothing rather than Go's zero clock,
// which serialises as year 1.
export function lastPollLabel(lastPoll) {
	if (!lastPoll) return "";
	const checked = new Date(lastPoll);
	if (Number.isNaN(checked.getTime()) || checked.getFullYear() <= 1) return "";
	const pad = (value) => String(value).padStart(2, "0");
	return `${pad(checked.getHours())}:${pad(checked.getMinutes())}:${pad(checked.getSeconds())}`;
}

// deliveryLabel describes how work is picked up, and when GitHub was last
// checked. A poll that finds nothing new logs nothing (issue #413), so the
// last-checked time is the only standing sign the run is still polling
// (issue #447). Push mode shows it too, since it still reconciles periodically.
export function deliveryLabel(snapshot) {
	const interval = snapshot.Interval
		? `${snapshot.Interval / 1_000_000_000}s`
		: "—";
	let label = snapshot.UseWebhooks ? "push" : `polling every ${interval}`;
	const checked = lastPollLabel(snapshot.LastPoll);
	if (checked) label += `; checked ${checked}`;
	return label;
}

export function jobAgentSummary(job) {
	if (!job.Agent) return "pending";
	let summary = job.Agent;
	if (job.Model) {
		summary += ` (${job.Model}${job.Effort ? `, ${job.Effort}` : ""})`;
	} else if (job.Effort) {
		summary += ` (${job.Effort})`;
	}
	return summary;
}

export function jobActionAvailability(status) {
	return {
		// A queued job is already on its way to a fresh gh-fix run, so retry is
		// available for every state shown in the dashboard.
		retry: true,
		stop: status === "active",
	};
}

export async function submitJobAction(job, action) {
	const response = await fetch("/api/jobs/action", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ action, target: job.Target, number: job.Number }),
	});
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
}

export function parseAllowedCommenters(text) {
	return text
		.split(",")
		.map((name) => name.trim())
		.filter(Boolean);
}

// buildSettingsUpdate turns the settings modal's form state into the partial
// update the /api/settings endpoint expects. agent is omitted when blank so
// an instance with no configured runner doesn't fail validation just for
// reporting its other settings back unchanged.
export function buildSettingsUpdate(form) {
	const update = {
		concurrency: Number(form.concurrency),
		readyState: form.readyState,
		allowedCommenters: parseAllowedCommenters(form.allowedCommenters),
	};
	const agent = form.agent.trim();
	if (agent) update.agent = agent;
	return update;
}

export async function fetchSettings() {
	const response = await fetch("/api/settings", { cache: "no-store" });
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
	return response.json();
}

export async function submitSettings(update) {
	const response = await fetch("/api/settings", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(update),
	});
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
	return response.json();
}
