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

// throwForResponse builds the error fetchSettings/submitSettings throw on a
// non-ok response, carrying the HTTP status so a caller can tell the run
// merely isn't ready yet (503, worth retrying) from a real failure.
async function throwForResponse(response) {
	const error = new Error((await response.text()) || `HTTP ${response.status}`);
	error.status = response.status;
	throw error;
}

export async function fetchSettings() {
	const response = await fetch("/api/settings", { cache: "no-store" });
	if (!response.ok) {
		await throwForResponse(response);
	}
	return response.json();
}

// settingsRetryDelaysMs backs off how often fetchSettingsWithRetry re-polls
// while the run is still starting (issue #579): the web server accepts
// connections well before Run reaches the point that can service a settings
// request, and in webhook mode ngrok startup and per-target webhook
// configuration stretch that gap to several seconds. The last delay repeats
// for any wait longer than this list covers.
const settingsRetryDelaysMs = [200, 400, 800, 1500, 3000];

// fetchSettingsWithRetry polls fetchSettings, retrying with backoff while the
// server reports 503 (the run hasn't reached its dispatch loop yet) instead
// of surfacing that as a hard error the settings modal would otherwise be
// stuck showing (issue #579). A non-503 failure -- or the signal firing --
// stops the retry and propagates immediately.
export async function fetchSettingsWithRetry(signal, wait = defaultWait) {
	for (let attempt = 0; ; attempt++) {
		if (signal?.aborted) throw new DOMException("aborted", "AbortError");
		try {
			return await fetchSettings();
		} catch (err) {
			if (err.status !== 503) throw err;
			const delay =
				settingsRetryDelaysMs[
					Math.min(attempt, settingsRetryDelaysMs.length - 1)
				];
			await wait(delay, signal);
		}
	}
}

function defaultWait(delayMs, signal) {
	return new Promise((resolve, reject) => {
		const timer = setTimeout(resolve, delayMs);
		signal?.addEventListener(
			"abort",
			() => {
				clearTimeout(timer);
				reject(new DOMException("aborted", "AbortError"));
			},
			{ once: true },
		);
	});
}

export async function fetchAgentStatuses() {
	const response = await fetch("/api/agents", { cache: "no-store" });
	if (!response.ok) {
		await throwForResponse(response);
	}
	return response.json();
}

// fetchAgentStatusesWithRetry waits for the server's background model probe
// when the settings modal opens before it completes (issue #595).
export async function fetchAgentStatusesWithRetry(signal, wait = defaultWait) {
	for (let attempt = 0; ; attempt++) {
		if (signal?.aborted) throw new DOMException("aborted", "AbortError");
		try {
			return await fetchAgentStatuses();
		} catch (err) {
			if (err.status !== 503) throw err;
			const delay =
				settingsRetryDelaysMs[
					Math.min(attempt, settingsRetryDelaysMs.length - 1)
				];
			await wait(delay, signal);
		}
	}
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

// specAgentName extracts the agent name from a bare agent name or a full
// agent/model:level spec, so callers can look up state that's shared across
// every model a given agent offers (issue #582).
function specAgentName(value) {
	return String(value || "")
		.trim()
		.split(":")[0]
		.split("/")[0];
}

// probedModelsByAgent indexes the model lists /api/agents reports, keyed by
// agent and stripped back down to the bare model id. The probe answers with
// the fully qualified `agent/model` names --agent takes, which is also what a
// chip's value must be, but the option record keeps the two halves apart.
export function probedModelsByAgent(statuses) {
	const models = new Map();
	for (const status of statuses || []) {
		if (!status?.name || !status.models?.length) continue;
		const prefix = `${status.name}/`;
		models.set(
			status.name,
			status.models
				.map((model) =>
					String(model).startsWith(prefix)
						? String(model).slice(prefix.length)
						: String(model),
				)
				.filter(Boolean),
		);
	}
	return models;
}

// modelOptionsFrom flattens the settings snapshot's agent list into one
// selectable entry per agent/model pair (issue #582): a fix for #572, which
// put the multiselect on agents even though picking an agent alone can't
// choose a model. Selecting a model drives which agent dispatches, so the
// multiselect belongs here instead.
//
// Most agent definitions declare no model allow-list, so the snapshot alone
// used to render them as bare agent names -- a spec that names no model at
// all, which is exactly what the multiselect exists to choose (issue #589).
// The models each CLI actually reports come from the same /api/agents probe
// the chips already read their auth and quota from, so they fill that gap
// here, and an agent no list can be built for contributes no entry rather
// than a nonsensical one. A spec the run is already dispatching with is kept
// regardless, so an active choice can always be switched back off.
export function modelOptionsFrom(snapshot, statuses, selected) {
	const probed = probedModelsByAgent(statuses);
	const options = [];
	const seen = new Set();
	const add = (agent, model) => {
		const value = model ? `${agent}/${model}` : agent;
		if (!agent || seen.has(value)) return;
		seen.add(value);
		options.push({ value, agent, model });
	};
	for (const option of agentOptionsFrom(snapshot)) {
		if (!option?.name) continue;
		const models = option.models?.length
			? option.models
			: probed.get(option.name) || [];
		for (const model of models) add(option.name, model);
	}
	for (const value of selected || []) {
		const agent = specAgentName(value);
		const spec = String(value || "").trim();
		add(
			agent,
			spec.startsWith(`${agent}/`) ? spec.slice(agent.length + 1) : "",
		);
	}
	return options;
}

// modelGroupsFrom keeps each agent's choices together in the settings modal
// (issue #596), while retaining a group for agents with no reported models.
export function modelGroupsFrom(snapshot, statuses, selected) {
	const groups = new Map();
	const add = (agent) => {
		if (!agent || groups.has(agent)) return;
		groups.set(agent, {
			agent,
			options: [],
			status: agentStatusFor(statuses, agent),
		});
	};
	for (const option of agentOptionsFrom(snapshot)) add(option?.name);
	for (const option of modelOptionsFrom(snapshot, statuses, selected)) {
		add(option.agent);
		groups.get(option.agent).options.push(option);
	}
	// Keep the configured order within each auth state, but put choices that
	// can be used now ahead of agents the probe says are signed out (issue #597).
	return Array.from(groups.values()).sort(
		(a, b) =>
			Number(b.status?.auth === "signed in") -
			Number(a.status?.auth === "signed in"),
	);
}

// agentStatusFor finds the probe result for the agent behind a model option's
// value in the list /api/agents returns, so a model chip (issue #582) can
// show its agent's auth and quota reading.
export function agentStatusFor(statuses, value) {
	const agent = specAgentName(value);
	return (statuses || []).find((status) => status && status.name === agent);
}

// toggleActiveModel adds or removes value from the multiselect's checked set
// (issue #582), used as the models tab's chip click handler.
export function toggleActiveModel(activeAgents, value, checked) {
	const set = new Set(activeAgents || []);
	if (checked) set.add(value);
	else set.delete(value);
	return Array.from(set);
}

// agentSummaries collects one status entry per registered agent, in the order
// the settings snapshot lists them. #583 moved each agent's auth/quota
// reading into a chip's hover-only title, so it's no longer visible without
// hovering every chip; this lets the models tab also render one always-visible
// status line per agent (issue #585). It reads the snapshot rather than the
// model options so an agent that contributes no chip -- one whose models could
// not be probed (issue #589) -- still says why on its own line.
export function agentSummaries(snapshot, statuses) {
	const seen = new Set();
	const summaries = [];
	for (const option of agentOptionsFrom(snapshot)) {
		if (!option?.name || seen.has(option.name)) continue;
		seen.add(option.name);
		summaries.push({
			agent: option.name,
			status: agentStatusFor(statuses, option.name),
		});
	}
	return summaries;
}
