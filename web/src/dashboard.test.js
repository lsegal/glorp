import { afterEach, describe, expect, it, vi } from "vitest";
import {
	deliveryLabel,
	jobActionAvailability,
	jobAgentSummary,
	submitJobAction,
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
