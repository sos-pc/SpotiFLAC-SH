import path from "path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
// Set by the Docker build (see Dockerfile's frontend-builder stage, fed by
// CI's docker/metadata-action `version` output) — "dev" for any build that
// doesn't go through that pipeline (local `bun run build`/`dev`), so an
// unversioned build is honestly labeled instead of showing a fake version.
const appVersion = process.env.APP_VERSION || "dev";
export default defineConfig({
    plugins: [react(), tailwindcss()],
    resolve: {
        alias: {
            "@": path.resolve(__dirname, "./src"),
        },
    },
    define: {
        __APP_VERSION__: JSON.stringify(appVersion),
    },
});
