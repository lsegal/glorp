import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
	commentLabel,
	editCounts,
	fetchJobEvents,
	jobEventsURL,
	mergeJobEvents,
	transcriptEntries,
	usageLabel,
} from "./transcript";
import { Transcript } from "./transcriptview";

const job = { Number: 715, Target: "lsegal/glorp" };

describe("jobEventsURL (issue #715)", () => {
	it("names the job by its target and number with the cursor", () => {
		expect(jobEventsURL(job, 12)).toBe(
			"/api/jobs/lsegal/glorp/715/events?since=12",
		);
	});

	it("falls back to the repository and refuses an unknown job", () => {
		expect(jobEventsURL({ Number: 3, Repository: "o/r" })).toBe(
			"/api/jobs/o/r/3/events?since=0",
		);
		expect(jobEventsURL({ Number: 0, Target: "o/r" })).toBe("");
	});
});

describe("mergeJobEvents (issue #715)", () => {
	const first = {
		run: 1,
		latest: 2,
		events: [
			{ seq: 1, kind: "text" },
			{ seq: 2, kind: "tool" },
		],
	};

	it("appends only events newer than the ones it holds", () => {
		const merged = mergeJobEvents(first, {
			run: 1,
			latest: 3,
			events: [
				{ seq: 2, kind: "tool" },
				{ seq: 3, kind: "text" },
			],
		});
		expect(merged.events.map((event) => event.seq)).toEqual([1, 2, 3]);
		expect(merged.latest).toBe(3);
	});

	it("keeps the same object when nothing changed", () => {
		expect(mergeJobEvents(first, { run: 1, latest: 2, events: [] })).toBe(
			first,
		);
	});

	it("starts over for a new run or a truncated response", () => {
		const next = { run: 2, latest: 4, events: [{ seq: 4, kind: "text" }] };
		expect(mergeJobEvents(first, next).events).toEqual(next.events);
		const gap = { ...next, run: 1, truncated: true };
		expect(mergeJobEvents(first, gap).events).toEqual(next.events);
	});
});

describe("editCounts (issue #715)", () => {
	it("counts an edit's added and removed lines", () => {
		expect(
			editCounts({ file_path: "a.go", old_string: "a\nb", new_string: "c" }),
		).toEqual({ added: 1, removed: 2 });
		expect(
			editCounts({ filePath: "a.go", oldString: "", newString: "x\ny\n" }),
		).toEqual({ added: 2, removed: 0 });
	});

	it("sums a multi-edit and counts a written file", () => {
		expect(
			editCounts({
				edits: [
					{ old_string: "a", new_string: "b\nc" },
					{ old_string: "d", new_string: "" },
				],
			}),
		).toEqual({ added: 2, removed: 2 });
		expect(editCounts({ file_path: "n.go", content: "1\n2\n3" })).toEqual({
			added: 3,
			removed: 0,
		});
	});

	it("is null for a call that edits no file", () => {
		expect(editCounts({ command: "go test ./..." })).toBeNull();
		expect(editCounts(undefined)).toBeNull();
	});
});

describe("transcriptEntries (issue #715)", () => {
	const events = [
		{ seq: 1, kind: "text", text: "Reading the issue." },
		{ seq: 2, kind: "tool", tool: "Bash", toolID: "a", summary: "go test" },
		{ seq: 3, kind: "tool_result", toolID: "a", isError: true, text: "FAIL" },
		{
			seq: 4,
			kind: "tool",
			tool: "Edit",
			toolID: "b",
			summary: "Edit main.go",
			input: { old_string: "x", new_string: "y\nz" },
		},
		{ seq: 5, kind: "tool", tool: "Read", toolID: "c", summary: "Read a.go" },
		{ seq: 6, kind: "tool_result", toolID: "c", isError: false },
	];

	it("draws tool calls apart from messages, with their outcome", () => {
		const entries = transcriptEntries(events);
		expect(entries.map((entry) => entry.kind)).toEqual([
			"text",
			"tool",
			"tool",
			"tool",
		]);
		expect(entries[1]).toMatchObject({ outcome: "failed", detail: "FAIL" });
		expect(entries[2]).toMatchObject({
			outcome: "running",
			edit: { added: 2, removed: 1 },
		});
		expect(entries[3]).toMatchObject({ outcome: "ok" });
	});

	it("renders messages, tool lines, comments, and usage", () => {
		const markup = renderToStaticMarkup(
			<Transcript
				events={[
					...events,
					{
						seq: 7,
						kind: "comment",
						comment: {
							author: "lsegal",
							url: "https://github.com/o/r/pull/9#issuecomment-1",
							number: 9,
							pullRequest: true,
						},
					},
					{
						seq: 8,
						kind: "usage",
						usage: { inputTokens: 1200, outputTokens: 30, costUSD: 0.5 },
					},
				]}
			/>,
		);
		expect(markup).toContain('class="transcript-message markdown"');
		expect(markup).toContain("Reading the issue.");
		expect(markup).toContain("FAIL");
		expect(markup).toContain("+2");
		expect(markup).toContain("lsegal commented on pull request #9");
		expect(markup).toContain("sent to the job");
		expect(markup).toContain("tokens: 1.2k in · 30 out · $0.50");
	});
});

describe("labels (issue #715)", () => {
	it("describes usage and comments", () => {
		expect(
			usageLabel({
				inputTokens: 5,
				outputTokens: 2_500_000,
				cacheReadTokens: 10,
			}),
		).toBe("tokens: 5 in · 2.5M out · 10 cached");
		expect(commentLabel({ number: 4 })).toBe("someone commented on issue #4");
	});
});

describe("fetchJobEvents (issue #715)", () => {
	it("reads the events after the cursor and treats a missing job as none", async () => {
		const requested = [];
		const body = { run: 1, latest: 1, events: [] };
		const found = await fetchJobEvents(job, 5, async (url) => {
			requested.push(url);
			return { ok: true, status: 200, json: async () => body };
		});
		expect(found).toEqual(body);
		expect(requested).toEqual(["/api/jobs/lsegal/glorp/715/events?since=5"]);
		const missing = await fetchJobEvents(job, 0, async () => ({
			ok: false,
			status: 404,
		}));
		expect(missing).toBeNull();
	});
});
