import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@agentkit/chat-ui": path.resolve(__dirname, "../../web/src/index.ts"),
    },
    // CRITICAL: dedupe React/MUI so the aliased source and the app share ONE copy.
    // Two React copies → "invalid hook call"; two emotion caches → broken styles.
    // Also dedupe packages used by chat-ui source but installed in examples/web.
    dedupe: [
      "react",
      "react-dom",
      "@mui/material",
      "@mui/icons-material",
      "@emotion/react",
      "@emotion/styled",
      "react-markdown",
      "remark-gfm",
      "prism-react-renderer",
      "@untitledui/file-icons",
    ],
  },
});
