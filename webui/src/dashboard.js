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

// formatInterval renders a poll interval held as Go nanoseconds. It mirrors
// formatInterval in ui.go so the web status bar and the TUI spell the same
// interval the same way (issue #449): zero components are dropped, so an hour
// reads `1h` on both sides rather than `3600s` here and `1h0m0s` there, and a
// short interval stays short (`20s`, not `0h0m20s`).
export function formatInterval(nanoseconds) {
	const milliseconds = Math.round(Number(nanoseconds) / 1_000_000);
	if (!Number.isFinite(milliseconds) || milliseconds <= 0) return "0s";
	if (milliseconds < 1000) return `${milliseconds}ms`;
	let rest = milliseconds;
	let text = "";
	const hours = Math.floor(rest / 3_600_000);
	if (hours > 0) {
		text += `${hours}h`;
		rest -= hours * 3_600_000;
	}
	const minutes = Math.floor(rest / 60_000);
	if (minutes > 0) {
		text += `${minutes}m`;
		rest -= minutes * 60_000;
	}
	if (rest > 0) text += `${Number((rest / 1000).toFixed(3))}s`;
	return text;
}

// deliveryLabel describes how work is picked up, and when GitHub was last
// checked. A poll that finds nothing new logs nothing (issue #413), so the
// last-checked time is the only standing sign the run is still polling
// (issue #447). Push mode shows it too, since it still reconciles periodically.
export function deliveryLabel(snapshot) {
	const interval = snapshot.Interval ? formatInterval(snapshot.Interval) : "—";
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
// update the /api/settings endpoint expects. activeAgents is omitted when
// empty (issue #572) so an instance with no configured runner doesn't fail
// validation just for reporting its other settings back unchanged.
export function buildSettingsUpdate(form) {
	const update = {
		concurrency: Number(form.concurrency),
		readyState: form.readyState,
		allowedCommenters: parseAllowedCommenters(form.allowedCommenters),
	};
	const activeAgents = (form.activeAgents || []).filter(Boolean);
	if (activeAgents.length) update.activeAgents = activeAgents;
	return update;
}

export async function fetchSettings() {
	const response = await fetch("/api/settings", { cache: "no-store" });
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
	return response.json();
}

export async function fetchAgentStatuses() {
	const response = await fetch("/api/agents", { cache: "no-store" });
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

// agentOptionsFrom normalises the settings snapshot's agent list. The
// registry-backed agentOptions carry each agent's model and level allow-lists
// (issue #489); the plain agents array is the same set without them, and is
// what an older glorp reports.
export function agentOptionsFrom(snapshot) {
	if (snapshot?.agentOptions?.length) return snapshot.agentOptions;
	return (snapshot?.agents || []).map((name) => ({ name }));
}

// agentOptionHint describes the models and levels a given agent name accepts,
// so the agents tab's multiselect (issue #572) shows what --agent-style spec
// each checkbox stands for.
export function agentOptionHint(agentOptions, value) {
	const name = String(value || "")
		.trim()
		.split(":")[0]
		.split("/")[0];
	if (!name) return "";
	const option = (agentOptions || []).find(
		(entry) => entry && entry.name === name,
	);
	if (!option) return "";
	const parts = [];
	if (option.models?.length) parts.push(`models: ${option.models.join(", ")}`);
	if (option.levels?.length) parts.push(`levels: ${option.levels.join(", ")}`);
	return parts.join(" · ");
}

// agentStatusFor finds the probe result for one agent name in the list
// /api/agents returns, so the agents tab (issue #572) can pair each
// checkbox with its own auth and quota reading.
export function agentStatusFor(statuses, name) {
	return (statuses || []).find((status) => status && status.name === name);
}

// toggleActiveAgent adds or removes name from the multiselect's checked set
// (issue #572), used as the agents tab's checkbox change handler.
export function toggleActiveAgent(activeAgents, name, checked) {
	const set = new Set(activeAgents || []);
	if (checked) set.add(name);
	else set.delete(name);
	return Array.from(set);
}
