import { afterEach, describe, expect, it, vi } from "vitest";
import {
	buildSettingsUpdate,
	deliveryLabel,
	fetchSettings,
	jobActionAvailability,
	jobAgentSummary,
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
