import { describe, expect, it } from "vitest";
import { deliveryLabel } from "./dashboard";

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
