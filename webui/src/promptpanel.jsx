import { useEffect, useRef, useState } from "react";
import {
	clampPromptPanelWidth,
	loadPromptPanel,
	promptPanelBounds,
	savePromptPanel,
} from "./dashboard";

const storage = () => {
	try {
		return window.localStorage;
	} catch {
		return null;
	}
};

// usePromptPanel holds the prompt agent panel's open state and width,
// restoring them from this browser's storage and remembering every change
// (issue #828).
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
	const toggle = () =>
		setPanel((current) => ({ ...current, open: !current.open }));
	const resize = (width) =>
		setPanel((current) => ({
			...current,
			width: clampPromptPanelWidth(width, window.innerWidth),
		}));
	return { panel, toggle, resize };
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
