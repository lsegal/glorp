import { useState } from "react";
import {
	clearClonesSummary,
	clearUnusedClones,
	cloneIssueURL,
	clonePullRequestURL,
	fetchClones,
} from "./dashboard";

function CloneLink({ href, children }) {
	if (!href) return children;
	return (
		<a href={href} target="_blank" rel="noopener noreferrer">
			{children}
		</a>
	);
}

// ClonesSection is the settings dialog's "Clear unused clones" control (issue
// #830). It stays collapsed, and reads nothing, until it is opened; opened, it
// lists every recorded clone with its repository, issue, and pull request and
// whether it is in use, and clears the unused ones.
export function ClonesSection() {
	const [snapshot, setSnapshot] = useState(null);
	const [error, setError] = useState("");
	const [result, setResult] = useState("");
	const [busy, setBusy] = useState(false);
	const load = async () => {
		setError("");
		try {
			setSnapshot(await fetchClones());
		} catch (err) {
			setError(err.message);
		}
	};
	const clear = async () => {
		setBusy(true);
		setError("");
		try {
			setResult(clearClonesSummary(await clearUnusedClones()));
			await load();
		} catch (err) {
			setError(err.message);
		} finally {
			setBusy(false);
		}
	};
	const clones = snapshot?.clones ?? [];
	const unused = clones.filter((clone) => !clone.inUse).length;
	return (
		<details
			className="clones-section"
			onToggle={(event) => event.currentTarget.open && load()}
		>
			<summary>
				<span>Clear unused clones</span>
				<span className="clones-count">
					{snapshot
						? `${clones.length} clone${clones.length === 1 ? "" : "s"}, ${unused} unused`
						: ""}
				</span>
			</summary>
			<div className="clones-body">
				{!snapshot && !error && (
					<p className="modal-loading">loading clones...</p>
				)}
				{snapshot && clones.length === 0 && (
					<p className="clones-empty">no clones on record</p>
				)}
				{clones.length > 0 && (
					<ul className="clones-list">
						{clones.map((clone) => (
							<li
								key={clone.path}
								className={clone.inUse ? "in-use" : "unused"}
								title={clone.reason}
							>
								<span className="clone-status">
									{clone.inUse ? "in use" : "unused"}
								</span>
								<span className="clone-path">{clone.path}</span>
								<span className="clone-meta">
									{clone.repository || clone.job || "unknown repository"}
									{clone.issue ? (
										<>
											{" "}
											<CloneLink href={cloneIssueURL(clone)}>
												#{clone.issue}
											</CloneLink>
										</>
									) : null}
									{clone.pullRequest ? (
										<>
											{" · "}
											<CloneLink href={clonePullRequestURL(clone)}>
												PR #{clone.pullRequest}
											</CloneLink>
										</>
									) : null}
								</span>
								{clone.reason && (
									<span className="clone-reason">{clone.reason}</span>
								)}
							</li>
						))}
					</ul>
				)}
				{result && <p className="clones-result">{result}</p>}
				{error && <p className="modal-error">{error}</p>}
				<button
					type="button"
					className="clones-clear"
					onClick={clear}
					disabled={busy || !snapshot || unused === 0}
				>
					{busy ? "Clearing…" : `Clear ${unused} unused`}
				</button>
			</div>
		</details>
	);
}
