import { XMarkIcon } from "@heroicons/react/24/outline";
import { useEffect, useRef, useState } from "react";
import {
	clampPromptPanelWidth,
	loadPromptPanel,
	promptPanelBounds,
	savePromptPanel,
} from "./dashboard";

// mobileQuery matches the narrow-screen layout in index.css. There the prompt
// agent opens as a modal instead of a side panel (issue #828).
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
// and whether the window is narrow enough to show it as a modal (issue #828).
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

// PromptModal shows the prompt agent as a pop-up on a phone, where a side
// panel would leave no room for the dashboard. Like the panel, it stays
// mounted while closed so a half-written request survives.
export function PromptModal({ open, onClose, children }) {
	useEffect(() => {
		if (!open) return;
		const closeOnEscape = (event) => {
			if (event.key === "Escape") onClose();
		};
		window.addEventListener("keydown", closeOnEscape);
		return () => window.removeEventListener("keydown", closeOnEscape);
	}, [open, onClose]);
	return (
		<div
			id="prompt-panel"
			className="modal-overlay prompt-modal"
			hidden={!open}
		>
			<button
				type="button"
				className="modal-overlay-dismiss"
				onClick={onClose}
				aria-label="Close prompt agent"
			/>
			<div
				className="modal prompt-modal-dialog"
				role="dialog"
				aria-modal="true"
				aria-label="Prompt agent"
			>
				<button
					type="button"
					className="modal-close prompt-modal-close"
					onClick={onClose}
					aria-label="Close prompt agent"
				>
					<XMarkIcon />
				</button>
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
