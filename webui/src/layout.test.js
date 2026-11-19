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

	it("gives job actions a finger-sized target on phones", () => {
		expect(rule(".viewport-actions button", mobile)).toContain(
			"min-height: 36px",
		);
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

describe("job card actions (issue #850)", () => {
	it("lays the actions out in the status row instead of over it", () => {
		const actions = rule(".viewport-actions");
		expect(actions).not.toContain("position: absolute");
		expect(actions).toContain("flex: 0 0 auto");
		expect(css).not.toMatch(/padding-right: 264px/);
	});

	it("wraps the actions onto their own line when the card is narrow", () => {
		const status = rule(".job-viewport .viewport-status");
		expect(status).toContain("display: flex");
		expect(status).toContain("flex-wrap: wrap");
		expect(rule(".job-viewport .viewport-status-text")).toContain(
			"flex: 1 1 160px",
		);
	});

	it("truncates the clone path rather than letting it push the actions", () => {
		const text = rule(".viewport-status-text");
		expect(text).toContain("min-width: 0");
		expect(text).toContain("text-overflow: ellipsis");
	});

	it("never positions the actions absolutely on phones either", () => {
		expect(rule(".viewport-actions", mobile)).not.toContain("position:");
	});
});

describe("prompt agent panel (issue #828)", () => {
	const panelMobile = css.lastIndexOf("@media (max-width: 760px)");

	it("docks the panel to the right edge of the window", () => {
		const panel = rule(".prompt-panel");
		expect(panel).toContain("position: fixed");
		expect(panel).toContain("right: 0");
		expect(panel).toContain("width: var(--prompt-panel-width)");
	});

	it("gives up the panel's width from the dashboard while open", () => {
		expect(rule(".app-shell.prompt-open")).toContain(
			"padding-right: var(--prompt-panel-width)",
		);
	});

	it("keeps the dashboard full width on phones, where a bottom drawer opens", () => {
		// The override has to follow the panel's own rules to win the cascade.
		expect(panelMobile).toBeGreaterThan(css.indexOf(".prompt-panel {"));
		expect(rule(".app-shell.prompt-open", panelMobile)).toContain(
			"padding-right: 0",
		);
	});
});

describe("prompt agent bottom drawer (issue #834)", () => {
	it("anchors the drawer to the bottom of the screen over the dashboard", () => {
		const drawer = rule(".prompt-drawer");
		expect(drawer).toContain("position: fixed");
		expect(drawer).toContain("justify-content: flex-end");
		expect(rule(".prompt-drawer[hidden]")).toContain("display: none");
	});

	it("slides the sheet up from the bottom edge when it opens", () => {
		expect(rule(".prompt-drawer-sheet")).toContain(
			"animation: prompt-drawer-up",
		);
		expect(rule("@keyframes prompt-drawer-up")).toContain(
			"transform: translateY(100%)",
		);
	});

	it("rounds only the top corners, like a sheet", () => {
		expect(rule(".prompt-drawer-sheet")).toContain(
			"border-radius: 14px 14px 0 0",
		);
	});
});
