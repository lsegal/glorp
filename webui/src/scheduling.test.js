import { afterEach, describe, expect, it, vi } from "vitest";
import {
	describeTrigger,
	emptyScheduleForm,
	formatRelative,
	formToSchedule,
	formTrigger,
	lastRunLabel,
	previewTrigger,
	saveSchedule,
	scheduledJobActions,
	scheduleToForm,
	toggleWeekday,
} from "./scheduling";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(body, status = 200) {
	const fetch = vi.fn(async () => ({
		ok: status < 400,
		status,
		json: async () => body,
		text: async () => (typeof body === "string" ? body : ""),
	}));
	vi.stubGlobal("fetch", fetch);
	return fetch;
}

describe("schedule editor form (issue #848)", () => {
	it("round-trips a schedule through the form", () => {
		const schedule = {
			id: "sweep",
			name: "Weekly sweep",
			repository: "o/r",
			prompt: "Sweep.",
			trigger: {
				kind: "weekly",
				time: "08:30",
				weekdays: [5, 1],
				timezone: "UTC",
			},
			agent: "claude/opus",
			enabled: false,
			overlap: "queue",
			missed: "runOnce",
		};
		const body = formToSchedule(scheduleToForm(schedule));
		expect(body).toEqual({
			...schedule,
			trigger: { ...schedule.trigger, weekdays: [1, 5] },
		});
	});

	it("sends only the fields the trigger kind uses", () => {
		const form = {
			...emptyScheduleForm("o/r"),
			timezone: "",
			kind: "interval",
		};
		expect(formTrigger(form)).toEqual({
			kind: "interval",
			every: "1h",
			timezone: "",
		});
		expect(formTrigger({ ...form, kind: "cron" })).toEqual({
			kind: "cron",
			cron: "0 9 * * 1-5",
			timezone: "",
		});
	});

	it("toggles weekdays", () => {
		expect(toggleWeekday([1, 3], 3)).toEqual([1]);
		expect(toggleWeekday([1], 5)).toEqual([1, 5]);
	});
});

describe("schedule requests (issue #848)", () => {
	it("creates a new schedule and patches an existing one", async () => {
		const fetch = stubFetch({ schedule: { id: "a" } });
		await saveSchedule("", { name: "a" });
		expect(fetch).toHaveBeenLastCalledWith(
			"/api/schedules",
			expect.objectContaining({ method: "POST" }),
		);
		await saveSchedule("a b", { name: "a", history: [] });
		const [url, options] = fetch.mock.calls[1];
		expect(url).toBe("/api/schedules/a%20b");
		expect(options.method).toBe("PATCH");
		expect(JSON.parse(options.body)).not.toHaveProperty("history");
	});

	it("reports the daemon's error text", async () => {
		stubFetch("invalid interval", 400);
		await expect(previewTrigger({ kind: "interval" })).rejects.toThrow(
			"invalid interval",
		);
	});
});

describe("schedule labels (issue #848)", () => {
	const now = Date.parse("2026-11-18T12:00:00Z");
	it("says how far off a run is", () => {
		expect(formatRelative("2026-11-18T12:20:00Z", now)).toBe("in 20m");
		expect(formatRelative("2026-11-18T15:20:00Z", now)).toBe("in 3h 20m");
		expect(formatRelative("2026-11-20T13:00:00Z", now)).toBe("in 2d 1h");
		expect(formatRelative("2026-11-18T11:00:00Z", now)).toBe("now");
		expect(formatRelative("0001-01-01T00:00:00Z", now)).toBe("");
	});

	it("describes triggers and last results", () => {
		expect(describeTrigger({ kind: "daily", time: "09:00" })).toBe(
			"Daily 09:00",
		);
		expect(
			describeTrigger({ kind: "weekly", time: "09:00", weekdays: [1, 3] }),
		).toBe("Mon, Wed 09:00");
		expect(lastRunLabel({ history: [] })).toBe("never run");
		expect(lastRunLabel({ history: [{ status: "failed" }] })).toBe(
			"last failed",
		);
		expect(lastRunLabel({ running: true })).toBe("running");
	});

	it("never offers pause on a scheduled run", () => {
		expect(scheduledJobActions("active")).toEqual({
			retry: true,
			stop: true,
			pause: false,
		});
		expect(scheduledJobActions("complete").stop).toBe(false);
		expect(scheduledJobActions("stopping").retry).toBe(false);
	});
});
