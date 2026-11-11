import {
	ArrowPathIcon,
	Cog6ToothIcon,
	PowerIcon,
	StopIcon,
	XMarkIcon,
} from "@heroicons/react/24/outline";
import {
	Activity,
	Check,
	ChevronLeft,
	ChevronRight,
	Circle,
	CirclePause,
	Clock,
	Cpu,
	FolderGit2,
	Radio,
	Terminal,
	X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
	buildSettingsUpdate,
	checkpointLines,
	fetchRestartProgress,
	deliveryLabel,
	fetchAgentStatusesWithRetry,
	fetchSettingsWithRetry,
	jobActionAvailability,
	jobAgentSummary,
	jobPage,
	jobWaitLabel,
	modelGroupsFrom,
	modelOptionsFrom,
	stateURL,
	submitJobAction,
	submitRefresh,
	submitRestart,
	submitSettings,
	toggleActiveModel,
} from "./dashboard";
import "./index.css";

const emptyState = {
	version: "",
	snapshot: { Jobs: [], Targets: [], IssueCounts: {} },
	logs: [],
};

// useDashboardState polls /api/state for the page of jobs being shown, and
// polls again right away when the page changes so its output fills in without
// waiting for the next tick (issue #739).
function useDashboardState(page) {
	const [state, setState] = useState(emptyState);
	const [connected, setConnected] = useState(true);
	useEffect(() => {
		let stopped = false;
		const refresh = async () => {
			try {
				const response = await fetch(stateURL(page), { cache: "no-store" });
				if (!response.ok) throw new Error(`HTTP ${response.status}`);
				const next = await response.json();
				if (!stopped) {
					setState(next);
					setConnected(true);
				}
			} catch {
				if (!stopped) setConnected(false);
			}
		};
		refresh();
		const timer = window.setInterval(refresh, 1000);
		return () => {
			stopped = true;
			window.clearInterval(timer);
		};
	}, [page]);
	return [state, connected];
}

function ScrollViewport({ value, empty = "waiting for output...", status }) {
	const viewport = useRef(null);
	const follow = useRef(true);
	const [showMore, setShowMore] = useState(false);
	useEffect(() => {
		const node = viewport.current;
		if (node && follow.current) node.scrollTop = node.scrollHeight;
	});
	const onScroll = () => {
		const node = viewport.current;
		follow.current = node.scrollHeight - node.scrollTop - node.clientHeight < 8;
		setShowMore(!follow.current);
	};
	return (
		<div className="viewport-wrap">
			{status && <div className="viewport-status">{status}</div>}
			<pre ref={viewport} onScroll={onScroll} className="viewport">
				{value || empty}
			</pre>
			{showMore && (
				<button
					type="button"
					className="more"
					onClick={() => {
						follow.current = true;
						setShowMore(false);
						viewport.current?.scrollTo({
							top: viewport.current.scrollHeight,
							behavior: "smooth",
						});
					}}
				>
					more ↓
				</button>
			)}
		</div>
	);
}

function JobIcon({ status }) {
	if (status === "complete")
		return <Check className="status-icon complete" aria-label="complete" />;
	if (status === "failed")
		return <X className="status-icon failed" aria-label="failed" />;
	if (status === "active" || status === "stopping")
		return <Activity className="status-icon active" aria-label="active" />;
	if (status === "waiting")
		return <Clock className="status-icon waiting" aria-label="waiting" />;
	if (status === "paused")
		return <CirclePause className="status-icon paused" aria-label="paused" />;
	return <Circle className="status-icon queued" aria-label="queued" />;
}

function JobCard({ job }) {
	const [pendingAction, setPendingAction] = useState("");
	const [actionError, setActionError] = useState("");
	const available = jobActionAvailability(job.Status);
	const runAction = async (action) => {
		setPendingAction(action);
		setActionError("");
		try {
			await submitJobAction(job, action);
		} catch (error) {
			setActionError(error.message);
		} finally {
			setPendingAction("");
		}
	};
	return (
		<article className="card">
			<header className="card-header">
				<JobIcon status={job.Status} />
				<h2>
					#{job.Number} {job.Title}
				</h2>
			</header>
			<div className="meta" title={job.CheckoutDirectory}>
				<FolderGit2 /> checkout: {job.CheckoutDirectory || "pending"}
			</div>
			<div className="meta" title={job.SessionID}>
				<Terminal /> session: {job.SessionID || "pending"}
			</div>
			<div className="meta" title={jobAgentSummary(job)}>
				<Cpu /> agent: {jobAgentSummary(job)}
			</div>
			{(job.Status === "waiting" || job.Status === "paused") && (
				<div className="meta" title={jobWaitLabel(job)}>
					<Clock /> {jobWaitLabel(job)}
				</div>
			)}
			<div className="job-viewport">
				<div className="viewport-actions">
					<button
						type="button"
						disabled={!available.retry || pendingAction !== ""}
						onClick={() => runAction("retry")}
						title={actionError || "Retry gh-fix"}
					>
						<ArrowPathIcon /> Retry
					</button>
					<button
						type="button"
						disabled={!available.stop || pendingAction !== ""}
						onClick={() => runAction("stop")}
						title={actionError || "Stop gh-fix"}
					>
						<StopIcon /> Stop
					</button>
				</div>
				<ScrollViewport
					value={job.Log}
					status={`clone: ${job.CheckoutDirectory || "pending"}`}
				/>
			</div>
		</article>
	);
}

// JobPager steps through the pages of jobs when they do not all fit on one
// (issue #739), wrapping around at either end as the terminal dashboard does.
function JobPager({ page, pages, onPage }) {
	if (pages < 2) return null;
	return (
		<nav className="job-pager" aria-label="Job pages">
			<button
				type="button"
				onClick={() => onPage((page + pages - 1) % pages)}
				aria-label="Previous page of jobs"
			>
				<ChevronLeft />
			</button>
			<span>
				page {page + 1} of {pages}
			</span>
			<button
				type="button"
				onClick={() => onPage((page + 1) % pages)}
				aria-label="Next page of jobs"
			>
				<ChevronRight />
			</button>
		</nav>
	);
}

function formatQuotas(snapshot) {
	const quotas = snapshot.Quotas;
	if (quotas && Object.keys(quotas).length > 0) {
		return Object.keys(quotas)
			.sort()
			.map((name) => `${name}: ${quotas[name] || "not tracked"}`)
			.join(", ");
	}
	return snapshot.Quota || "unavailable";
}

function StatusBar({ snapshot, connected }) {
	const active = (snapshot.Running || 0) + (snapshot.Queued || 0);
	const idle = Math.max(0, (snapshot.Concurrency || 0) - active);
	const paused = snapshot.Paused || 0;
	const total =
		(snapshot.Completed || 0) + (snapshot.Failed || 0) + paused + active;
	const delivery = deliveryLabel(snapshot);
	const targets = (snapshot.Targets || [])
		.map(
			(target) => `${target} (${snapshot.IssueCounts?.[target] || 0} issues)`,
		)
		.join(", ");
	return (
		<footer className="status-bar">
			{snapshot.Identity && (
				<div className="status-cell id">id: {snapshot.Identity}</div>
			)}
			<div className="status-cell jobs">
				jobs: <span className="idle">{idle}</span> idle{" "}
				<span className="active-text">{active}</span> active{" "}
				{paused > 0 && (
					<>
						<span className="idle">{paused}</span> paused{" "}
					</>
				)}
				<span className="total">{total}</span> total
			</div>
			<div className="status-cell quota">quota: {formatQuotas(snapshot)}</div>
			<div className="status-cell delivery">
				<Radio /> {delivery}
				{!connected && " (offline)"}
			</div>
			<div className="status-cell targets">
				targets: {targets || "waiting..."}
			</div>
		</footer>
	);
}

// ModelChip renders one selectable agent/model entry as a toggle chip (issue
// #582), annotated with the auth and quota state /api/agents reports for its
// agent, so toggling which models are active reads the same information
// `glorp agents` prints to the terminal.
function ModelChip({ option, checked, onToggle, status }) {
	const probe = status
		? `auth: ${status.auth} · quota: ${status.quota}${status.installed ? "" : " · not installed"}`
		: "probing...";
	// A default chip pins no model, so its tooltip says which choice it is
	// standing in for rather than leaving it to be read as a model name
	// (issue #611).
	const title = option.isDefault
		? `${option.agent}'s own default model · ${probe}`
		: probe;
	return (
		<button
			type="button"
			className={`model-chip${checked ? " active" : ""}${option.isDefault ? " default" : ""}${status?.auth === "signed out" ? " signed-out" : ""}`}
			aria-pressed={checked}
			title={title}
			onClick={() => onToggle(option.value, !checked)}
		>
			{option.model ? (
				<>
					<span className="model-chip-agent">{option.agent}</span>/
					{option.model}
				</>
			) : option.isDefault ? (
				<>
					<span className="model-chip-agent">{option.agent}</span>/
					<span className="model-chip-default">default model</span>
				</>
			) : (
				option.agent
			)}
		</button>
	);
}

function SettingsModal({ onClose }) {
	const [tab, setTab] = useState("general");
	const [form, setForm] = useState(null);
	const [settings, setSettings] = useState(null);
	const [agentStatuses, setAgentStatuses] = useState([]);
	// probed flips once /api/agents has answered either way, so the models tab
	// can tell "still probing" from "probed, and this is all there is"
	// (issue #589).
	const [probed, setProbed] = useState(false);
	const [readyStateDefault, setReadyStateDefault] = useState("");
	const [error, setError] = useState("");
	const [saving, setSaving] = useState(false);
	useEffect(() => {
		// The dashboard can start serving before glorp's run loop is ready to
		// answer a settings request -- in webhook mode, ngrok startup and
		// per-target webhook configuration can take several seconds. Retrying
		// with backoff here keeps the modal on its loading state through that
		// window instead of surfacing it as an error the user can't act on
		// (issue #579).
		const controller = new AbortController();
		fetchSettingsWithRetry(controller.signal)
			.then((snapshot) => {
				setSettings(snapshot);
				setReadyStateDefault(snapshot.readyStateDefault ?? "");
				setForm({
					concurrency: String(snapshot.concurrency ?? ""),
					readyState: snapshot.readyState ?? "",
					allowedCommenters: (snapshot.allowedCommenters || []).join(", "),
					noMerge: Boolean(snapshot.noMerge),
					noChangelog: Boolean(snapshot.noChangelog),
					activeAgents: snapshot.configuredAgents ?? [],
				});
			})
			.catch((err) => err.name !== "AbortError" && setError(err.message));
		fetchAgentStatusesWithRetry(controller.signal)
			.then(
				(statuses) => !controller.signal.aborted && setAgentStatuses(statuses),
			)
			.catch(() => {
				// The agents tab's status column is a diagnostic on top of the
				// settings it lets you edit; a probe failure leaves it showing
				// what it knows rather than blocking the form.
			})
			.finally(() => !controller.signal.aborted && setProbed(true));
		return () => {
			controller.abort();
		};
	}, []);
	// The chips are derived rather than stored: the probed model lists arrive
	// after the settings snapshot does, and an agent's entries only exist once
	// they have (issue #589).
	const modelOptions = modelOptionsFrom(
		settings,
		agentStatuses,
		form?.activeAgents,
	);
	const modelGroups = modelGroupsFrom(
		settings,
		agentStatuses,
		form?.activeAgents,
	);
	const update = (field) => (event) =>
		setForm({ ...form, [field]: event.target.value });
	const toggleModel = (value, checked) =>
		setForm({
			...form,
			activeAgents: toggleActiveModel(form.activeAgents, value, checked),
		});
	const submit = async (event) => {
		event.preventDefault();
		setSaving(true);
		setError("");
		try {
			await submitSettings(buildSettingsUpdate(form));
			onClose();
		} catch (err) {
			setError(err.message);
			setSaving(false);
		}
	};
	return (
		<div className="modal-overlay">
			<button
				type="button"
				className="modal-overlay-dismiss"
				onClick={onClose}
				aria-label="Close settings"
			/>
			<div className="modal" role="dialog" aria-label="Settings">
				<header className="modal-header">
					<h2>Settings</h2>
					<button
						type="button"
						className="modal-close"
						onClick={onClose}
						aria-label="Close settings"
					>
						<XMarkIcon />
					</button>
				</header>
				{!form ? (
					<p className="modal-loading">{error || "loading settings..."}</p>
				) : (
					<form onSubmit={submit} className="modal-body">
						<div className="modal-tabs" role="tablist">
							<button
								type="button"
								role="tab"
								aria-selected={tab === "general"}
								className={tab === "general" ? "active" : ""}
								onClick={() => setTab("general")}
							>
								General
							</button>
							<button
								type="button"
								role="tab"
								aria-selected={tab === "models"}
								className={tab === "models" ? "active" : ""}
								onClick={() => setTab("models")}
							>
								Models
							</button>
						</div>
						{tab === "general" && (
							<>
								<label htmlFor="settings-concurrency">
									Concurrency
									<input
										id="settings-concurrency"
										type="number"
										min="1"
										max="256"
										value={form.concurrency}
										onChange={update("concurrency")}
									/>
								</label>
								<label htmlFor="settings-ready-state">
									Ready state label
									<input
										id="settings-ready-state"
										type="text"
										value={form.readyState}
										onChange={update("readyState")}
										placeholder={readyStateDefault || undefined}
									/>
								</label>
								<label htmlFor="settings-allowed-commenters">
									Allowed commenters (comma separated)
									<input
										id="settings-allowed-commenters"
										type="text"
										value={form.allowedCommenters}
										onChange={update("allowedCommenters")}
									/>
								</label>
								<label htmlFor="settings-no-merge" className="checkbox-label">
									<input
										id="settings-no-merge"
										type="checkbox"
										checked={form.noMerge}
										onChange={(event) =>
											setForm({ ...form, noMerge: event.target.checked })
										}
									/>
									Leave completed pull requests ready for human merge
								</label>
								<label
									htmlFor="settings-no-changelog"
									className="checkbox-label"
								>
									<input
										id="settings-no-changelog"
										type="checkbox"
										checked={form.noChangelog}
										onChange={(event) =>
											setForm({ ...form, noChangelog: event.target.checked })
										}
									/>
									Skip changelog entries in gh-fix pull requests
								</label>
							</>
						)}
						{tab === "models" && (
							<fieldset className="model-list">
								<legend className="sr-only">Active models</legend>
								{modelOptions.length === 0 && (
									<p className="modal-loading">
										{probed
											? "no agent models available"
											: "probing agents for models..."}
									</p>
								)}
								<div className="agent-model-groups">
									{modelGroups.map((group) => (
										<details
											key={group.agent}
											className={`agent-model-group${group.status?.auth === "signed out" ? " signed-out" : ""}`}
											open
										>
											<summary>
												<span>{group.agent}</span>
												<span className="agent-model-status">
													{group.status
														? `auth: ${group.status.auth} · quota: ${group.status.quota}${group.status.installed ? "" : " · not installed"}`
														: probed
															? "unavailable"
															: "probing..."}
												</span>
											</summary>
											<div className="agent-model-options">
												{group.options.length ? (
													group.options.map((option) => (
														<ModelChip
															key={option.value}
															option={option}
															checked={form.activeAgents.includes(option.value)}
															onToggle={toggleModel}
															status={group.status}
														/>
													))
												) : (
													<p className="agent-model-empty">
														{group.status?.modelNote || "no models reported"}
													</p>
												)}
											</div>
										</details>
									))}
								</div>
							</fieldset>
						)}
						{error && <p className="modal-error">{error}</p>}
						<div className="modal-actions">
							<button type="button" onClick={onClose} disabled={saving}>
								Cancel
							</button>
							<button type="submit" disabled={saving}>
								{saving ? "Saving…" : "Save"}
							</button>
						</div>
					</form>
				)}
			</div>
		</div>
	);
}

// CheckpointReport lists what the shutdown checkpoint did for each active run
// before the restart (issue #741): pushed, nothing to push, failed, or timed
// out, with the reason, and pending while the restart still waits on it.
function CheckpointReport({ checkpoints, onClose }) {
	const lines = checkpointLines(checkpoints);
	return (
		<section className="checkpoint-report" aria-label="Shutdown checkpoint">
			<div className="checkpoint-title">
				<FolderGit2 /> Shutdown checkpoint
				<button
					type="button"
					className="icon-button"
					onClick={onClose}
					aria-label="Dismiss shutdown checkpoint"
				>
					<XMarkIcon />
				</button>
			</div>
			{lines.length ? (
				<ul>
					{checkpoints.map((checkpoint, index) => (
						<li
							key={`${checkpoint.Target}#${checkpoint.Number}`}
							className={`checkpoint-${checkpoint.Outcome.replaceAll(" ", "-")}`}
						>
							{lines[index]}
						</li>
					))}
				</ul>
			) : (
				<p>No active agents had work to push.</p>
			)}
		</section>
	);
}

function App() {
	const [page, setPage] = useState(0);
	const [state, connected] = useDashboardState(page);
	const [settingsOpen, setSettingsOpen] = useState(false);
	const [refreshing, setRefreshing] = useState(false);
	// restarting is "requested" until the daemon drops off, then "down" until
	// it answers again, so the button stays busy through the whole restart.
	const [restarting, setRestarting] = useState("");
	// checkpoints holds the shutdown checkpoint outcomes the last restart
	// reported (issue #741), shown until dismissed.
	const [checkpoints, setCheckpoints] = useState(null);
	useEffect(() => {
		if (restarting === "requested" && !connected) setRestarting("down");
		if (restarting === "down" && connected) setRestarting("");
	}, [restarting, connected]);
	const snapshot = state.snapshot || emptyState.snapshot;
	const shown = jobPage(snapshot, page);
	// Follow the clamp when finished jobs empty the page being shown, so the
	// next poll asks for the output of the page actually drawn.
	useEffect(() => {
		if (shown.page !== page) setPage(shown.page);
	}, [shown.page, page]);
	const refresh = async () => {
		setRefreshing(true);
		try {
			await submitRefresh();
		} catch {
			// The next poll interval still catches up; nothing to surface here.
		} finally {
			setRefreshing(false);
		}
	};
	const restart = async () => {
		if (
			!window.confirm(
				"Restart glorp? Running agents are paused and their work is pushed to their branches, then they are stopped and resumed once it starts again.",
			)
		) {
			return;
		}
		setRestarting("requested");
		setCheckpoints(null);
		// Show each run's checkpoint as it lands while the restart waits on
		// them, so a slow run is visible as pending (issue #749).
		let waiting = true;
		const progress = setInterval(async () => {
			try {
				const runs = await fetchRestartProgress();
				if (waiting && runs.length) setCheckpoints(runs);
			} catch {
				// The final answer or the dropped connection says what happened.
			}
		}, 1000);
		try {
			const results = await submitRestart();
			waiting = false;
			setCheckpoints(results);
		} catch (error) {
			waiting = false;
			// A dropped connection means the daemon is already going down.
			if (error instanceof TypeError) return;
			setRestarting("");
			window.alert(`Could not restart glorp: ${error.message}`);
		} finally {
			clearInterval(progress);
		}
	};
	return (
		<main>
			<div className="masthead">
				<div>
					<p className="eyebrow">Git Loop fOr Robot Patchers</p>
					<h1>
						glorp <span>dashboard</span>
						{state.version && <span className="version">{state.version}</span>}
					</h1>
				</div>
				<div className="masthead-actions">
					<button
						type="button"
						className="refresh-button"
						onClick={refresh}
						disabled={refreshing}
						aria-label="Refresh now"
						title="Repoll GitHub now"
					>
						<ArrowPathIcon className={refreshing ? "spinning" : ""} />
					</button>
					<button
						type="button"
						className="restart-button"
						onClick={restart}
						disabled={restarting !== ""}
						aria-label="Restart glorp"
						title={restarting ? "Restarting glorp..." : "Restart glorp"}
					>
						<PowerIcon className={restarting ? "pulsing" : ""} />
					</button>
					<button
						type="button"
						className="settings-button"
						onClick={() => setSettingsOpen(true)}
						aria-label="Open settings"
						title="Settings"
					>
						<Cog6ToothIcon />
					</button>
					<div className={`connection ${connected ? "online" : "offline"}`}>
						<i />
						{connected ? "live" : "reconnecting"}
					</div>
				</div>
			</div>
			{settingsOpen && <SettingsModal onClose={() => setSettingsOpen(false)} />}
			<StatusBar snapshot={snapshot} connected={connected} />
			{checkpoints && (
				<CheckpointReport
					checkpoints={checkpoints}
					onClose={() => setCheckpoints(null)}
				/>
			)}
			<JobPager page={shown.page} pages={shown.pages} onPage={setPage} />
			<section className="job-grid" aria-label="Agent jobs">
				{shown.jobs.length ? (
					shown.jobs.map((job) => (
						<JobCard key={`${job.Target}#${job.Number}`} job={job} />
					))
				) : (
					<div className="empty-jobs">
						<Activity />
						<span>Waiting for agent jobs</span>
					</div>
				)}
			</section>
			<section className="log-card">
				<div className="log-title">
					<Terminal /> Logs
				</div>
				<ScrollViewport
					value={(state.logs || []).join("\n")}
					empty="waiting for daemon logs..."
				/>
			</section>
		</main>
	);
}

createRoot(document.getElementById("root")).render(<App />);
