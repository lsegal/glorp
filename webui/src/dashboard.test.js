import { afterEach, describe, expect, it, vi } from "vitest";
import {
	agentOptionsFrom,
	agentStatusFor,
	agentSummaries,
	buildSettingsUpdate,
	deliveryLabel,
	fetchAgentStatuses,
	fetchAgentStatusesWithRetry,
	fetchSettings,
	fetchSettingsWithRetry,
	formatInterval,
	jobActionAvailability,
	jobAgentSummary,
	lastPollLabel,
	modelGroupsFrom,
	modelOptionsFrom,
	parseAllowedCommenters,
	probedModelsByAgent,
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

describe("fetchSettingsWithRetry", () => {
	it("retries on a 503 until the run reports ready", async () => {
		const snapshot = { concurrency: 2, readyState: "Ready" };
		const fetch = vi
			.fn()
			.mockResolvedValueOnce({
				ok: false,
				status: 503,
				text: () => Promise.resolve("settings unavailable"),
			})
			.mockResolvedValueOnce({
				ok: false,
				status: 503,
				text: () => Promise.resolve("settings unavailable"),
			})
			.mockResolvedValueOnce({
				ok: true,
				json: () => Promise.resolve(snapshot),
			});
		vi.stubGlobal("fetch", fetch);
		const wait = vi.fn().mockResolvedValue(undefined);
		await expect(fetchSettingsWithRetry(undefined, wait)).resolves.toEqual(
			snapshot,
		);
		expect(fetch).toHaveBeenCalledTimes(3);
		expect(wait).toHaveBeenCalledTimes(2);
	});

	it("propagates a non-503 failure immediately without retrying", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 400,
			text: () => Promise.resolve("invalid settings update"),
		});
		vi.stubGlobal("fetch", fetch);
		const wait = vi.fn().mockResolvedValue(undefined);
		await expect(fetchSettingsWithRetry(undefined, wait)).rejects.toThrow(
			"invalid settings update",
		);
		expect(fetch).toHaveBeenCalledTimes(1);
		expect(wait).not.toHaveBeenCalled();
	});

	it("stops retrying once the signal aborts", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: false,
			status: 503,
			text: () => Promise.resolve("settings unavailable"),
		});
		vi.stubGlobal("fetch", fetch);
		const controller = new AbortController();
		const wait = vi.fn().mockImplementation(() => {
			controller.abort();
			return Promise.resolve(undefined);
		});
		await expect(
			fetchSettingsWithRetry(controller.signal, wait),
		).rejects.toThrow();
		expect(fetch).toHaveBeenCalledTimes(1);
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

describe("probedModelsByAgent", () => {
	it("indexes each agent's probed models with the agent prefix stripped", () => {
		expect(
			probedModelsByAgent([
				{ name: "codex", models: ["codex/gpt-5.6", "codex/gpt-5.6-mini"] },
				{ name: "muse", models: ["muse-1"] },
				{ name: "claude", models: [] },
			]),
		).toEqual(
			new Map([
				["codex", ["gpt-5.6", "gpt-5.6-mini"]],
				["muse", ["muse-1"]],
			]),
		);
	});

	it("tolerates a missing or empty status list", () => {
		expect(probedModelsByAgent()).toEqual(new Map());
		expect(probedModelsByAgent([null, {}])).toEqual(new Map());
	});
});

describe("modelOptionsFrom", () => {
	const options = [
		{ name: "codex", levels: ["low", "high"] },
		{ name: "muse", models: ["muse-1", "muse-2"] },
		{ name: "plain" },
	];

	it("expands each agent's declared models into their own qualified entry", () => {
		expect(modelOptionsFrom({ agentOptions: options })).toEqual([
			{ value: "muse/muse-1", agent: "muse", model: "muse-1" },
			{ value: "muse/muse-2", agent: "muse", model: "muse-2" },
		]);
	});

	it("fills an agent with no allow-list from its probed models", () => {
		expect(
			modelOptionsFrom({ agentOptions: options }, [
				{ name: "codex", models: ["codex/gpt-5.6"] },
				{ name: "plain", models: ["plain/p-1"] },
			]),
		).toEqual([
			{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" },
			{ value: "muse/muse-1", agent: "muse", model: "muse-1" },
			{ value: "muse/muse-2", agent: "muse", model: "muse-2" },
			{ value: "plain/p-1", agent: "plain", model: "p-1" },
		]);
	});

	it("prefers the declared allow-list over what the probe reported", () => {
		expect(
			modelOptionsFrom(
				{ agentOptions: [{ name: "muse", models: ["muse-1"] }] },
				[{ name: "muse", models: ["muse/other"] }],
			),
		).toEqual([{ value: "muse/muse-1", agent: "muse", model: "muse-1" }]);
	});

	it("never offers a bare agent name with no model behind it", () => {
		expect(modelOptionsFrom({ agents: ["codex"] })).toEqual([]);
		expect(modelOptionsFrom({})).toEqual([]);
		expect(modelOptionsFrom(null, [{ name: "codex", models: [] }])).toEqual([]);
	});

	it("keeps an already active spec selectable even with no models for it", () => {
		expect(modelOptionsFrom({ agents: ["codex"] }, [], ["codex"])).toEqual([
			{ value: "codex", agent: "codex", model: "" },
		]);
		expect(
			modelOptionsFrom({ agents: ["codex"] }, [], ["codex/gpt-5.6"]),
		).toEqual([{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" }]);
	});

	it("does not duplicate an active spec the probe already offers", () => {
		expect(
			modelOptionsFrom(
				{ agents: ["codex"] },
				[{ name: "codex", models: ["codex/gpt-5.6"] }],
				["codex/gpt-5.6"],
			),
		).toEqual([{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" }]);
	});
});

describe("modelGroupsFrom", () => {
	it("puts signed-in agents before signed-out agents without reordering peers", () => {
		const groups = modelGroupsFrom({ agents: ["claude", "codex", "gemini"] }, [
			{ name: "claude", auth: "signed out" },
			{ name: "codex", auth: "signed in" },
			{ name: "gemini", auth: "signed in" },
		]);
		expect(groups.map((group) => group.agent)).toEqual([
			"codex",
			"gemini",
			"claude",
		]);
	});
	it("groups model choices by agent and retains an empty agent group", () => {
		expect(
			modelGroupsFrom({ agents: ["codex", "claude"] }, [
				{ name: "codex", models: ["codex/gpt-5.6"] },
			]),
		).toEqual([
			{
				agent: "codex",
				options: [{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" }],
				status: { name: "codex", models: ["codex/gpt-5.6"] },
			},
			{ agent: "claude", options: [], status: undefined },
		]);
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

describe("fetchAgentStatusesWithRetry", () => {
	it("retries while the server warms its agent report", async () => {
		const statuses = [{ name: "codex" }];
		const fetch = vi
			.fn()
			.mockResolvedValueOnce({
				ok: false,
				status: 503,
				text: () => Promise.resolve("agents are still being probed"),
			})
			.mockResolvedValueOnce({
				ok: true,
				json: () => Promise.resolve(statuses),
			});
		const wait = vi.fn().mockResolvedValue();
		vi.stubGlobal("fetch", fetch);
		await expect(fetchAgentStatusesWithRetry(undefined, wait)).resolves.toEqual(
			statuses,
		);
		expect(wait).toHaveBeenCalledOnce();
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

describe("agentSummaries", () => {
	const statuses = [
		{ name: "codex", auth: "signed in", quota: "80%", installed: true },
		{ name: "muse", auth: "unknown", quota: "n/a", installed: false },
	];

	it("collects one summary per registered agent, in snapshot order", () => {
		expect(agentSummaries({ agents: ["codex", "muse"] }, statuses)).toEqual([
			{ agent: "codex", status: statuses[0] },
			{ agent: "muse", status: statuses[1] },
		]);
	});

	it("keeps an agent that contributes no model chip", () => {
		expect(
			agentSummaries({ agentOptions: [{ name: "claude" }] }, statuses),
		).toEqual([{ agent: "claude", status: undefined }]);
	});

	it("returns an empty list for no agents", () => {
		expect(agentSummaries({}, statuses)).toEqual([]);
		expect(agentSummaries(undefined, statuses)).toEqual([]);
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
