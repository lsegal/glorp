import { afterEach, describe, expect, it, vi } from "vitest";
import {
	deliveryLabel,
	jobActionAvailability,
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
	it("enables retry for active, failed, and completed jobs", () => {
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
			retry: false,
			stop: false,
		});
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
