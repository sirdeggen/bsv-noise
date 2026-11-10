import "./style.css";
import { formatBSV, parseBSVToSatoshis } from "./amount";
import { fundService } from "./fund";
import type { Stats } from "./types";

const app = document.querySelector<HTMLDivElement>("#app");
if (!app) throw new Error("missing app");

app.innerHTML = `
  <header>
    <div>
      <p class="kicker" data-console="noise">Covering fire</p>
      <h1>Noise</h1>
    </div>
    <div class="flags" id="flags"></div>
  </header>
  <p class="note" id="note">Loading the daemon…</p>
  <section class="grid" id="cards"></section>
  <section class="split">
    <div class="panel">
      <h2>Recent payments</h2>
      <div id="txs">No payments yet.</div>
    </div>
    <div class="panel">
      <h2>Top up the service</h2>
      <p class="note">Send a payment from your local wallet to this service's Message Box identity.</p>
      <form id="topup">
        <label for="amount">Amount (BSV)</label>
        <input id="amount" name="amount" type="text" inputmode="decimal" value="0.001" autocomplete="off" />
        <button type="submit" id="review">Review top-up</button>
        <div id="reviewbox"></div>
      </form>
    </div>
  </section>
  <footer id="foot"></footer>
`;

const flags = document.querySelector<HTMLDivElement>("#flags")!;
const note = document.querySelector<HTMLParagraphElement>("#note")!;
const cards = document.querySelector<HTMLElement>("#cards")!;
const txs = document.querySelector<HTMLDivElement>("#txs")!;
const foot = document.querySelector<HTMLElement>("#foot")!;
const form = document.querySelector<HTMLFormElement>("#topup")!;
const reviewBox = document.querySelector<HTMLDivElement>("#reviewbox")!;
const amountInput = document.querySelector<HTMLInputElement>("#amount")!;

let latest: Stats | null = null;
let funding = false;

function pill(label: string, value: string): string {
  return `<span class="pill">${label} <strong>${escapeHtml(value)}</strong></span>`;
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function render(stats: Stats): void {
  latest = stats;
  const health = stats.ready ? stats.health : `paused${stats.paused ? `: ${stats.paused}` : ""}`;
  flags.innerHTML = [
    pill("network", stats.network),
    pill("mode", stats.dry_run ? "dry-run" : "live"),
    pill("health", health),
    pill("import", stats.import_funding ? "on" : "off"),
  ].join("");
  note.textContent = stats.note;
  const sweep = stats.funding.last_sweep
    ? new Date(stats.funding.last_sweep).toLocaleString()
    : "no sweep yet";
  cards.innerHTML = [
    card("Transactions", String(stats.transactions.total), `${stats.transactions.broadcast} broadcast · ${stats.transactions.dry_run} dry-run`),
    card("Balance", stats.balance_bsv + " BSV", `${stats.balance_satoshis.toLocaleString()} sats`),
    card("TPS", stats.tps.accepted.toFixed(2), `${stats.tps.state} · target ${stats.tps.target}`),
    card("Variance", stats.tps.interval_variance_seconds2.toFixed(3), `s² between attempts · model ${stats.tps.exponential_variance_seconds2.toFixed(3)}`),
    card("Confirmed", String(stats.transactions.confirmed), `${stats.transactions.seen} seen · ${stats.transactions.in_flight} in flight · ${stats.transactions.failed} failed`),
    card("Funding queue", String(stats.funding.pending), `${stats.funding.acknowledged} acknowledged`),
    card("Last sweep", sweep, stats.funding.last_error || "Message Box import"),
    card("Attempted TPS", stats.tps.attempted.toFixed(2), `confirmed ${stats.tps.confirmed.toFixed(2)}`),
  ].join("");
  if (stats.recent.length === 0) {
    txs.textContent = "No payments yet.";
  } else {
    txs.innerHTML = `<table>
      <thead><tr><th>When</th><th>Amount</th><th>State</th><th>Txid</th></tr></thead>
      <tbody>
        ${stats.recent.map((tx) => `<tr>
          <td>${escapeHtml(new Date(tx.created).toLocaleTimeString())}</td>
          <td>${tx.amount_satoshis.toLocaleString()}</td>
          <td>${escapeHtml(tx.state)}</td>
          <td>${txCell(tx)}</td>
        </tr>`).join("")}
      </tbody>
    </table>`;
  }
  foot.textContent = `Identity ${stats.identity_key} · ${stats.message_box} @ ${stats.message_box_url}`;
}

function card(label: string, value: string, detail: string): string {
  return `<article class="card"><span>${escapeHtml(label)}</span><b>${escapeHtml(value)}</b><em>${escapeHtml(detail)}</em></article>`;
}

function txCell(tx: Stats["recent"][number]): string {
  const id = tx.txid || tx.id;
  if (tx.dry_run || !tx.explorer_url) {
    return `<span class="sim">${escapeHtml(id)} · simulated</span>`;
  }
  return `<a href="${escapeHtml(tx.explorer_url)}" target="_blank" rel="noopener">${escapeHtml(tx.txid)}</a>`;
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
