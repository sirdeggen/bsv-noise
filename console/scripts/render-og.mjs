// Captures the Open Graph card from the running dev server (og.html).
//   npm run dev            # in another terminal
//   npm run og -- --seed 4 [--out public/og.jpg] [--url http://127.0.0.1:5173]
// Uses an installed Chrome; set CHROME_PATH to override.
import { chromium } from "playwright-core";

const arg = (name, fallback) => {
  const i = process.argv.indexOf(`--${name}`);
  return i > 0 ? process.argv[i + 1] : fallback;
};
const seed = arg("seed", "4");
const out = arg("out", "public/og.jpg");
const base = arg("url", "http://127.0.0.1:5173");
const executablePath = process.env.CHROME_PATH ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const browser = await chromium.launch({ executablePath, headless: true, args: ["--ignore-gpu-blocklist", "--enable-gpu"] });
const page = await browser.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
await page.goto(`${base}/og.html?seed=${encodeURIComponent(seed)}`, { waitUntil: "load" });
await page.waitForSelector("body[data-ready]", { timeout: 20000 });
// Let the flame converge.
await page.waitForTimeout(3000);
if (await page.$("body[data-error]")) throw new Error("WebGL2 unavailable in this browser");
await page.screenshot({ path: out, type: out.endsWith(".jpg") ? "jpeg" : "png", ...(out.endsWith(".jpg") ? { quality: 90 } : {}) });
await browser.close();
console.log(`wrote ${out} (seed ${seed})`);
