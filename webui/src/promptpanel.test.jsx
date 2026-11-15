import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { mobileQuery, PromptModal, PromptPanel } from "./promptpanel";

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

const renderModal = (open) =>
	renderToStaticMarkup(
		<PromptModal open={open} onClose={() => {}}>
			<p>prompt agent</p>
		</PromptModal>,
	);

describe("PromptModal (issue #828)", () => {
	it("opens the prompt agent as a dialog with a close button", () => {
		const html = renderModal(true);
		expect(html).not.toContain('hidden=""');
		expect(html).toContain('role="dialog"');
		expect(html).toContain('aria-modal="true"');
		expect(html).toContain('aria-label="Close prompt agent"');
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("has no resize handle, since a modal is not dragged", () => {
		expect(renderModal(true)).not.toContain('role="separator"');
	});

	it("keeps the prompt agent mounted but hidden while closed", () => {
		const html = renderModal(false);
		expect(html).toContain('hidden=""');
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("switches at the same width as the narrow-screen layout", () => {
		const css = readFileSync(new URL("./index.css", import.meta.url), "utf8");
		expect(css).toContain(`@media ${mobileQuery}`);
	});
});
