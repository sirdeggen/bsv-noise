/** Parse a BSV decimal into integer satoshis. At most 8 decimal places. */
export function parseBSVToSatoshis(raw: string): number {
  const text = raw.trim();
  if (!/^\d+(\.\d{1,8})?$/.test(text)) {
    throw new Error("Use a positive BSV amount with at most 8 decimal places.");
  }
  const [whole, frac = ""] = text.split(".");
  const sats = Number(whole) * 100_000_000 + Number((frac + "00000000").slice(0, 8));
  if (!Number.isSafeInteger(sats) || sats <= 0) {
    throw new Error("Amount must be at least 1 satoshi.");
  }
  return sats;
}

export function formatBSV(sats: number): string {
  const sign = sats < 0 ? "-" : "";
  const abs = Math.abs(Math.trunc(sats));
  const whole = Math.floor(abs / 100_000_000);
  const frac = String(abs % 100_000_000).padStart(8, "0");
  return `${sign}${whole}.${frac}`;
}
