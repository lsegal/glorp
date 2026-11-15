import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
	drawerDismissDistance,
	mobileQuery,
	PromptDrawer,
	PromptPanel,
	shouldDismissDrawer,
} from "./promptpanel";

const render = (open) =>
	renderToStaticMarkup(
		<PromptPanel open={open} width={420} onResize={() => {}}>
			<p>prompt agent</p>
		</PromptPanel>,
	);

describe("PromptPanel (issue #828)", () => {
	it("keeps the prompt agent mounted but hidden while closed", () => {
		const html = render(false);
		expect(html).toContain('id="prompt-panel"');
		expect(html).toContain('hidden=""');
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("shows the panel with a keyboard-reachable resize handle when open", () => {
		const html = render(true);
		expect(html).not.toContain('hidden=""');
		expect(html).toContain('role="separator"');
		expect(html).toContain('aria-valuenow="420"');
		expect(html).toContain('tabindex="0"');
	});
});

const renderDrawer = (open) =>
	renderToStaticMarkup(
		<PromptDrawer open={open} onClose={() => {}}>
			<p>prompt agent</p>
		</PromptDrawer>,
	);

describe("PromptDrawer (issue #834)", () => {
	it("opens the prompt agent as a bottom drawer with a close button", () => {
		const html = renderDrawer(true);
		expect(html).not.toContain('hidden=""');
		expect(html).toContain('class="prompt-drawer"');
		expect(html).toContain('class="prompt-drawer-sheet"');
		expect(html).toContain('role="dialog"');
		expect(html).toContain('aria-modal="true"');
		expect(html).toContain('aria-label="Close prompt agent"');
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("is not a side panel or a centered modal", () => {
		const html = renderDrawer(true);
		expect(html).not.toContain('role="separator"');
		expect(html).not.toContain("prompt-panel-resizer");
		expect(html).not.toContain('class="modal');
	});

	it("dims the dashboard behind it with a tap-to-close backdrop", () => {
		expect(renderDrawer(true)).toContain('class="prompt-drawer-backdrop"');
	});

	it("keeps the prompt agent mounted but hidden while closed", () => {
		const html = renderDrawer(false);
		expect(html).toContain('hidden=""');
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("switches at the same width as the narrow-screen layout", () => {
		const css = readFileSync(new URL("./index.css", import.meta.url), "utf8");
		expect(css).toContain(`@media ${mobileQuery}`);
	});
});

describe("shouldDismissDrawer (issue #834)", () => {
	it("closes on a long enough swipe down", () => {
		expect(shouldDismissDrawer(drawerDismissDistance, 640)).toBe(true);
		expect(shouldDismissDrawer(300, 640)).toBe(true);
	});

	it("snaps back on a short swipe or an upward drag", () => {
		expect(shouldDismissDrawer(drawerDismissDistance - 1, 640)).toBe(false);
		expect(shouldDismissDrawer(0, 640)).toBe(false);
		expect(shouldDismissDrawer(-200, 640)).toBe(false);
		expect(shouldDismissDrawer(Number.NaN, 640)).toBe(false);
	});

	it("needs only a quarter of a short drawer's height", () => {
		expect(shouldDismissDrawer(60, 200)).toBe(true);
		expect(shouldDismissDrawer(40, 200)).toBe(false);
	});
});
