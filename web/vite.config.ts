import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [svelte()],
  // dist/ holds the tracked .gitkeep that `go:embed all:dist` needs, so Vite must not empty it;
  // `make web` clears the old build itself.
  build: { outDir: "dist", emptyOutDir: false },
});
