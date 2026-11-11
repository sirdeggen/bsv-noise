// Dev-only entry for og.html: a seeded flame behind the wordmark.
import "./fonts.css";
import { createFlame } from "./flame";

const style = document.createElement("style");
style.textContent = `
  html, body { margin: 0; width: 1200px; height: 630px; overflow: hidden; background: #050505; }
  #field { position: fixed; inset: 0; width: 1200px; height: 630px; transform: translateX(250px); }
  .shade {
    position: fixed; inset: 0;
    background:
      linear-gradient(90deg, rgba(5,5,5,0.92) 0%, rgba(5,5,5,0.6) 34%, rgba(5,5,5,0) 62%),
      linear-gradient(0deg, rgba(5,5,5,0.7) 0%, rgba(5,5,5,0) 40%);
  }
  main { position: fixed; left: 72px; bottom: 68px; color: #eee; }
  .kicker { margin: 0 0 18px; font: 500 20px "Geist Mono", monospace; letter-spacing: 0.14em; text-transform: uppercase; color: #d67a52; }
  h1 { margin: 0; font: 400 168px/0.9 "Geist Pixel Square", monospace; letter-spacing: -0.02em; }
  .tag { margin: 26px 0 0; font: 400 26px/1.4 "Geist Mono", monospace; color: rgba(238,238,238,0.78); }
`;
document.head.appendChild(style);

const seed = Number(new URLSearchParams(location.search).get("seed") ?? 4);
const visual = createFlame(document.querySelector<HTMLCanvasElement>("#field")!, { seed });
if (!visual) document.body.dataset.error = "webgl2";
// A couple of payments for a little colour.
window.setTimeout(() => visual?.pulse(`og-${seed}-a`, 120000), 3500);
window.setTimeout(() => visual?.pulse(`og-${seed}-b`, 40000), 4200);
document.fonts.ready.then(() => window.setTimeout(() => (document.body.dataset.ready = "1"), 5200));
