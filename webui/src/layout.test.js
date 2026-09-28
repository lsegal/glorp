import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./index.css", import.meta.url), "utf8");

// rule returns the declarations of the first `selector {` block at or after
// from, so a rule can be read either at top level or inside a media query.
function rule(selector, from = 0) {
	const start = css.indexOf(`${selector} {`, from);
	if (start < 0) return "";
	return css.slice(start, css.indexOf("}", start));
}

const mobile = css.indexOf("@media (max-width: 760px)");

describe("mobile layout (issue #669)", () => {
	it("keeps a job card's column from growing past the card", () => {
		// An auto column grows to its widest nowrap child, such as a long clone
		// path, which pushed the Retry and Stop buttons off the card on phones.
		expect(rule(".card")).toContain("grid-template-columns: minmax(0, 1fr)");
	});

	it("has a narrow-screen media query", () => {
		expect(mobile).toBeGreaterThan(0);
	});

	it("moves job actions out of the log header on phones", () => {
		expect(rule(".viewport-actions", mobile)).toContain("position: static");
		expect(rule(".job-viewport", mobile)).toContain("flex-direction: column");
	});

	it("lets the masthead wrap on phones", () => {
		expect(rule(".masthead", mobile)).toContain("flex-wrap: wrap");
	});

	it("gives masthead buttons a finger-sized target on phones", () => {
		expect(rule(".restart-button", mobile)).toContain("height: 40px");
	});

	it("keeps settings fields at 16px so mobile Safari does not zoom", () => {
		expect(rule(".modal-body select", mobile)).toContain("font-size: 16px");
	});
});
