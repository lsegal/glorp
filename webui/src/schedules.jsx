import { XMarkIcon } from "@heroicons/react/24/outline";
import { CalendarClock, Play, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import {
	deleteSchedule,
	describeTrigger,
	emptyScheduleForm,
	fetchSchedules,
	formatDuration,
	formatFireTime,
	formatRelative,
	formToSchedule,
	formTrigger,
	lastRunLabel,
	previewTrigger,
	runScheduleNow,
	saveSchedule,
	scheduleToForm,
	setScheduleEnabled,
	toggleWeekday,
	triggerKinds,
	weekdayNames,
} from "./scheduling";

// UpNextStrip lists the next scheduled runs above the job grid (issue #848).
// It is hidden when no schedule is enabled.
export function UpNextStrip({ upcoming, onOpen }) {
	if (!upcoming?.length) return null;
	return (
		<section className="up-next" aria-label="Up next">
			<span className="up-next-title">
				<CalendarClock /> Up next
			</span>
			<ol>
				{upcoming.map((run) => (
					<li key={`${run.scheduleId}@${run.at}`}>
						<button
							type="button"
							onClick={() => onOpen(run.scheduleId)}
							title={`${run.name} on ${run.repository} at ${formatFireTime(run.at)}`}
						>
							<strong>{run.name}</strong>
							<span>{formatRelative(run.at)}</span>
						</button>
					</li>
				))}
			</ol>
		</section>
	);
}

// TriggerFields edits the part of the trigger its kind uses.
function TriggerFields({ form, set }) {
	switch (form.kind) {
		case "once":
			return (
				<label>
					Run at
					<input
						type="datetime-local"
						value={form.at}
						onChange={(event) => set({ at: event.target.value })}
					/>
				</label>
			);
		case "interval":
			return (
				<label>
					Every
					<input
						value={form.every}
						placeholder="30m, 6h, 24h"
						onChange={(event) => set({ every: event.target.value })}
					/>
				</label>
			);
		case "cron":
			return (
				<label>
					Cron expression
					<input
						value={form.cron}
						placeholder="minute hour day month weekday"
						onChange={(event) => set({ cron: event.target.value })}
					/>
				</label>
			);
		default:
			return (
				<>
					<label>
						Time
						<input
							type="time"
							value={form.time}
							onChange={(event) => set({ time: event.target.value })}
						/>
					</label>
					{form.kind === "weekly" && (
						<fieldset className="weekday-picker">
							<legend>Days</legend>
							{weekdayNames.map((name, day) => (
								<button
									key={name}
									type="button"
									className={form.weekdays.includes(day) ? "active" : ""}
									aria-pressed={form.weekdays.includes(day)}
									onClick={() =>
										set({ weekdays: toggleWeekday(form.weekdays, day) })
									}
								>
									{name}
								</button>
							))}
						</fieldset>
					)}
				</>
			);
	}
}

// useTriggerPreview fetches the next three fire times of the trigger being
// edited, shortly after the last change.
function useTriggerPreview(trigger) {
	const key = JSON.stringify(trigger);
	const [preview, setPreview] = useState({ fires: [], error: "" });
	useEffect(() => {
		let stopped = false;
		const timer = setTimeout(async () => {
			try {
				const fires = await previewTrigger(JSON.parse(key));
				if (!stopped) setPreview({ fires, error: "" });
			} catch (error) {
				if (!stopped) setPreview({ fires: [], error: error.message });
			}
		}, 250);
		return () => {
			stopped = true;
			clearTimeout(timer);
		};
	}, [key]);
	return preview;
}

function ScheduleEditor({ schedule, form, setForm, onSaved, onDeleted }) {
	const [busy, setBusy] = useState("");
	const [error, setError] = useState("");
	const [notice, setNotice] = useState("");
	const set = (change) => setForm({ ...form, ...change });
	const preview = useTriggerPreview(formTrigger(form));
	const act = async (name, action) => {
		setBusy(name);
		setError("");
		setNotice("");
		try {
			await action();
		} catch (err) {
			setError(err.message);
		} finally {
			setBusy("");
		}
	};
	const save = (event) => {
		event.preventDefault();
		act("save", async () => {
			const saved = await saveSchedule(form.id, formToSchedule(form));
			onSaved(saved);
			setNotice("Saved.");
		});
	};
	const runNow = () =>
		act("run", async () => {
			const saved = await runScheduleNow(form.id);
			onSaved(saved);
			setNotice("Run started; it shows up on the dashboard.");
		});
	const remove = () => {
		if (!window.confirm(`Delete schedule "${form.name || form.id}"?`)) return;
		act("delete", async () => {
			await deleteSchedule(form.id);
			onDeleted(form.id);
		});
	};
	const history = [...(schedule?.history || [])].reverse();
	return (
		<form className="schedule-editor modal-body" onSubmit={save}>
			<div className="schedule-editor-grid">
				<label>
					Name
					<input
						value={form.name}
						placeholder="Nightly dependency sweep"
						onChange={(event) => set({ name: event.target.value })}
					/>
				</label>
				<label>
					Repository
					<input
						value={form.repository}
						placeholder="OWNER/REPO"
						onChange={(event) => set({ repository: event.target.value })}
					/>
				</label>
			</div>
			<label>
				Prompt
				<textarea
					rows={4}
					value={form.prompt}
					placeholder="Find outdated dependencies and file an issue for each one worth upgrading."
					onChange={(event) => set({ prompt: event.target.value })}
				/>
			</label>
			<div className="schedule-editor-grid">
				<label>
					Trigger
					<select
						value={form.kind}
						onChange={(event) => set({ kind: event.target.value })}
					>
						{triggerKinds.map((kind) => (
							<option key={kind.value} value={kind.value}>
								{kind.label}
							</option>
						))}
					</select>
				</label>
				<label>
					Timezone
					<input
						value={form.timezone}
						placeholder="local"
						onChange={(event) => set({ timezone: event.target.value })}
					/>
				</label>
			</div>
			<div className="schedule-editor-grid">
				<TriggerFields form={form} set={set} />
			</div>
			<div className="schedule-preview" aria-live="polite">
				<span>Next runs</span>
				{preview.error ? (
					<em>{preview.error}</em>
				) : preview.fires.length ? (
					<ol>
						{preview.fires.map((fire) => (
							<li key={fire}>{formatFireTime(fire)}</li>
						))}
					</ol>
				) : (
					<em>never</em>
				)}
			</div>
			<div className="schedule-editor-grid">
				<label>
					Agent
					<input
						value={form.agent}
						placeholder="default: the watch's agents"
						onChange={(event) => set({ agent: event.target.value })}
					/>
				</label>
				<label>
					If the previous run is still active
					<select
						value={form.overlap}
						onChange={(event) => set({ overlap: event.target.value })}
					>
						<option value="skip">Skip this run</option>
						<option value="queue">Queue it until that run ends</option>
					</select>
				</label>
				<label>
					Runs missed while asleep or stopped
					<select
						value={form.missed}
						onChange={(event) => set({ missed: event.target.value })}
					>
						<option value="skip">Skip them</option>
						<option value="runOnce">Run once on wake</option>
					</select>
				</label>
				<label className="checkbox-label">
					<input
						type="checkbox"
						checked={form.enabled}
						onChange={(event) => set({ enabled: event.target.checked })}
					/>
					Enabled
				</label>
			</div>
			{form.id && (
				<div className="schedule-history">
					<span>Recent runs</span>
					{history.length ? (
						<ul>
							{history.slice(0, 5).map((run) => (
								<li key={`${run.fired}-${run.status}`}>
									<i className={`run-status ${run.status}`}>{run.status}</i>
									<span>{formatFireTime(run.fired)}</span>
									<span>{formatDuration(run.duration)}</span>
									<span className="run-summary" title={run.summary}>
										{run.summary}
									</span>
								</li>
							))}
						</ul>
					) : (
						<em>No runs yet</em>
					)}
				</div>
			)}
			{error && <p className="modal-error">{error}</p>}
			{notice && <p className="schedule-notice">{notice}</p>}
			<div className="modal-actions">
				{form.id && (
					<button
						type="button"
						className="schedule-delete"
						onClick={remove}
						disabled={busy !== ""}
					>
						<Trash2 /> Delete
					</button>
				)}
				{form.id && (
					<button type="button" onClick={runNow} disabled={busy !== ""}>
						<Play /> {busy === "run" ? "Starting..." : "Run now"}
					</button>
				)}
				<button type="submit" disabled={busy !== ""}>
					{busy === "save" ? "Saving..." : "Save"}
				</button>
			</div>
		</form>
	);
}

// SchedulesModal lists the schedules on the left, each with an enable toggle
// and its last result, and edits the selected one on the right (issue #848).
export function SchedulesModal({ onClose, initialId, defaultRepository }) {
	const [schedules, setSchedules] = useState(null);
	const [error, setError] = useState("");
	const [selected, setSelected] = useState(initialId || "");
	const [form, setForm] = useState(() => emptyScheduleForm(defaultRepository));
	const select = (schedule) => {
		setSelected(schedule ? schedule.id : "");
		setForm(
			schedule
				? scheduleToForm(schedule)
				: emptyScheduleForm(defaultRepository),
		);
	};
	// biome-ignore lint/correctness/useExhaustiveDependencies: loads once on open.
	useEffect(() => {
		let stopped = false;
		const load = async (attempt = 0) => {
			try {
				const list = await fetchSchedules();
				if (stopped) return;
				setSchedules(list);
				const initial = list.find((schedule) => schedule.id === initialId);
				select(initial || list[0]);
			} catch (err) {
				// The run may still be starting; try again shortly.
				if (!stopped && attempt < 5) {
					setTimeout(() => load(attempt + 1), 1000);
				} else if (!stopped) {
					setError(err.message);
				}
			}
		};
		load();
		return () => {
			stopped = true;
		};
	}, []);
	const replace = (saved) => {
		if (!saved) return;
		setSchedules((list) => {
			const index = list.findIndex((schedule) => schedule.id === saved.id);
			if (index < 0) return [...list, saved];
			return list.map((schedule) =>
				schedule.id === saved.id ? saved : schedule,
			);
		});
		if (saved.id !== selected || !form.id) select(saved);
	};
	const toggle = async (schedule) => {
		try {
			replace(await setScheduleEnabled(schedule.id, !schedule.enabled));
			if (schedule.id === selected) {
				setForm((current) => ({ ...current, enabled: !schedule.enabled }));
			}
		} catch (err) {
			setError(err.message);
		}
	};
	const current = schedules?.find((schedule) => schedule.id === selected);
	return (
		<div className="modal-overlay">
			<button
				type="button"
				className="modal-overlay-dismiss"
				onClick={onClose}
				aria-label="Close schedules"
			/>
			<div
				className="modal schedules-modal"
				role="dialog"
				aria-label="Schedules"
			>
				<header className="modal-header">
					<h2>
						<CalendarClock /> Schedules
					</h2>
					<button
						type="button"
						className="modal-close"
						onClick={onClose}
						aria-label="Close schedules"
					>
						<XMarkIcon />
					</button>
				</header>
				{!schedules ? (
					<p className="modal-loading">{error || "loading schedules..."}</p>
				) : (
					<div className="schedules-layout">
						<aside className="schedule-list">
							<button
								type="button"
								className={`schedule-new${selected === "" ? " active" : ""}`}
								onClick={() => select(null)}
							>
								<Plus /> New schedule
							</button>
							{schedules.length === 0 && (
								<p className="settings-hint">
									No schedules yet. A scheduled prompt runs on a timer as a job
									of its own, such as a nightly sweep that files issues.
								</p>
							)}
							<ul>
								{schedules.map((schedule) => (
									<li
										key={schedule.id}
										className={schedule.id === selected ? "active" : ""}
									>
										<label
											className="schedule-toggle"
											title={schedule.enabled ? "Disable" : "Enable"}
										>
											<input
												type="checkbox"
												role="switch"
												aria-checked={schedule.enabled}
												checked={schedule.enabled}
												onChange={() => toggle(schedule)}
												aria-label={`Enable ${schedule.name}`}
											/>
										</label>
										<button type="button" onClick={() => select(schedule)}>
											<strong>{schedule.name}</strong>
											<span>{describeTrigger(schedule.trigger)}</span>
											<span
												className={`schedule-last ${schedule.running ? "running" : (schedule.history?.at(-1)?.status ?? "")}`}
											>
												{lastRunLabel(schedule)}
												{schedule.enabled && schedule.next
													? ` · next ${formatRelative(schedule.next)}`
													: ""}
											</span>
										</button>
									</li>
								))}
							</ul>
						</aside>
						<ScheduleEditor
							key={selected || "new"}
							schedule={current}
							form={form}
							setForm={setForm}
							onSaved={replace}
							onDeleted={(id) => {
								const rest = schedules.filter((schedule) => schedule.id !== id);
								setSchedules(rest);
								select(rest[0]);
							}}
						/>
					</div>
				)}
				{schedules && error && <p className="modal-error">{error}</p>}
			</div>
		</div>
	);
}
