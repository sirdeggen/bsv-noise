import "./fonts.css";
import "./style.css";
import { formatBSV, parseBSVToSatoshis } from "./amount";
import { createField } from "./field";
import { createFlame, type Visual } from "./flame";
import { fundService } from "./fund";
import type { Stats } from "./types";

if (import.meta.env.DEV) {
  void import("./agentation").then((m) => m.mountAgentation());
} else if ("serviceWorker" in navigator) {
  // Installable as a desktop app. Skipped in dev so it never caches Vite modules.
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").catch(() => undefined);
  });
}

const app = document.querySelector<HTMLDivElement>("#app");
if (!app) throw new Error("missing app");

// Panel icon: a few straws with one needle — the haystack in miniature.
const ICON = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 18 9 6M8 19l4-13M13 19l2-14M17 18l3-11"/><path class="needle" d="M5 9l14 6"/></svg>`;

const CAMERA = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 8h3l1.6-2.2h6.8L17 8h3a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z"/><circle cx="12" cy="13" r="3.6"/></svg>`;

const FOLDERS = ["status", "throughput", "wallet", "recent", "topup"] as const;
type Folder = (typeof FOLDERS)[number];
const DEFAULT_OPEN: Folder[] = [];

function folder(id: Folder, title: string, body: string): string {
  return `<details class="folder" data-folder="${id}">
    <summary><span class="label">${title}</span><span class="meta" id="meta-${id}"></span></summary>
    <div class="rows">${body}</div>
  </details>`;
}

app.innerHTML = `
  <canvas id="field" aria-hidden="true"></canvas>
  <div class="titlebar" aria-hidden="true"></div>
  <div class="dock" data-console="noise">
    <section class="panel" id="panel" aria-label="Noise controls">
      <p class="note" id="note">Connecting…</p>
      ${folder("status", "Status", `<div id="status-rows"></div>`)}
      ${folder("throughput", "Throughput", `<div id="tps-rows"></div>`)}
      ${folder("wallet", "Wallet", `<div id="wallet-rows"></div>`)}
      ${folder("recent", "Recent", `<div id="txs"></div>`)}
      ${folder("topup", "Top up", `
        <form id="topup">
          <div class="row text">
            <label for="amount">amount</label>
            <span class="ctl"><input id="amount" name="amount" type="text" inputmode="decimal" value="0.001" autocomplete="off" /><em>BSV</em></span>
          </div>
          <div class="row action"><button type="submit" id="review">Review top-up</button></div>
          <div id="reviewbox"></div>
        </form>
        <p class="fine">Privacy, not anonymity. Both parties know who they're dealing with, and traditional tracing still works.</p>
        <p class="fine" id="foot"></p>`)}
    </section>
    <div class="barwrap">
      <button type="button" class="bar" id="toggle" aria-controls="panel" aria-expanded="true" title="Hide controls (H)">
        <span class="icon">${ICON}</span>
        <span class="count" id="count" aria-label="payments added" title="Payments added — each one is a flare in the flame"></span>
        <span class="title">Noise</span>
        <span class="dot" id="dot"></span>
        <span class="hint" id="toggle-label">Hide</span>
      </button>
      <button type="button" class="help snap" id="snap" aria-label="Save this image" title="Save this image (S)">${CAMERA}</button>
      <button type="button" class="help" id="help" popovertarget="about" aria-label="Why run Noise?" title="Why run Noise? (?)"><span>?</span></button>
    </div>
  </div>
  <div class="flash" id="flash" aria-hidden="true"></div>
  <p class="toast" id="toast" role="status" aria-live="polite"></p>
  <div id="about" popover class="about" role="dialog" aria-labelledby="about-title">
    <header>
      <h2 id="about-title">Why run Noise?</h2>
      <button type="button" class="close" popovertarget="about" popovertargetaction="hide" aria-label="Close">×</button>
    </header>
    <div class="about-body">
      <p class="lead">Every BSV payment is public. What keeps yours private is the crowd around it: a payment that looks like thousands of others is hard to single out. <strong>Noise adds to that crowd, around the clock.</strong></p>

      <section class="impact" aria-live="polite">
        <div><b id="about-total">–</b><span>payments so far</span></div>
        <div><b id="about-hour">–</b><span>per hour</span></div>
        <div><b id="about-day">–</b><span>per day</span></div>
      </section>
      <p class="sim-note" id="about-sim" hidden>Dry run: these payments are simulated. Nothing reaches the chain until broadcast is on.</p>

      <h3>Cover for everyone, not just you</h3>
      <p>Noise's payments are built to look like ordinary commerce: everyday amounts, change outputs, irregular timing. Anyone on BSV whose payments look similar gets more cover, whether or not they've heard of Noise.</p>

      <h3>How it works</h3>
      <ol class="steps">
        <li><strong>Fund it.</strong> Top up through Message Box from any BRC&#8209;100 wallet.</li>
        <li><strong>It pays itself.</strong> At an uneven, human rhythm it sends small payments between keys it controls. The coins stay yours; only network fees are spent.</li>
        <li><strong>It can't run away.</strong> Daily caps on spending and fees, plus a reserve it never touches, mean a forgotten tab can't empty the wallet.</li>
        <li><strong>You see it.</strong> Each flare in the flame is one payment. Every few minutes the flame grows into a new form, shaped by the payments that fed it.</li>
      </ol>

      <h3>Privacy, not anonymity</h3>
      <p>The two sides of a real payment still know each other, and lawful tracing still works. Noise stops casual profiling of everyday spending. It won't hide anyone from an investigation, and it isn't meant to.</p>

      <h3>What it can't do</h3>
      <p>A determined analyst can trace Noise's payments back to a single operator. Noise makes the crowd bigger; it doesn't make anyone invisible. It's a bridge until everyday BSV use builds the crowd by itself.</p>

      <h3>Keep it running</h3>
      <p>The crowd only exists while the noise does. Leave Noise on as your screensaver, install it as an app, and top it up before the balance runs dry.</p>

      <dl class="keys" aria-label="Keyboard shortcuts">
        <div><dt>H</dt><dd>hide panel</dd></div>
        <div><dt>F</dt><dd>fullscreen</dd></div>
        <div><dt>S</dt><dd>save image</dd></div>
        <div><dt>?</dt><dd>this guide</dd></div>
      </dl>
    </div>
    <footer class="about-actions">
      <button type="button" id="about-topup">Top up</button>
      <button type="button" id="about-install" hidden>Install app</button>
    </footer>
  </div>
`;

const canvas = document.querySelector<HTMLCanvasElement>("#field")!;
// Live fractal flame; the 2D haystack is the fallback without WebGL2.
const visual: Visual = createFlame(canvas) ?? (() => {
  const field = createField(canvas);
  return {
    pulse: () => field.needle(),
    snapshot: () => new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/png")),
  };
})();
const statusRows = document.querySelector<HTMLDivElement>("#status-rows")!;
const tpsRows = document.querySelector<HTMLDivElement>("#tps-rows")!;
const walletRows = document.querySelector<HTMLDivElement>("#wallet-rows")!;
const dot = document.querySelector<HTMLSpanElement>("#dot")!;
const note = document.querySelector<HTMLParagraphElement>("#note")!;
const txs = document.querySelector<HTMLDivElement>("#txs")!;
const foot = document.querySelector<HTMLElement>("#foot")!;
const form = document.querySelector<HTMLFormElement>("#topup")!;
const reviewBox = document.querySelector<HTMLDivElement>("#reviewbox")!;
const amountInput = document.querySelector<HTMLInputElement>("#amount")!;
const toggle = document.querySelector<HTMLButtonElement>("#toggle")!;
const toggleLabel = document.querySelector<HTMLSpanElement>("#toggle-label")!;
const about = document.querySelector<HTMLDivElement>("#about")!;
const aboutTotal = document.querySelector<HTMLElement>("#about-total")!;
const aboutHour = document.querySelector<HTMLElement>("#about-hour")!;
const aboutDay = document.querySelector<HTMLElement>("#about-day")!;
const aboutSim = document.querySelector<HTMLElement>("#about-sim")!;
const aboutInstall = document.querySelector<HTMLButtonElement>("#about-install")!;

let latest: Stats | null = null;
let funding = false;

// --- Panel chrome -------------------------------------------------------------

const HIDDEN_KEY = "noise.v2.hud-hidden";
const FOLDERS_KEY = "noise.v2.folders";

function store(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable */
  }
}

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function setHidden(hidden: boolean): void {
  document.body.classList.toggle("hud-hidden", hidden);
  toggle.setAttribute("aria-expanded", String(!hidden));
  toggle.title = `${hidden ? "Show" : "Hide"} controls (H)`;
  toggle.setAttribute("aria-label", `${hidden ? "Show" : "Hide"} Noise controls`);
  toggleLabel.textContent = hidden ? "Show" : "Hide";
  store(HIDDEN_KEY, hidden ? "1" : "0");
}

toggle.addEventListener("click", () => setHidden(!document.body.classList.contains("hud-hidden")));
document.addEventListener("keydown", (event) => {
  const target = event.target as HTMLElement | null;
  if (target && (target.tagName === "INPUT" || target.isContentEditable)) return;
  if (event.key === "h" || event.key === "H") setHidden(!document.body.classList.contains("hud-hidden"));
  if (event.key === "f" || event.key === "F") toggleFullscreen();
  if (event.key === "?") about.togglePopover();
  if ((event.key === "s" || event.key === "S") && !event.metaKey && !event.ctrlKey) void saveSnapshot();
});

// --- Snapshot -----------------------------------------------------------------

const flash = document.querySelector<HTMLDivElement>("#flash")!;
const toast = document.querySelector<HTMLParagraphElement>("#toast")!;
let toastTimer = 0;

function say(message: string): void {
  toast.textContent = message;
  toast.classList.add("show");
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => toast.classList.remove("show"), 3200);
}

function snapshotName(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `noise-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}.png`;
}

interface SavePicker {
  showSaveFilePicker(options: {
    suggestedName: string;
    startIn?: string;
    types: { description: string; accept: Record<string, string[]> }[];
  }): Promise<{ createWritable(): Promise<{ write(data: Blob): Promise<void>; close(): Promise<void> }> }>;
}

let saving = false;
async function saveSnapshot(): Promise<void> {
  if (saving) return;
  saving = true;
  try {
    const blob = await visual.snapshot();
    if (!blob) throw new Error("Could not capture the image");
    flash.classList.remove("on");
    void flash.offsetWidth;
    flash.classList.add("on");
    const name = snapshotName();
    const picker = (window as unknown as Partial<SavePicker>).showSaveFilePicker;
    if (picker) {
      // Chrome / Edge: a real save dialog that opens on the Desktop.
      try {
        const handle = await picker.call(window, {
          suggestedName: name,
          startIn: "desktop",
          types: [{ description: "PNG image", accept: { "image/png": [".png"] } }],
        });
        const out = await handle.createWritable();
        await out.write(blob);
        await out.close();
        say(`Saved ${name}`);
      } catch (err) {
        if ((err as DOMException)?.name !== "AbortError") throw err;
      }
    } else {
      // Elsewhere: a regular download.
      const url = URL.createObjectURL(blob);
      const a = Object.assign(document.createElement("a"), { href: url, download: name });
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 10000);
      say(`Downloaded ${name}`);
    }
  } catch (err) {
    say(err instanceof Error ? err.message : "Could not save the image");
  } finally {
    saving = false;
  }
}

document.querySelector("#snap")!.addEventListener("click", () => void saveSnapshot());

// "Top up" from the explainer: close it, reveal the panel, open the folder.
document.querySelector("#about-topup")!.addEventListener("click", () => {
  about.hidePopover();
  setHidden(false);
  const folderEl = document.querySelector<HTMLDetailsElement>('details[data-folder="topup"]')!;
  folderEl.open = true;
  folderEl.scrollIntoView({ block: "nearest" });
  amountInput.focus();
});

// Offer "Install app" when the browser supports it and it isn't installed yet.
interface InstallPrompt extends Event { prompt(): Promise<void> }
let installPrompt: InstallPrompt | null = null;
window.addEventListener("beforeinstallprompt", (event) => {
  event.preventDefault();
  installPrompt = event as InstallPrompt;
  aboutInstall.hidden = false;
});
window.addEventListener("appinstalled", () => {
  installPrompt = null;
  aboutInstall.hidden = true;
});
aboutInstall.addEventListener("click", () => {
  void installPrompt?.prompt();
});

function toggleFullscreen(): void {
  if (document.fullscreenElement) void document.exitFullscreen();
  else void document.documentElement.requestFullscreen?.().catch(() => undefined);
}
document.querySelector("#field")!.addEventListener("dblclick", toggleFullscreen);
setHidden(read(HIDDEN_KEY) === "1");

let openFolders: Folder[] = DEFAULT_OPEN;
try {
  const saved = JSON.parse(read(FOLDERS_KEY) ?? "null");
  if (Array.isArray(saved)) openFolders = saved.filter((f): f is Folder => FOLDERS.includes(f));
} catch {
  /* keep defaults */
}
for (const el of Array.from(document.querySelectorAll<HTMLDetailsElement>("details.folder"))) {
  const id = el.dataset.folder as Folder;
  el.open = openFolders.includes(id);
  el.addEventListener("toggle", () => {
    const open = Array.from(document.querySelectorAll<HTMLDetailsElement>("details.folder"))
      .filter((d) => d.open)
      .map((d) => d.dataset.folder as Folder);
    store(FOLDERS_KEY, JSON.stringify(open));
    if (id === "topup" && el.open) amountInput.focus();
  });
}

let idleTimer = 0;
function wake(): void {
  document.body.classList.remove("idle");
  window.clearTimeout(idleTimer);
  idleTimer = window.setTimeout(() => {
    const busy = funding || document.activeElement === amountInput;
    if (!busy) document.body.classList.add("idle");
  }, 3500);
}
for (const type of ["pointermove", "pointerdown", "keydown", "focusin"]) {
  document.addEventListener(type, wake, { passive: true });
}
wake();

// --- Rendering ----------------------------------------------------------------

const seen = new Set<string>();
const countEl = document.querySelector<HTMLSpanElement>("#count")!;
let shownCount = 0;

function setCount(n: number, tick: boolean): void {
  countEl.textContent = n.toLocaleString();
  if (!tick) return;
  countEl.classList.remove("tick");
  void countEl.offsetWidth;
  countEl.classList.add("tick");
}
let primed = false;

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

/** Text value — green stripe. */
function textRow(label: string, value: string, cls = ""): string {
  return `<div class="row text"><span class="k">${escapeHtml(label)}</span><span class="v ${cls}">${escapeHtml(value)}</span></div>`;
}

/** Boolean — purple stripe, read-only checkbox. */
function boolRow(label: string, on: boolean): string {
  return `<div class="row bool"><span class="k">${escapeHtml(label)}</span><span class="check ${on ? "on" : ""}" role="img" aria-label="${on ? "on" : "off"}"></span></div>`;
}

/** Number with a slider-style bar — blue stripe. */
function barRow(label: string, value: number, max: number, digits = 2): string {
  const pct = max > 0 ? Math.max(0, Math.min(1, value / max)) * 100 : 0;
  return `<div class="row num"><span class="k">${escapeHtml(label)}</span><span class="slider"><i style="width:${pct.toFixed(1)}%"></i></span><span class="box">${value.toFixed(digits)}</span></div>`;
}

/** Plain number — blue stripe. */
function numRow(label: string, value: string): string {
  return `<div class="row num"><span class="k">${escapeHtml(label)}</span><span class="box wide">${escapeHtml(value)}</span></div>`;
}

function render(stats: Stats): void {
  latest = stats;
  const health = stats.ready ? stats.health : `paused${stats.paused ? `: ${stats.paused}` : ""}`;
  dot.className = `dot ${stats.ready ? "on" : "paused"}`;
  dot.title = health;
  note.textContent = stats.ready ? "" : health;

  statusRows.innerHTML = [
    textRow("network", `${stats.network}net`),
    textRow("mode", stats.dry_run ? "dry-run" : "live", stats.dry_run ? "" : "hot"),
    textRow("health", health, stats.ready ? "ok" : "warn"),
    boolRow("broadcast", stats.broadcast),
    boolRow("import", stats.import_funding),
  ].join("");

  const t = stats.tps;
  const scale = Math.max(t.target * 2, t.attempted, 0.01);
  tpsRows.innerHTML = [
    `<div class="row hero"><span class="k">payments</span><b>${stats.transactions.total.toLocaleString()}</b></div>`,
    barRow("target", t.target, scale),
    barRow("attempted", t.attempted, scale),
    barRow("accepted", t.accepted, scale),
    barRow("confirmed", t.confirmed, scale),
    numRow("in flight", String(stats.transactions.in_flight)),
    numRow("failed", String(stats.transactions.failed)),
  ].join("");

  const sweep = stats.funding.last_sweep
    ? new Date(stats.funding.last_sweep).toLocaleTimeString()
    : "never";
  walletRows.innerHTML = [
    numRow("balance", `${stats.balance_bsv} BSV`),
    numRow("sats", stats.balance_satoshis.toLocaleString()),
    numRow("queue", `${stats.funding.pending} pending · ${stats.funding.acknowledged} ack`),
    textRow("last sweep", stats.funding.last_error || sweep, stats.funding.last_error ? "warn" : ""),
  ].join("");

  const fresh: Stats["recent"] = [];
  for (const tx of [...stats.recent].reverse()) {
    if (seen.has(tx.id)) continue;
    seen.add(tx.id);
    fresh.push(tx);
  }
  // On first load only replay a few, so the screen doesn't open in a flash.
  const show = (primed ? fresh : fresh.slice(-3)).slice(-12);
  // The counter lands on each payment together with its flare.
  shownCount = Math.max(shownCount, stats.transactions.total - show.length);
  setCount(shownCount, false);
  show.forEach((tx, i) => window.setTimeout(() => {
    visual.pulse(tx.id, tx.amount_satoshis);
    setCount(++shownCount, true);
  }, i * 220));
  primed = true;
  if (seen.size > 500) {
    const keep = new Set(stats.recent.map((tx) => tx.id));
    for (const id of seen) if (!keep.has(id)) seen.delete(id);
  }

  txs.innerHTML = stats.recent.length === 0
    ? `<div class="row text"><span class="k">none yet</span></div>`
    : stats.recent
      .slice(0, 5)
      .map((tx) => `<div class="row tx">
        <span class="k">${escapeHtml(new Date(tx.created).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }))}</span>
        <span class="amt">${tx.amount_satoshis.toLocaleString()}</span>
        ${txCell(tx)}
      </div>`)
      .join("");
  const latestTx = stats.recent[0];
  meta("status", `${stats.dry_run ? "dry-run" : "live"} · ${stats.network}net`, stats.ready ? "ok" : "warn");
  meta("throughput", `${stats.transactions.total.toLocaleString()} · ${stats.tps.accepted.toFixed(2)} tps`);
  meta("wallet", `${stats.balance_bsv} BSV`);
  meta("recent", latestTx ? `${latestTx.amount_satoshis.toLocaleString()} sats · ${ago(latestTx.created)}` : "none yet");
  meta("topup", "PeerPay");
  const perSecond = stats.tps.accepted > 0 ? stats.tps.accepted : stats.tps.target;
  const totalText = stats.transactions.total.toLocaleString();
  if (aboutTotal.textContent !== totalText && aboutTotal.textContent !== "–") {
    aboutTotal.classList.remove("tick");
    void aboutTotal.offsetWidth;
    aboutTotal.classList.add("tick");
  }
  aboutTotal.textContent = totalText;
  aboutHour.textContent = `~${Math.round(perSecond * 3600).toLocaleString()}`;
  aboutDay.textContent = `~${Math.round(perSecond * 86400).toLocaleString()}`;
  aboutSim.hidden = !stats.dry_run;
  foot.textContent = `Identity ${stats.identity_key} · ${stats.message_box} @ ${stats.message_box_url}`;
}

function meta(id: Folder, text: string, tone = ""): void {
  const el = document.querySelector<HTMLSpanElement>(`#meta-${id}`);
  if (!el) return;
  el.textContent = text;
  el.className = `meta ${tone}`;
}

function ago(iso: string): string {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}

function txCell(tx: Stats["recent"][number]): string {
  const id = tx.txid || tx.id;
  const short = id.length > 12 ? `${id.slice(0, 5)}…${id.slice(-5)}` : id;
  if (tx.dry_run || !tx.explorer_url) {
    return `<span class="id sim" title="${escapeHtml(id)} · simulated">${escapeHtml(short)}</span>`;
  }
  return `<a class="id" href="${escapeHtml(tx.explorer_url)}" target="_blank" rel="noopener" title="${escapeHtml(tx.txid)}">${escapeHtml(short)}</a>`;
}

async function load(): Promise<void> {
  const response = await fetch("/api/stats", { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`stats ${response.status}`);
  render((await response.json()) as Stats);
}

form.addEventListener("submit", (event) => {
  event.preventDefault();
  if (!latest || funding) return;
  let sats: number;
  try {
    sats = parseBSVToSatoshis(amountInput.value);
  } catch (err) {
    reviewBox.innerHTML = `<p class="err">${escapeHtml(err instanceof Error ? err.message : "Invalid amount")}</p>`;
    return;
  }
  const bsv = formatBSV(sats);
  reviewBox.innerHTML = `
    <div class="review">
      <p>Send <strong>${escapeHtml(bsv)} BSV</strong> (${sats.toLocaleString()} sats) from your local BRC-100 wallet.</p>
      <p>Recipient identity <span class="sim">${escapeHtml(latest.identity_key)}</span></p>
      <p>Message Box <span class="sim">${escapeHtml(latest.message_box)}</span> on ${escapeHtml(latest.message_box_url)}</p>
      <p class="${latest.import_funding ? "ok" : "warn"}">${latest.import_funding
        ? "The daemon is importing funding and will pick this up on the next sweep."
        : "Import is off, so this payment will sit in the Message Box until IMPORT_FUNDING is enabled."}</p>
      <button type="button" id="send">Send from my wallet</button>
      <button type="button" class="secondary" id="cancel">Cancel</button>
    </div>`;
  reviewBox.querySelector<HTMLButtonElement>("#cancel")?.addEventListener("click", () => {
    reviewBox.innerHTML = "";
  });
  reviewBox.querySelector<HTMLButtonElement>("#send")?.addEventListener("click", () => {
    void send(sats, latest!.balance_satoshis, latest!.funding.acknowledged);
  });
});

async function send(sats: number, balanceBefore: number, ackedBefore: number): Promise<void> {
  if (!latest) return;
  funding = true;
  reviewBox.innerHTML = `<p>Waiting for your wallet…</p>`;
  try {
    const txid = await fundService({
      identityKey: latest.identity_key,
      messageBox: latest.message_box,
      messageBoxURL: latest.message_box_url,
      originator: latest.originator,
      satoshis: sats,
    });
    const net = latest.network === "main" ? "https://whatsonchain.com/tx/" : "https://test.whatsonchain.com/tx/";
    reviewBox.innerHTML = `<p class="ok">Sent. Txid <a href="${net}${txid}" target="_blank" rel="noopener">${txid}</a>. Watching for the daemon to internalize it.</p>`;
    await watch(balanceBefore, ackedBefore, txid);
  } catch (err) {
    reviewBox.innerHTML = `<p class="err">${escapeHtml(err instanceof Error ? err.message : "Top-up failed")}</p>`;
  } finally {
    funding = false;
  }
}

async function watch(balanceBefore: number, ackedBefore: number, txid: string): Promise<void> {
  for (let i = 0; i < 15; i++) {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    try {
      await load();
    } catch {
      continue;
    }
    if (!latest) return;
    if (latest.balance_satoshis > balanceBefore || latest.funding.acknowledged > ackedBefore) {
      reviewBox.insertAdjacentHTML("beforeend", `<p class="ok">Balance is now ${escapeHtml(latest.balance_bsv)} BSV. Funding tx ${escapeHtml(txid)}.</p>`);
      return;
    }
  }
  reviewBox.insertAdjacentHTML("beforeend", `<p class="warn">The tx is sent. The service balance has not moved yet. If import is on, the next sweep still has to accept it.</p>`);
}

async function tick(): Promise<void> {
  try {
    await load();
  } catch (err) {
    note.textContent = err instanceof Error ? err.message : "Unable to load stats";
  }
}

void tick();
setInterval(() => {
  if (!funding) void tick();
}, 2000);
