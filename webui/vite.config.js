import { defineConfig } from "vite";

export default defineConfig({
	clearScreen: false,
	// Vitest runs on its own Vite, which transforms with esbuild, and renders
	// components too (issue #767), so it needs the build's automatic JSX.
	esbuild: { jsx: "automatic" },
});
