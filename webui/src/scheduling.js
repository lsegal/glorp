// Helpers behind the Schedules modal and the "Up next" strip (issue #848).
// They talk to /api/schedules and turn its schedules into the editor's form
// and back, so the components stay free of request and formatting details.

export const weekdayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export const triggerKinds = [
	{ value: "once", label: "Once" },
	{ value: "interval", label: "Every" },
	{ value: "daily", label: "Daily" },
	{ value: "weekly", label: "Weekly" },
	{ value: "cron", label: "Cron" },
];

async function scheduleRequest(path, method = "GET", body) {
	const response = await fetch(path, {
		method,
		headers: body ? { "Content-Type": "application/json" } : undefined,
		body: body ? JSON.stringify(body) : undefined,
	});
	if (!response.ok) {
		throw new Error((await response.text()) || `HTTP ${response.status}`);
	}
	if (response.status === 204) return null;
	return response.json();
}

export async function fetchSchedules() {
	const result = await scheduleRequest("/api/schedules");
	return result.schedules || [];
}

// saveSchedule creates the form's schedule when it has no id yet, and
// otherwise replaces every editable field of the existing one.
export async function saveSchedule(id, schedule) {
	if (!id) {
		const result = await scheduleRequest("/api/schedules", "POST", schedule);
		return result.schedule;
	}
	const { name, repository, prompt, trigger, agent, enabled, overlap, missed } =
		schedule;
	const result = await scheduleRequest(
		`/api/schedules/${encodeURIComponent(id)}`,
		"PATCH",
		{ name, repository, prompt, trigger, agent, enabled, overlap, missed },
	);
	return result.schedule;
}

export async function setScheduleEnabled(id, enabled) {
	const result = await scheduleRequest(
		`/api/schedules/${encodeURIComponent(id)}`,
		"PATCH",
		{ enabled },
	);
	return result.schedule;
}

export async function deleteSchedule(id) {
	await scheduleRequest(`/api/schedules/${encodeURIComponent(id)}`, "DELETE");
}

export async function runScheduleNow(id) {
	const result = await scheduleRequest(
		`/api/schedules/${encodeURIComponent(id)}/run`,
		"POST",
	);
	return result.schedule;
}

// previewTrigger asks the daemon for the trigger's next three fire times, so
// the preview uses exactly the scheduler's own rules.
export async function previewTrigger(trigger) {
	const result = await scheduleRequest(
		"/api/schedules/preview",
		"POST",
		trigger,
	);
	return result.fires || [];
}

export function localTimezone() {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
	} catch {
		return "";
	}
}

// emptyScheduleForm is the editor's state for a new schedule.
export function emptyScheduleForm(repository = "") {
	return {
		id: "",
		name: "",
		repository,
		prompt: "",
		kind: "daily",
		at: "",
		every: "1h",
		time: "09:00",
		weekdays: [1],
		cron: "0 9 * * 1-5",
		timezone: localTimezone(),
		agent: "",
		enabled: true,
		overlap: "skip",
		missed: "skip",
	};
}

// datetimeLocal formats a time for an <input type="datetime-local">.
function datetimeLocal(value) {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return "";
	const pad = (n) => String(n).padStart(2, "0");
	return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

// scheduleToForm turns a schedule from the API into the editor's state.
export function scheduleToForm(schedule) {
	const trigger = schedule.trigger || {};
	const form = emptyScheduleForm(schedule.repository);
	return {
		...form,
		id: schedule.id,
		name: schedule.name || "",
		prompt: schedule.prompt || "",
		kind: trigger.kind || "daily",
		at: trigger.at ? datetimeLocal(trigger.at) : "",
		every: trigger.every || form.every,
		time: trigger.time || form.time,
		weekdays: trigger.weekdays?.length ? trigger.weekdays : form.weekdays,
		cron: trigger.cron || form.cron,
		timezone: trigger.timezone || "",
		agent: schedule.agent || "",
		enabled: !!schedule.enabled,
		overlap: schedule.overlap || "skip",
		missed: schedule.missed || "skip",
	};
}

// formTrigger is the trigger the editor's state describes, with only the
// fields its kind uses.
export function formTrigger(form) {
	const trigger = { kind: form.kind, timezone: form.timezone.trim() };
	if (form.kind === "once") {
		const at = new Date(form.at);
		trigger.at = Number.isNaN(at.getTime()) ? "" : at.toISOString();
	}
	if (form.kind === "interval") trigger.every = form.every.trim();
	if (form.kind === "daily" || form.kind === "weekly") trigger.time = form.time;
	if (form.kind === "weekly") {
		trigger.weekdays = [...form.weekdays].sort((a, b) => a - b);
	}
	if (form.kind === "cron") trigger.cron = form.cron.trim();
	return trigger;
}

// formToSchedule is the request body the editor's state saves.
export function formToSchedule(form) {
	return {
		...(form.id && { id: form.id }),
		name: form.name.trim(),
		repository: form.repository.trim(),
		prompt: form.prompt,
		trigger: formTrigger(form),
		agent: form.agent.trim(),
		enabled: form.enabled,
		overlap: form.overlap,
		missed: form.missed,
	};
}

export function toggleWeekday(weekdays, day) {
	return weekdays.includes(day)
		? weekdays.filter((value) => value !== day)
		: [...weekdays, day];
}

// describeTrigger summarizes a trigger in a few words, such as "Daily 09:00".
export function describeTrigger(trigger = {}) {
	const zone = trigger.timezone ? ` ${trigger.timezone}` : "";
	switch (trigger.kind) {
		case "once":
			return `Once ${formatFireTime(trigger.at)}`;
		case "interval":
			return `Every ${trigger.every}`;
		case "daily":
			return `Daily ${trigger.time}${zone}`;
		case "weekly":
			return `${(trigger.weekdays || []).map((day) => weekdayNames[day]).join(", ")} ${trigger.time}${zone}`;
		case "cron":
			return `Cron ${trigger.cron}${zone}`;
		default:
			return trigger.kind || "";
	}
}

// formatFireTime shows a fire time in the browser's zone, such as
// "Wed 18 Nov, 09:00".
export function formatFireTime(value) {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return "";
	return date.toLocaleString(undefined, {
		weekday: "short",
		day: "numeric",
		month: "short",
		hour: "2-digit",
		minute: "2-digit",
	});
}

// formatRelative says how far off a fire time is, such as "in 3h 20m".
export function formatRelative(value, now = Date.now()) {
	const minutes = Math.round((new Date(value).getTime() - now) / 60000);
	if (Number.isNaN(minutes)) return "";
	if (minutes <= 0) return "now";
	if (minutes < 60) return `in ${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24)
		return `in ${hours}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`;
	const days = Math.floor(hours / 24);
	return `in ${days}d${hours % 24 ? ` ${hours % 24}h` : ""}`;
}

// lastRunLabel is a schedule's most recent result for its list row.
export function lastRunLabel(schedule) {
	if (schedule.running) return "running";
	const history = schedule.history || [];
	const last = history[history.length - 1];
	if (!last) return "never run";
	return last.status === "ok" ? "last ok" : `last ${last.status}`;
}

export function formatDuration(seconds) {
	if (!seconds) return "";
	if (seconds < 60) return `${Math.round(seconds)}s`;
	const minutes = Math.round(seconds / 60);
	if (minutes < 60) return `${minutes}m`;
	return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function isScheduledJob(job) {
	return job?.Kind === "schedule";
}

// scheduledJobActions are the job actions a scheduled run accepts: it can be
// stopped while queued or running and retried once it has stopped, but it
// has no session to hold, so it cannot be paused.
export function scheduledJobActions(status) {
	const running =
		status === "active" || status === "queued" || status === "waiting";
	return {
		retry: status !== "stopping",
		...(status === "stopping" && {
			retryReason: "Waiting for the stopped run to exit",
		}),
		stop: running,
		pause: false,
	};
}
