import { afterEach, describe, expect, it, vi } from "vitest";
import {
	agentOptionsFrom,
	agentStatusFor,
	agentSummaries,
	buildSettingsUpdate,
	checkpointLines,
	deliveryLabel,
	fetchAgentStatuses,
	fetchAgentStatusesWithRetry,
	fetchRestartProgress,
	fetchSettings,
	fetchSettingsWithRetry,
	formatInterval,
	jobActionAvailability,
	jobAgentSummary,
	jobPage,
	jobProgress,
	jobPullRequestLabel,
	jobWaitLabel,
	lastPollLabel,
	modelGroupsFrom,
	modelOptionsFrom,
	parseAllowedCommenters,
	probedModelsByAgent,
	stateURL,
	submitJobAction,
	submitRefresh,
	submitRestart,
	submitSettings,
	toggleActiveModel,
} from "./dashboard";

afterEach(() => vi.unstubAllGlobals());

describe("deliveryLabel", () => {
	it("describes push delivery", () => {
		expect(deliveryLabel({ UseWebhooks: true, WebhookOnline: true })).toBe(
			"push",
		);
	});

	it("shows push delivery as offline while the tunnel is down", () => {
		expect(deliveryLabel({ UseWebhooks: true, WebhookOnline: false })).toBe(
			"push (offline)",
		);
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
			deliveryLabel({
				UseWebhooks: true,
				WebhookOnline: true,
				LastPoll: checked.toISOString(),
			}),
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
	it("enables retry for every state the run can retry", () => {
		expect(jobActionAvailability("active")).toEqual({
			retry: true,
			stop: true,
			pause: true,
		});
		expect(jobActionAvailability("failed")).toEqual({
			retry: true,
			stop: false,
			pause: false,
		});
		expect(jobActionAvailability("complete")).toEqual({
			retry: true,
			stop: false,
			pause: false,
		});
		expect(jobActionAvailability("paused")).toEqual({
			retry: true,
			stop: true,
			pause: false,
		});
	});

	it("disables retry with a reason for states the run refuses", () => {
		expect(jobActionAvailability("queued")).toEqual({
			retry: false,
			retryReason: "Already queued for a gh-fix run",
			stop: false,
			pause: true,
		});
		expect(jobActionAvailability("stopping")).toEqual({
			retry: false,
			retryReason: "Waiting for the stopped run to exit",
			stop: false,
			pause: false,
		});
	});

	it("lets a job waiting on a scheduled wakeup be stopped or paused", () => {
		expect(jobActionAvailability("waiting")).toEqual({
			retry: true,
			stop: true,
			pause: true,
		});
	});

	it("offers pause only for active, waiting, and queued jobs", () => {
		const pausable = [
			"active",
			"waiting",
			"queued",
			"stopping",
			"paused",
			"failed",
			"complete",
		].filter((status) => jobActionAvailability(status).pause);
		expect(pausable).toEqual(["active", "waiting", "queued"]);
	});
});

describe("jobPullRequestLabel", () => {
	it("names the job's pull request next to its issue number", () => {
		expect(jobPullRequestLabel({ Number: 747, PullRequest: 750 })).toBe(
			"PR #750",
		);
	});

	it("qualifies a pull request in another repository", () => {
		expect(
			jobPullRequestLabel({
				Number: 747,
				PullRequest: 12,
				PullRequestRepository: "fork/glorp",
			}),
		).toBe("PR fork/glorp#12");
	});

	it("is empty for a job without a pull request", () => {
		expect(jobPullRequestLabel({ Number: 743 })).toBe("");
		expect(jobPullRequestLabel({ Number: 743, PullRequest: 0 })).toBe("");
	});
});

describe("jobProgress", () => {
	it("draws the percentage glorp computed from the plan checklist", () => {
		expect(
			jobProgress({
				Number: 7,
				PullRequest: 187,
				TasksDone: 2,
				TasksTotal: 5,
				Progress: 40,
			}),
		).toEqual({
			percent: 40,
			label: "40%",
			title: "2 of 5 planned tasks done",
		});
	});

	it("shows an empty bar for a checklist with nothing checked", () => {
		expect(jobProgress({ TasksTotal: 3, Progress: 0 })).toEqual({
			percent: 0,
			label: "0%",
			title: "0 of 3 planned tasks done",
		});
	});

	it("follows the plan when it grows", () => {
		expect(
			jobProgress({ TasksDone: 2, TasksTotal: 8, Progress: 25 }).label,
		).toBe("25%");
	});

	it("shows no bar without a pull request or a checklist", () => {
		expect(jobProgress({ Number: 743 })).toBeNull();
		expect(
			jobProgress({ Number: 743, PullRequest: 750, TasksTotal: 0 }),
		).toBeNull();
	});
});

describe("jobWaitLabel", () => {
	it("names the reason and the local time the job resumes", () => {
		const wakeAt = new Date(2026, 8, 28, 15, 4, 5);
		expect(
			jobWaitLabel({
				WaitReason: "CI is running",
				WakeAt: wakeAt.toISOString(),
			}),
		).toBe("waiting: CI is running, resumes at 15:04:05");
	});

	it("leaves out what the snapshot does not carry", () => {
		expect(jobWaitLabel({ WakeAt: "0001-01-01T00:00:00Z" })).toBe("waiting");
	});

	it("says what paused work waits on", () => {
		expect(
			jobWaitLabel({
				Status: "paused",
				WaitReason: "its pull request waits to be merged or closed",
				WakeAt: "0001-01-01T00:00:00Z",
			}),
		).toBe("paused: its pull request waits to be merged or closed");
	});
});

describe("jobPage", () => {
	const jobs = Array.from({ length: 14 }, (_, i) => ({ Number: i + 1 }));
	const numbers = (page) => page.jobs.map((job) => job.Number);

	it("pages through every published job VisibleJobs at a time", () => {
		const snapshot = { Jobs: jobs, VisibleJobs: 6 };
		expect(numbers(jobPage(snapshot, 0))).toEqual([1, 2, 3, 4, 5, 6]);
		expect(numbers(jobPage(snapshot, 1))).toEqual([7, 8, 9, 10, 11, 12]);
		expect(jobPage(snapshot, 2)).toEqual({
			jobs: [{ Number: 13 }, { Number: 14 }],
			page: 2,
			pages: 3,
		});
	});

	it("clamps a page that no longer exists to the last one", () => {
		const page = jobPage({ Jobs: jobs.slice(0, 8), VisibleJobs: 6 }, 2);
		expect(page.page).toBe(1);
		expect(page.pages).toBe(2);
		expect(numbers(page)).toEqual([7, 8]);
		expect(jobPage({ Jobs: jobs, VisibleJobs: 6 }, -1).page).toBe(0);
	});

	it("draws every job on one page when there are fewer than VisibleJobs", () => {
		expect(jobPage({ Jobs: jobs.slice(0, 3), VisibleJobs: 6 }, 0)).toEqual({
			jobs: jobs.slice(0, 3),
			page: 0,
			pages: 1,
		});
	});

	it("draws every job on one page when no limit is published", () => {
		expect(jobPage({ Jobs: jobs }, 0)).toEqual({ jobs, page: 0, pages: 1 });
		expect(jobPage({}, 3)).toEqual({ jobs: [], page: 0, pages: 0 });
	});
});

describe("stateURL", () => {
	it("asks for the page of jobs being shown", () => {
		expect(stateURL(0)).toBe("/api/state");
		expect(stateURL(2)).toBe("/api/state?page=2");
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

describe("fetchRestartProgress", () => {
	it("reads each run's checkpoint while the restart waits on it", async () => {
		const checkpoints = [
			{ Target: "o/r", Number: 7, Outcome: "pushed" },
			{ Target: "o/r", Number: 8, Outcome: "pending" },
		];
		const fetch = vi.fn().mockResolvedValue({
			ok: true,
			json: () => Promise.resolve({ Checkpoints: checkpoints }),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(fetchRestartProgress()).resolves.toEqual(checkpoints);
		expect(fetch).toHaveBeenCalledWith("/api/restart");
	});

	it("is empty when the daemon reports no runs", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: true,
			json: () => Promise.resolve({}),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(fetchRestartProgress()).resolves.toEqual([]);
	});
});

describe("submitRestart", () => {
	it("posts to /api/restart", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: true,
			json: () => Promise.resolve({ Checkpoints: [] }),
		});
		vi.stubGlobal("fetch", fetch);
		await submitRestart();
		expect(fetch).toHaveBeenCalledWith("/api/restart", { method: "POST" });
	});

	it("returns each run's shutdown checkpoint outcome", async () => {
		const checkpoints = [
			{ Target: "o/r", Number: 7, Branch: "fix/issue-7-x", Outcome: "pushed" },
		];
		const fetch = vi.fn().mockResolvedValue({
			ok: true,
			json: () => Promise.resolve({ Checkpoints: checkpoints }),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(submitRestart()).resolves.toEqual(checkpoints);
	});

	it("throws with the response body when the request fails", async () => {
		const fetch = vi.fn().mockResolvedValue({
			ok: false,
			text: () => Promise.resolve("glorp is already restarting"),
		});
		vi.stubGlobal("fetch", fetch);
		await expect(submitRestart()).rejects.toThrow(
			"glorp is already restarting",
		);
	});
});

describe("submitRefresh", () => {
	it("posts to /api/refresh", async () => {
		const fetch = vi.fn().mockResolvedValue({ ok: true });
		vi.stubGlobal("fetch", fetch);
		await submitRefresh();
		expect(fetch).toHaveBeenCalledWith("/api/refresh", { method: "POST" });
	});

	it("throws with the response body when the request fails", async () => {
		const fetch = vi
			.fn()
			.mockResolvedValue({ ok: false, text: () => Promise.resolve("busy") });
		vi.stubGlobal("fetch", fetch);
		await expect(submitRefresh()).rejects.toThrow("busy");
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
				noMerge: true,
				noChangelog: true,
				activeAgents: ["codex", "muse"],
			}),
		).toEqual({
			concurrency: 3,
			readyState: "Agent Ready",
			allowedCommenters: ["alice", "bob"],
			noMerge: true,
			noChangelog: true,
			activeAgents: ["codex", "muse"],
		});
	});

	it("omits activeAgents when nothing is checked", () => {
		expect(
			buildSettingsUpdate({
				concurrency: "1",
				readyState: "",
				allowedCommenters: "",
				noMerge: false,
				noChangelog: false,
				activeAgents: [],
			}),
		).toEqual({
			concurrency: 1,
			readyState: "",
			allowedCommenters: [],
			noMerge: false,
			noChangelog: false,
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

	it("keeps an active spec selectable while its agent has not reported models", () => {
		expect(modelOptionsFrom({ agents: ["codex"] }, [], ["codex"])).toEqual([
			{ value: "codex", agent: "codex", model: "", isDefault: true },
		]);
		expect(
			modelOptionsFrom({ agents: ["codex"] }, [], ["codex/gpt-5.6"]),
		).toEqual([{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" }]);
	});

	it("keeps a bare active default beside the concrete models it auto-picks from", () => {
		expect(
			modelOptionsFrom(
				{ agents: ["codex"] },
				[{ name: "codex", models: ["codex/gpt-5.6"] }],
				["codex"],
			),
		).toEqual([
			{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" },
			{ value: "codex", agent: "codex", model: "", isDefault: true },
		]);
	});

	it("flags only the bare default, not an active fully qualified spec", () => {
		expect(
			modelOptionsFrom({ agents: ["codex"] }, [], ["codex/gpt-5.6"]),
		).toEqual([{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" }]);
	});

	it("offers no default entry for an agent that is not active", () => {
		expect(
			modelOptionsFrom(
				{ agents: ["codex"] },
				[{ name: "codex", models: ["codex/gpt-5.6"] }],
				["codex/gpt-5.6"],
			).some((option) => option.isDefault),
		).toBe(false);
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
	it("leads an agent's group with the default chip its bare spec selects", () => {
		const groups = modelGroupsFrom(
			{ agents: ["codex"] },
			[{ name: "codex", models: ["codex/gpt-5.6", "codex/gpt-5.6-codex"] }],
			["codex"],
		);
		expect(groups[0].options).toEqual([
			{ value: "codex", agent: "codex", model: "", isDefault: true },
			{ value: "codex/gpt-5.6", agent: "codex", model: "gpt-5.6" },
			{
				value: "codex/gpt-5.6-codex",
				agent: "codex",
				model: "gpt-5.6-codex",
			},
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

	it("replaces an agent's bare default when selecting one of its models", () => {
		expect(toggleActiveModel(["codex"], "codex/gpt-5.6", true)).toEqual([
			"codex/gpt-5.6",
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

describe("checkpointLines", () => {
	it("describes each run's outcome with its branch and reason", () => {
		expect(
			checkpointLines([
				{
					Target: "o/r",
					Number: 7,
					Branch: "fix/issue-7-x",
					Outcome: "pushed",
				},
				{
					Target: "o/r",
					Number: 8,
					Outcome: "failed",
					Detail: "remote origin is unreachable",
				},
			]),
		).toEqual([
			"o/r#7 (fix/issue-7-x) pushed",
			"o/r#8 failed: remote origin is unreachable",
		]);
	});

	it("is empty without checkpoints", () => {
		expect(checkpointLines(undefined)).toEqual([]);
	});
});
