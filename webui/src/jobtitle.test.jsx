import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { JobTitle } from "./jobtitle";

const render = (job) => renderToStaticMarkup(<JobTitle job={job} />);

describe("JobTitle (issue #767)", () => {
	it("links the issue number and pull request label in new tabs", () => {
		const html = render({
			Number: 24,
			Target: "lsegal/nupp",
			Title: "Delete the platform crate",
			PullRequest: 190,
		});
		expect(html).toContain(
			'<a class="job-link" href="https://github.com/lsegal/nupp/issues/24" target="_blank" rel="noopener noreferrer">#24</a>',
		);
		expect(html).toContain(
			'<a class="job-link" href="https://github.com/lsegal/nupp/pull/190" target="_blank" rel="noopener noreferrer">PR #190</a>',
		);
		expect(html).toContain("Delete the platform crate");
	});

	it("links a fork's pull request to the fork's repository", () => {
		const html = render({
			Number: 24,
			Target: "lsegal/nupp",
			PullRequest: 12,
			PullRequestRepository: "fork/nupp",
		});
		expect(html).toContain('href="https://github.com/lsegal/nupp/issues/24"');
		expect(html).toContain(
			'href="https://github.com/fork/nupp/pull/12" target="_blank" rel="noopener noreferrer">PR fork/nupp#12</a>',
		);
	});

	it("links a project job's issue to the issue's repository", () => {
		const html = render({
			Number: 5,
			Target: "https://github.com/users/o/projects/1",
			Repository: "o/r",
		});
		expect(html).toContain('href="https://github.com/o/r/issues/5"');
	});

	it("shows plain numbers when the repository is unknown", () => {
		const html = render({
			Number: 5,
			Target: "https://github.com/users/o/projects/1",
			PullRequest: 6,
			Title: "t",
		});
		expect(html).not.toContain("<a");
		expect(html).toContain("#5 · PR #6 t");
	});
});
