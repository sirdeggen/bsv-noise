// Dev-only: mounts the Agentation annotation toolbar in its own React root.
// Loaded via dynamic import behind import.meta.env.DEV, so it is never part
// of the production single-file build embedded in the Go binary.
import { createElement } from "react";
import { createRoot } from "react-dom/client";
import { Agentation } from "agentation";

export function mountAgentation(): void {
  const host = document.createElement("div");
  host.id = "agentation-root";
  document.body.appendChild(host);
  createRoot(host).render(createElement(Agentation));
}
