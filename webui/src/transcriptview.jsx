import {
	Check,
	Coins,
	FilePen,
	Loader,
	MessageSquare,
	TriangleAlert,
	Wrench,
	X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
	commentLabel,
	fetchJobEvents,
	mergeJobEvents,
	transcriptEntries,
	usageLabel,
} from "./transcript";

// useJobEvents keeps a job's typed events, reading only the ones after its
// cursor each time the job's output or status changes (issue #715).
export function useJobEvents(job) {
	const [events, setEvents] = useState(null);
	const latest = useRef(null);
	const key = `${job.Target || job.Repository}#${job.Number}`;
	const trigger = `${key}:${job.Status}:${(job.Log || "").length}`;
	// biome-ignore lint/correctness/useExhaustiveDependencies: refetched whenever the job's output or status changes.
	useEffect(() => {
		let cancelled = false;
		const since = latest.current?.key === key ? latest.current.latest : 0;
		fetchJobEvents(job, since)
			.then((response) => {
				if (cancelled || !response) return;
				const current = latest.current?.key === key ? latest.current : null;
				const merged = mergeJobEvents(current, response);
				if (merged === current) return;
				latest.current = { ...merged, key };
				setEvents(latest.current);
			})
			.catch(() => {});
		return () => {
			cancelled = true;
		};
	}, [trigger]);
	return events?.key === key ? events.events : [];
}

function ToolOutcome({ outcome }) {
	if (outcome === "ok")
		return (
			<span className="tool-outcome ok">
				<Check /> ok
			</span>
		);
	if (outcome === "failed")
		return (
			<span className="tool-outcome failed">
				<X /> FAIL
			</span>
		);
	if (outcome === "running")
		return (
			<span className="tool-outcome running">
				<Loader /> running
			</span>
		);
	return null;
}

function TranscriptEntry({ entry }) {
	switch (entry.kind) {
		case "text":
			return <div className="transcript-message">{entry.text}</div>;
		case "tool":
			return (
				<div
					className={`transcript-tool ${entry.outcome}`}
					title={entry.detail}
				>
					{entry.edit ? <FilePen /> : <Wrench />}
					<span className="transcript-tool-summary">{entry.summary}</span>
					{entry.edit && (
						<span className="transcript-edit">
							<span className="added">+{entry.edit.added}</span>{" "}
							<span className="removed">−{entry.edit.removed}</span>
						</span>
					)}
					<ToolOutcome outcome={entry.outcome} />
				</div>
			);
		case "comment":
			return (
				<div className="transcript-comment">
					<MessageSquare />
					<div>
						<a href={entry.comment.url} target="_blank" rel="noreferrer">
							{commentLabel(entry.comment)}
						</a>
						<div className="transcript-comment-note">sent to the job</div>
					</div>
				</div>
			);
		case "usage":
			return (
				<div className="transcript-usage">
					<Coins /> {usageLabel(entry.usage)}
				</div>
			);
		case "error":
			return (
				<div className="transcript-error">
					<TriangleAlert /> {entry.text}
				</div>
			);
		default:
			return <div className="transcript-output">{entry.text}</div>;
	}
}

// Transcript draws a job's events as a structured flow: the agent's messages
// as blocks, tool calls as one-line entries with their outcome, and comments
// sent to the job as attributed cards (issue #715).
export function Transcript({ events }) {
	return (
		<div className="transcript">
			{transcriptEntries(events).map((entry) => (
				<TranscriptEntry key={entry.key} entry={entry} />
			))}
		</div>
	);
}
