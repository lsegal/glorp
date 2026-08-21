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
	it("only enables retry for failed jobs", () => {
		expect(jobActionAvailability("failed")).toEqual({
			retry: true,
			stop: false,
		});
	});

	it("only enables stop for active jobs", () => {
		expect(jobActionAvailability("active")).toEqual({
			retry: false,
			stop: true,
		});
		expect(jobActionAvailability("queued")).toEqual({
			retry: false,
			stop: false,
		});
	});

	it("keeps both actions disabled for completed jobs", () => {
		expect(jobActionAvailability("complete")).toEqual({
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
