import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ReleaseLink } from "./releaselink";

describe("ReleaseLink (issue #825)", () => {
	it("links the version to its release page in a new tab", () => {
		const html = renderToStaticMarkup(
			<ReleaseLink
				className="version"
				href="https://github.com/lsegal/glorp/releases/tag/v1.5.22"
			>
				v1.5.22
			</ReleaseLink>,
		);
		expect(html).toBe(
			'<a class="release-link version" href="https://github.com/lsegal/glorp/releases/tag/v1.5.22" target="_blank" rel="noopener noreferrer" title="Open the GitHub release page">v1.5.22</a>',
		);
	});

	it("wraps other markup, such as the update notice's version", () => {
		const html = renderToStaticMarkup(
			<ReleaseLink href="https://github.com/lsegal/glorp/releases/tag/v1.6.0">
				<strong>v1.6.0</strong>
			</ReleaseLink>,
		);
		expect(html).toContain(
			'href="https://github.com/lsegal/glorp/releases/tag/v1.6.0"',
		);
		expect(html).toContain("<strong>v1.6.0</strong></a>");
	});

	it("shows plain text without a release page", () => {
		expect(renderToStaticMarkup(<ReleaseLink>dev</ReleaseLink>)).toBe("dev");
	});
});
