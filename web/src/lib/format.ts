// Display formatting (spec/ui.md): numbers right-aligned, 2 decimals, USD
// without symbol, negatives in parentheses; percentages with one decimal and
// an explicit sign; byte sizes in decimal units like the BigQuery console.

const MINUS = '−'; // U+2212, matches the spec's "−3.1 %"

function round2(n: number): number {
  // Round half away from zero so negatives mirror positives.
  const r = (Math.sign(n) * Math.round(Math.abs(n) * 100)) / 100;
  return r === 0 ? 0 : r;
}

export function formatMoney(n: number, opts: { grouped?: boolean } = {}): string {
  const r = round2(n);
  const abs = Math.abs(r);
  const text = opts.grouped
    ? abs.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
    : abs.toFixed(2);
  return r < 0 ? `(${text})` : text;
}

/** Signed money for deltas: `+423.89` / `−50.00`. */
export function formatSignedMoney(n: number): string {
  const r = round2(n);
  const text = Math.abs(r).toFixed(2);
  if (r > 0) return `+${text}`;
  if (r < 0) return `${MINUS}${text}`;
  return text;
}

export function formatPct(n: number | null): string {
  if (n === null) return '—';
  const r = (Math.sign(n) * Math.round(Math.abs(n) * 10)) / 10;
  const text = `${Math.abs(r).toFixed(1)} %`;
  if (r > 0) return `+${text}`;
  if (r < 0) return `${MINUS}${text}`;
  return text;
}

/** `▲ +423.89 (+25.4 %)`; the pct part is omitted when null. */
export function formatDelta(deltaAbs: number, deltaPct: number | null): string {
  const arrow = deltaAbs < 0 ? '▼' : '▲';
  const base = `${arrow} ${formatSignedMoney(deltaAbs)}`;
  return deltaPct === null ? base : `${base} (${formatPct(deltaPct)})`;
}

export function formatBytes(bytes: number | null): string {
  if (bytes === null) return '—';
  if (bytes < 1000) return `${bytes} B`;
  if (bytes < 1_000_000) return `${(bytes / 1000).toFixed(1)} KB`;
  if (bytes < 1_000_000_000) return `${(bytes / 1_000_000).toFixed(1)} MB`;
  return `${(bytes / 1_000_000_000).toFixed(2)} GB`;
}

export function formatDurationMs(ms: number | null): string {
  if (ms === null) return '—';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

export function formatInteger(n: number | null): string {
  if (n === null) return '—';
  return n.toLocaleString('en-US', { maximumFractionDigits: 0 });
}

/** Threshold values print without trailing zeros (20, 2.5). */
export function formatThreshold(n: number): string {
  return Number.isFinite(n) ? String(n) : '—';
}

/** `09:05Z` from an RFC3339 UTC timestamp; the input is returned when unparsable. */
export function formatTimeZ(ts: string | null): string {
  if (ts === null) return '—';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return `${d.toISOString().slice(11, 16)}Z`;
}

/** `2026-10-26 09:01:10Z`. */
export function formatTimestamp(ts: string | null): string {
  if (ts === null) return '—';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return `${d.toISOString().slice(0, 10)} ${d.toISOString().slice(11, 19)}Z`;
}
