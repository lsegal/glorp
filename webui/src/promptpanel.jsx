import { XMarkIcon } from "@heroicons/react/24/outline";
import { useEffect, useRef, useState } from "react";
import {
	clampPromptPanelWidth,
	loadPromptPanel,
	promptPanelBounds,
	savePromptPanel,
} from "./dashboard";

// mobileQuery matches the narrow-screen layout in index.css. There the prompt
// agent opens as a bottom drawer instead of a side panel (issue #834).
export const mobileQuery = "(max-width: 760px)";

const storage = () => {
	try {
		return window.localStorage;
	} catch {
		return null;
	}
};

// usePromptPanel holds the prompt agent panel's open state and width,
// restoring them from this browser's storage and remembering every change,
// and whether the window is narrow enough to show it as a bottom drawer
// (issue #834).
export function usePromptPanel() {
	const [panel, setPanel] = useState(() => {
		const stored = loadPromptPanel(storage());
		return {
			...stored,
			width: clampPromptPanelWidth(stored.width, window.innerWidth),
		};
	});
	useEffect(() => savePromptPanel(storage(), panel), [panel]);
	// A smaller window can leave the stored width past the panel's bounds.
	useEffect(() => {
		const fit = () =>
			setPanel((current) => {
				const width = clampPromptPanelWidth(current.width, window.innerWidth);
				return width === current.width ? current : { ...current, width };
			});
		window.addEventListener("resize", fit);
		return () => window.removeEventListener("resize", fit);
	}, []);
	const [mobile, setMobile] = useState(
		() => window.matchMedia?.(mobileQuery).matches ?? false,
	);
	useEffect(() => {
		const query = window.matchMedia?.(mobileQuery);
		if (!query) return;
		const change = () => setMobile(query.matches);
		query.addEventListener("change", change);
		return () => query.removeEventListener("change", change);
	}, []);
	const toggle = () =>
		setPanel((current) => ({ ...current, open: !current.open }));
	const resize = (width) =>
		setPanel((current) => ({
			...current,
			width: clampPromptPanelWidth(width, window.innerWidth),
		}));
	const close = () => setPanel((current) => ({ ...current, open: false }));
	return { panel, mobile, toggle, close, resize };
}

// drawerDismissDistance is how far, in pixels, the drawer has to be dragged
// down before letting go closes it rather than snapping it back.
export const drawerDismissDistance = 96;

// shouldDismissDrawer reports whether a downward drag of distance pixels on a
// drawer of the given height closes it: past drawerDismissDistance, or past a
// quarter of a short drawer's height.
export function shouldDismissDrawer(distance, height) {
	if (!(distance > 0)) return false;
	return (
		distance >= drawerDismissDistance || (height > 0 && distance >= height / 4)
	);
}

// PromptDrawer shows the prompt agent as a bottom drawer on a phone, where a
// side panel would leave no room for the dashboard (issue #834). It slides up
// over the dashboard and closes with its X button, a tap on the dimmed
// dashboard, Escape, or a swipe down on its handle. Like the panel, it stays
// mounted while closed so a half-written request survives.
export function PromptDrawer({ open, onClose, children }) {
	const [drag, setDrag] = useState(0);
	const start = useRef(null);
	useEffect(() => {
		if (!open) return;
		const closeOnEscape = (event) => {
			if (event.key === "Escape") onClose();
		};
		window.addEventListener("keydown", closeOnEscape);
		return () => window.removeEventListener("keydown", closeOnEscape);
	}, [open, onClose]);
	const begin = (event) => {
		if (event.target.closest?.("button")) return;
		event.currentTarget.setPointerCapture?.(event.pointerId);
		const sheet = event.currentTarget.closest(".prompt-drawer-sheet");
		start.current = { y: event.clientY, height: sheet?.offsetHeight ?? 0 };
	};
	const move = (event) => {
		if (!start.current) return;
		setDrag(Math.max(0, event.clientY - start.current.y));
	};
	const end = (event) => {
		if (!start.current) return;
		const distance = event.clientY - start.current.y;
		const dismiss =
			event.type === "pointerup" &&
			shouldDismissDrawer(distance, start.current.height);
		start.current = null;
		setDrag(0);
		if (dismiss) onClose();
	};
	return (
		<div
			id="prompt-panel"
			className={`prompt-drawer${drag > 0 ? " dragging" : ""}`}
			hidden={!open}
		>
			<button
				type="button"
				className="prompt-drawer-backdrop"
				onClick={onClose}
				aria-label="Close prompt agent"
				tabIndex={-1}
			/>
			<div
				className="prompt-drawer-sheet"
				role="dialog"
				aria-modal="true"
				aria-label="Prompt agent"
				style={drag > 0 ? { transform: `translateY(${drag}px)` } : undefined}
			>
				<div
					className="prompt-drawer-grip"
					onPointerDown={begin}
					onPointerMove={move}
					onPointerUp={end}
					onPointerCancel={end}
				>
					<span className="prompt-drawer-handle" aria-hidden="true" />
					<button
						type="button"
						className="prompt-drawer-close"
						onClick={onClose}
						aria-label="Close prompt agent"
					>
						<XMarkIcon />
					</button>
				</div>
				{children}
			</div>
		</div>
	);
}

// PromptPanel docks the prompt agent to the right edge of the window. It
// stays mounted while closed so a half-written request survives, and its
// left edge drags, or takes arrow keys, to resize it.
export function PromptPanel({ open, width, onResize, children }) {
	const [dragging, setDragging] = useState(false);
	const start = useRef(null);
	const { min, max } = promptPanelBounds(
		typeof window === "undefined" ? 1280 : window.innerWidth,
	);
	const begin = (event) => {
		event.preventDefault();
		event.currentTarget.setPointerCapture?.(event.pointerId);
		start.current = { x: event.clientX, width };
		setDragging(true);
	};
	const move = (event) => {
		if (!start.current) return;
		onResize(start.current.width + start.current.x - event.clientX);
	};
	const end = () => {
		start.current = null;
		setDragging(false);
	};
	const keys = (event) => {
		const step = event.shiftKey ? 64 : 16;
		if (event.key === "ArrowLeft") onResize(width + step);
		else if (event.key === "ArrowRight") onResize(width - step);
		else if (event.key === "Home") onResize(max);
		else if (event.key === "End") onResize(min);
		else return;
		event.preventDefault();
	};
	return (
		<aside
			id="prompt-panel"
			className={`prompt-panel${dragging ? " dragging" : ""}`}
			hidden={!open}
		>
			{/* biome-ignore lint/a11y/useSemanticElements: an <hr> cannot be dragged or focused as a resize handle. */}
			<div
				className="prompt-panel-resizer"
				role="separator"
				aria-orientation="vertical"
				aria-label="Resize prompt agent panel"
				aria-valuemin={min}
				aria-valuemax={max}
				aria-valuenow={width}
				tabIndex={0}
				onPointerDown={begin}
				onPointerMove={move}
				onPointerUp={end}
				onPointerCancel={end}
				onKeyDown={keys}
			/>
			{children}
		</aside>
	);
}
