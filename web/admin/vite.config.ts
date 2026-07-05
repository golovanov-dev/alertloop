import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Relative base + hash routing means the same build works whether it is served
// from the Go binary at /admin/ or standalone at /. Output goes into the Go
// package so it can be embedded into the binary.
export default defineConfig({
  base: "./",
  plugins: [react()],
  build: {
    outDir: "../../internal/adminui/dist",
    emptyOutDir: true,
  },
  server: {
    port: 5273,
    // Dev proxy so `npm run dev` talks to a locally running AlertLoop API.
    proxy: {
      "/v1": "http://localhost:8080",
      "/health": "http://localhost:8080",
      "/ready": "http://localhost:8080",
    },
  },
});
