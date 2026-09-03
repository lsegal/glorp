import { afterEach, describe, expect, it, vi } from "vitest";
import {
	buildSettingsUpdate,
	deliveryLabel,
	fetchAccess,
	fetchSettings,
	formatInterval,
	jobActionAvailability,
	jobAgentSummary,
	lastPollLabel,
	parseAllowedCommenters,
	submitJobAction,
	submitSettings,
} from "./dashboard";

afterEach(() => vi.unstubAllGlobals());

describe("deliveryLabel", () => {
	it("describes push delivery", () => {
		expect(deliveryLabel({ UseWebhooks: true })).toBe("push");
	});

	it("describes the polling interval", () => {
		expect(deliveryLabel({ Interval: 30_000_000_000 })).toBe(
			"polling every 30s",
		);
	});

	it("reports when GitHub was last checked", () => {
		const checked = new Date(2026, 7, 30, 14, 5, 9);
		expect(
			deliveryLabel({
				Interval: 30_000_000_000,
				LastPoll: checked.toISOString(),
			}),
		).toBe("polling every 30s; checked 14:05:09");
		expect(
			deliveryLabel({ UseWebhooks: true, LastPoll: checked.toISOString() }),
		).toBe("push; checked 14:05:09");
	});

	it("omits the last check before the first poll finishes", () => {
		expect(deliveryLabel({ Interval: 30_000_000_000 })).toBe(
			"polling every 30s",
		);
		expect(
			deliveryLabel({
				Interval: 30_000_000_000,
				LastPoll: "0001-01-01T00:00:00Z",
			}),
		).toBe("polling every 30s");
	});
});

describe("lastPollLabel", () => {
	it("ignores a missing or unparsable timestamp", () => {
		expect(lastPollLabel(undefined)).toBe("");
		expect(lastPollLabel("")).toBe("");
		expect(lastPollLabel("not a time")).toBe("");
	});
});

describe("jobActionAvailability", () => {
	it("enables retry for every dashboard job state", () => {
		expect(jobActionAvailability("active")).toEqual({
			retry: true,
			stop: true,
		});
		expect(jobActionAvailability("failed")).toEqual({
			retry: true,
			stop: false,
		});
		expect(jobActionAvailability("complete")).toEqual({
			retry: true,
			stop: false,
		});
		expect(jobActionAvailability("queued")).toEqual({
			retry: true,
			stop: false,
		});
		expect(jobActionAvailability("stopping")).toEqual({
			retry: true,
			stop: false,
		});
	});
});

describe("jobAgentSummary", () => {
	it("reports pending when no agent has been assigned yet", () => {
		expect(jobAgentSummary({})).toBe("pending");
	});

	it("includes the model and effort when available", () => {
		expect(
			jobAgentSummary({ Agent: "claude", Model: "opus", Effort: "low" }),
		).toBe("claude (opus, low)");
	});

	it("includes only the model when effort is missing", () => {
		expect(jobAgentSummary({ Agent: "claude", Model: "opus" })).toBe(
			"claude (opus)",
		);
	});

	it("includes only the effort when the model is missing", () => {
		expect(jobAgentSummary({ Agent: "codex", Effort: "high" })).toBe(
			"codex (high)",
		);
	});

	it("shows only the agent name when model and effort are missing", () => {
		expect(jobAgentSummary({ Agent: "codex" })).toBe("codex");
	});
});

describe("submitJobAction", () => {
	it("posts the selected job and action", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true });
		vi.stubGlobal("fetch", fetch);
		await submitJobAction({ Target: "lsegal/glorp", Number: 302 }, "stop");
		expect(fetch).toHaveBeenCalledWith("/api/jobs/action", {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				action: "stop",
				target: "lsegal/glorp",
				number: 302,
			}),
		});
	});
});

describe("parseAllowedCommenters", () => {
	it("splits, trims, and drops empty names", () => {
		expect(parseAllowedCommenters("alice, bob ,, carol")).toEqual([
			"alice",
			"bob",
			"carol",
		]);
	});

	it("returns an empty array for blank input", () => {
		expect(parseAllowedCommenters("  ")).toEqual([]);
	});
});

describe("buildSettingsUpdate", () => {
	it("builds a full update including a trimmed agent", () => {
		expect(
			buildSettingsUpdate({
				concurrency: "3",
				readyState: "Agent Ready",
				allowedCommenters: "alice, bob",
				agent: " codex ",
			}),
		).toEqual({
			concurrency: 3,
			readyState: "Agent Ready",
			allowedCommenters: ["alice", "bob"],
			agent: "codex",
		});
	});

	it("omits the agent field when blank", () => {
		expect(
			buildSettingsUpdate({
				concurrency: "1",
				readyState: "",
				allowedCommenters: "",
				agent: "   ",
			}),
		).toEqual({
			concurrency: 1,
			readyState: "",
			allowedCommenters: [],
		});
	});
});

describe("fetchSettings", () => {
	it("returns the parsed settings snapshot", async () => {
		const snapshot = { concurrency: 2, readyState: "Ready" };
		const fetch = vi
			.fn()
			.mockResolvedValue({ ok: true, json: () => Promise.resolve(snapshot) });
		vi.stubGlobal("fetch", fetch);
		await expect(fetchSettings()).resolves.toEqual(snapshot);
		expect(fetch).toHaveBeenCalledWith("/api/settings", { cache: "no-store" });
	});

	it("throws with the response body on failure", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 503,
			text: () => Promise.resolve("settings unavailable"),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(fetchSettings()).rejects.toThrow("settings unavailable");
	});
});

describe("submitSettings", () => {
	it("posts the update and returns the resulting snapshot", async () => {
		const snapshot = { concurrency: 4 };
		const fetch = vi
			.fn()
			.mockResolvedValue({ ok: true, json: () => Promise.resolve(snapshot) });
		vi.stubGlobal("fetch", fetch);
		await expect(submitSettings({ concurrency: 4 })).resolves.toEqual(snapshot);
		expect(fetch).toHaveBeenCalledWith("/api/settings", {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ concurrency: 4 }),
		});
	});
});

// The same table runs against formatInterval in ui.go (TestFormatInterval), so
// the web status bar and the TUI cannot spell an interval differently again
// (issue #449).
describe("formatInterval", () => {
	const second = 1_000_000_000;
	const cases = [
		[5 * second, "5s"],
		[20 * second, "20s"],
		[30 * second, "30s"],
		[60 * second, "1m"],
		[90 * second, "1m30s"],
		[3600 * second, "1h"],
		[5400 * second, "1h30m"],
		[3630 * second, "1h30s"],
		[7509 * second, "2h5m9s"],
		[1_500_000_000, "1.5s"],
		[500_000_000, "500ms"],
		[0, "0s"],
		[-second, "0s"],
	];
	for (const [nanoseconds, want] of cases) {
		it(`renders ${nanoseconds}ns as ${want}`, () => {
			expect(formatInterval(nanoseconds)).toBe(want);
		});
	}
});

describe("deliveryLabel interval spelling", () => {
	it("renders a whole hour the way the TUI does", () => {
		expect(deliveryLabel({ Interval: 3600 * 1_000_000_000 })).toBe(
			"polling every 1h",
		);
	});
});

describe("fetchAccess", () => {
	it("reports a published dashboard as read-only", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: true,
			json: async () => ({ readOnly: true }),
		});
		vi.stubGlobal("fetch", fetch);
		expect(await fetchAccess()).toEqual({ readOnly: true });
		expect(fetch).toHaveBeenCalledWith("/api/access", { cache: "no-store" });
	});

	it("reports the loopback dashboard as fully accessible", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) }),
		);
		expect(await fetchAccess()).toEqual({ readOnly: false });
	});

	it("falls back to full access when the endpoint cannot be read", async () => {
		vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
		expect(await fetchAccess()).toEqual({ readOnly: false });
		vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
		expect(await fetchAccess()).toEqual({ readOnly: false });
	});
});
