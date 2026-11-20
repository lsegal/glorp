import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown, safeHref } from "./markdown";
import { PromptResponse, Transcript } from "./transcriptview";

const render = (text) => renderToStaticMarkup(<Markdown text={text} />);

describe("Markdown (issue #863)", () => {
	it("renders bold text and a bulleted list without literal markers", () => {
		const html = render(
			"**The problem:** the card hid lines.\n\n- first *point*\n- second point",
		);
		expect(html).toContain("<strong>The problem:</strong>");
		expect(html).toContain("<ul><li><span>first <em>point</em></span></li>");
		expect(html).toContain("<em>point</em>");
		expect(html).not.toContain("**");
		expect(html).not.toContain("- first");
	});

	it("renders links, inline code, and code blocks", () => {
		const html = render(
			"See [#856](https://github.com/lsegal/glorp/pull/856) and `go test`.\n\n```go\nfunc main() {}\n```",
		);
		expect(html).toContain(
			'<a href="https://github.com/lsegal/glorp/pull/856" target="_blank" rel="noreferrer">#856</a>',
		);
		expect(html).toContain("<code>go test</code>");
		expect(html).toContain("<pre><code>func main() {}</code></pre>");
	});

	it("renders headings, ordered and nested lists, quotes, and tables", () => {
		const html = render(
			"## What changed\n\n1. one\n   - nested\n2. two\n\n> quoted\n\n| a | b |\n| - | - |\n| 1 | 2 |",
		);
		expect(html).toContain("<h2>What changed</h2>");
		expect(html).toContain(
			"<ol><li><span>one</span><ul><li><span>nested</span></li></ul></li><li><span>two</span></li></ol>",
		);
		expect(html).toContain("<blockquote><p>quoted</p></blockquote>");
		expect(html).toContain("<th>a</th>");
		expect(html).toContain("<td>2</td>");
	});

	it("links bare URLs and keeps snake_case words intact", () => {
		const html = render("open https://example.com/a_b. in some_var_name");
		expect(html).toContain('<a href="https://example.com/a_b"');
		expect(html).toContain("some_var_name");
		expect(html).not.toContain("<em>");
	});

	it("shows raw HTML as inert text", () => {
		const html = render(
			'<script>alert(1)</script>\n\n<img src=x onerror="alert(2)">',
		);
		expect(html).not.toContain("<script");
		expect(html).not.toContain("<img");
		expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
		expect(html).toContain("&lt;img src=x onerror=");
	});

	it("drops links to unsafe schemes", () => {
		const html = render(
			"[click](javascript:alert(1)) [data](data:text/html,x)",
		);
		expect(html).not.toContain("href");
		expect(html).toContain("click");
		expect(safeHref("JavaScript:alert(1)")).toBe("");
		expect(safeHref("mailto:a@b.c")).toBe("mailto:a@b.c");
	});
});

describe("Transcript markdown (issue #863)", () => {
	it("renders the agent's messages as markdown", () => {
		const html = renderToStaticMarkup(
			<Transcript
				events={[{ seq: 1, kind: "text", text: "**bold** <b>x</b>" }]}
			/>,
		);
		expect(html).toContain('class="transcript-message markdown"');
		expect(html).toContain("<strong>bold</strong>");
		expect(html).toContain("&lt;b&gt;x&lt;/b&gt;");
	});

	it("keeps output lines as they are", () => {
		const html = renderToStaticMarkup(
			<Transcript events={[{ seq: 1, kind: "output", text: "**raw**" }]} />,
		);
		expect(html).toContain('<div class="transcript-output">**raw**</div>');
	});
});

describe("PromptResponse (issue #863)", () => {
	it("draws the prompt agent's events through the job transcript", () => {
		const html = renderToStaticMarkup(
			<PromptResponse
				entry={{
					status: "done",
					response: "Running: gh pr view 460\n**What the session changed**",
					events: [
						{
							seq: 1,
							kind: "tool",
							tool: "Bash",
							toolID: "t1",
							summary: "Running: gh pr view 460",
						},
						{ seq: 2, kind: "tool_result", toolID: "t1" },
						{
							seq: 3,
							kind: "text",
							text: "**What the session changed**\n- one",
						},
					],
				}}
			/>,
		);
		expect(html).toContain('class="transcript"');
		expect(html).toContain("transcript-tool ok");
		expect(html).toContain("Running: gh pr view 460");
		expect(html).toContain("<strong>What the session changed</strong>");
		expect(html).toContain("<li><span>one</span></li>");
	});

	it("renders an answer without typed events as markdown", () => {
		const html = renderToStaticMarkup(
			<PromptResponse
				entry={{
					status: "done",
					response: "**done**",
					events: [{ seq: 1, kind: "output", text: "**done**" }],
				}}
			/>,
		);
		expect(html).toContain('class="prompt-response markdown"');
		expect(html).toContain("<strong>done</strong>");
		expect(html).not.toContain('class="transcript"');
	});
});
