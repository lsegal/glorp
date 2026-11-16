import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SummarizeButton, summarizeJob, summarizeRequest } from "./summarize";

const job = { Number: 337, Target: "lsegal/nupp", Status: "running" };

describe("summarizeRequest (issue #840)", () => {
	it("names the job's issue by its OWNER/REPO#NUMBER key", () => {
		expect(summarizeRequest(job)).toBe(
			"Summarize the current session log of work on lsegal/nupp#337",
		);
	});

	it("uses the issue's repository for a job of a project target", () => {
		expect(
			summarizeRequest({
				Number: 12,
				Target: "users/lsegal/projects/3",
				Repository: "lsegal/glorp",
			}),
		).toBe("Summarize the current session log of work on lsegal/glorp#12");
	});
});

describe("summarizeJob (issue #840)", () => {
	it("opens the prompt agent and sends the job's summary request", () => {
		const calls = [];
		summarizeJob(job, {
			open: () => calls.push(["open"]),
			send: (text) => calls.push(["send", text]),
		});
		expect(calls).toEqual([
			["open"],
			["send", "Summarize the current session log of work on lsegal/nupp#337"],
		]);
	});
});

describe("SummarizeButton (issue #840)", () => {
	it("shows a Summarize button for running and finished jobs alike", () => {
		for (const Status of ["running", "succeeded", "failed", "paused"]) {
			const html = renderToStaticMarkup(
				<SummarizeButton job={{ ...job, Status }} onSummarize={() => {}} />,
			);
			expect(html).toContain("Summarize</span></button>");
			expect(html).toContain('aria-label="Summarize"');
			expect(html).not.toContain("disabled");
		}
	});

	it("hands its own job to onSummarize when clicked", () => {
		const clicked = [];
		const button = SummarizeButton({
			job,
			onSummarize: (target) => clicked.push(target),
		});
		button.props.onClick();
		expect(clicked).toEqual([job]);
	});

	it("is disabled when the job's issue is unknown", () => {
		const html = renderToStaticMarkup(
			<SummarizeButton
				job={{ Number: 4, Target: "users/lsegal/projects/3" }}
				onSummarize={() => {}}
			/>,
		);
		expect(html).toContain('disabled=""');
	});
});
