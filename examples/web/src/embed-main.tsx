// Entry point for embed.html — the second Vite entry (see vite.config.ts).
// Kept separate from main.tsx so the embed bundle carries none of the shell:
// no login, no project picker, no sidebar.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import EmbedSession, { takeTokenFromHash } from "./EmbedSession.js";
import "./fonts.css";

// The fragment is read HERE, before React mounts, because the read is
// destructive (it erases the hash) and StrictMode double-invokes effects — a
// read inside the component would find the fragment already gone on the second
// pass and replace the captured token with "". From here on the token exists
// only as a prop and a closure: never storage, never a URL.
const token = takeTokenFromHash();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <EmbedSession token={token} />
  </StrictMode>
);
