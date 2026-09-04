import { afterEach, describe, expect, it, vi } from "vitest";
import {
	agentOptionsFrom,
	agentStatusFor,
	buildSettingsUpdate,
	deliveryLabel,
	fetchAgentStatuses,
	fetchSettings,
	formatInterval,
	jobActionAvailability,
	jobAgentSummary,
	lastPollLabel,
	modelOptionsFrom,
	parseAllowedCommenters,
	submitJobAction,
	submitSettings,
	toggleActiveModel,
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
	it("builds a full update including the checked active agents", () => {
		expect(
			buildSettingsUpdate({
				concurrency: "3",
				readyState: "Agent Ready",
				allowedCommenters: "alice, bob",
				activeAgents: ["codex", "muse"],
			}),
		).toEqual({
			concurrency: 3,
			readyState: "Agent Ready",
			allowedCommenters: ["alice", "bob"],
			activeAgents: ["codex", "muse"],
		});
	});

	it("omits activeAgents when nothing is checked", () => {
		expect(
			buildSettingsUpdate({
				concurrency: "1",
				readyState: "",
				allowedCommenters: "",
				activeAgents: [],
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

describe("agent options", () => {
	const options = [
		{ name: "codex", levels: ["low", "high"] },
		{ name: "muse", models: ["muse-1", "muse-2"] },
		{ name: "plain" },
	];

	it("prefers the registry's agent options over the bare name list", () => {
		expect(
			agentOptionsFrom({ agentOptions: options, agents: ["codex"] }),
		).toEqual(options);
	});

	it("falls back to the bare agent names", () => {
		expect(agentOptionsFrom({ agents: ["codex", "muse"] })).toEqual([
			{ name: "codex" },
			{ name: "muse" },
		]);
		expect(agentOptionsFrom({})).toEqual([]);
	});
});

describe("modelOptionsFrom", () => {
	const options = [
		{ name: "codex", levels: ["low", "high"] },
		{ name: "muse", models: ["muse-1", "muse-2"] },
		{ name: "plain" },
	];

	it("expands each agent's models into their own qualified entry", () => {
		expect(modelOptionsFrom({ agentOptions: options })).toEqual([
			{ value: "codex", agent: "codex", model: "" },
			{ value: "muse/muse-1", agent: "muse", model: "muse-1" },
			{ value: "muse/muse-2", agent: "muse", model: "muse-2" },
			{ value: "plain", agent: "plain", model: "" },
		]);
	});

	it("falls back to the bare agent names with no allow-list", () => {
		expect(modelOptionsFrom({ agents: ["codex"] })).toEqual([
			{ value: "codex", agent: "codex", model: "" },
		]);
		expect(modelOptionsFrom({})).toEqual([]);
	});
});

describe("fetchAgentStatuses", () => {
	it("returns the parsed agent status list", async () => {
		const statuses = [{ name: "codex", auth: "signed in", quota: "80% left" }];
		const fetch = vi
			.fn()
			.mockResolvedValue({ ok: true, json: () => Promise.resolve(statuses) });
		vi.stubGlobal("fetch", fetch);
		await expect(fetchAgentStatuses()).resolves.toEqual(statuses);
		expect(fetch).toHaveBeenCalledWith("/api/agents", { cache: "no-store" });
	});

	it("throws with the response body on failure", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 503,
			text: () => Promise.resolve("agents unavailable"),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(fetchAgentStatuses()).rejects.toThrow("agents unavailable");
	});
});

describe("agentStatusFor", () => {
	const statuses = [
		{ name: "codex", auth: "signed in" },
		{ name: "muse", auth: "unknown" },
	];

	it("finds the status matching a bare agent name", () => {
		expect(agentStatusFor(statuses, "muse")).toEqual({
			name: "muse",
			auth: "unknown",
		});
	});

	it("finds the status matching a full agent/model spec", () => {
		expect(agentStatusFor(statuses, "muse/muse-1")).toEqual({
			name: "muse",
			auth: "unknown",
		});
	});

	it("returns undefined for an agent with no probe result yet", () => {
		expect(agentStatusFor(statuses, "claude")).toBeUndefined();
		expect(agentStatusFor(undefined, "codex")).toBeUndefined();
	});
});

describe("toggleActiveModel", () => {
	it("adds a value when checked", () => {
		expect(toggleActiveModel(["codex"], "muse/muse-1", true)).toEqual([
			"codex",
			"muse/muse-1",
		]);
	});

	it("removes a value when unchecked", () => {
		expect(toggleActiveModel(["codex", "muse/muse-1"], "codex", false)).toEqual(
			["muse/muse-1"],
		);
	});

	it("does not duplicate a value already checked", () => {
		expect(toggleActiveModel(["codex"], "codex", true)).toEqual(["codex"]);
	});
});
