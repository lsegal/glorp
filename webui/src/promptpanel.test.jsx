import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PromptPanel } from "./promptpanel";

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
		expect(html).toContain("hidden");
		expect(html).toContain("<p>prompt agent</p>");
	});

	it("shows the panel with a keyboard-reachable resize handle when open", () => {
		const html = render(true);
		expect(html).not.toContain("hidden");
		expect(html).toContain('role="separator"');
		expect(html).toContain('aria-valuenow="420"');
		expect(html).toContain('tabindex="0"');
	});
});
