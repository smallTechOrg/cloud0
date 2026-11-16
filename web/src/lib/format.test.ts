import { describe, expect, it } from 'vitest';
import {
  formatBytes,
  formatDelta,
  formatDurationMs,
  formatInteger,
  formatMoney,
  formatSignedMoney,
  formatPct,
  formatThreshold,
  formatTimeZ,
  formatTimestamp,
} from './format';

describe('formatMoney', () => {
  it('renders INR by default with en-IN grouping, 2 decimals and negatives in parentheses', () => {
    expect(formatMoney(1191.69)).toBe('₹1,191.69');
    expect(formatMoney(0)).toBe('₹0.00');
    expect(formatMoney(-193.51)).toBe('(₹193.51)');
    expect(formatMoney(-0.001)).toBe('₹0.00');
  });
  it('uses lakh/crore grouping for INR', () => {
    expect(formatMoney(1234567.5, 'INR')).toBe('₹12,34,567.50');
  });
  it('follows the configured currency', () => {
    expect(formatMoney(1191.69, 'USD')).toContain('1,191.69');
    expect(formatMoney(1191.69, 'USD')).not.toContain('₹');
    expect(formatMoney(5, 'EUR')).toContain('€');
  });
});

describe('formatSignedMoney', () => {
  it('prefixes the sign before the symbol', () => {
    expect(formatSignedMoney(50)).toBe('+₹50.00');
    expect(formatSignedMoney(-50)).toBe('−₹50.00');
    expect(formatSignedMoney(0)).toBe('₹0.00');
  });
});

describe('formatPct', () => {
  it('shows one decimal with an explicit sign', () => {
    expect(formatPct(25.36)).toBe('+25.4 %');
    expect(formatPct(-3.1)).toBe('−3.1 %');
    expect(formatPct(0)).toBe('0.0 %');
    expect(formatPct(null)).toBe('—');
  });
});

describe('formatDelta', () => {
  it('shows direction marker, abs and pct', () => {
    expect(formatDelta(423.89, 25.36)).toBe('▲ +₹423.89 (+25.4 %)');
    expect(formatDelta(-50, -10)).toBe('▼ −₹50.00 (−10.0 %)');
    expect(formatDelta(80, null)).toBe('▲ +₹80.00');
  });
});

describe('formatBytes', () => {
  it('uses decimal units like the BigQuery console', () => {
    expect(formatBytes(41943040)).toBe('41.9 MB');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1_500_000_000)).toBe('1.50 GB');
    expect(formatBytes(null)).toBe('—');
  });
});

describe('formatDurationMs', () => {
  it('renders seconds with one decimal and sub-second in ms', () => {
    expect(formatDurationMs(8123)).toBe('8.1 s');
    expect(formatDurationMs(450)).toBe('450 ms');
    expect(formatDurationMs(null)).toBe('—');
  });
});

describe('timestamps', () => {
  it('formats HH:MMZ and full UTC', () => {
    expect(formatTimeZ('2026-10-26T09:05:00Z')).toBe('09:05Z');
    expect(formatTimestamp('2026-10-26T09:01:10Z')).toBe('2026-10-26 09:01:10Z');
    expect(formatTimeZ('garbage')).toBe('garbage');
  });
});

describe('misc', () => {
  it('formats integers with grouping and thresholds without trailing zeros', () => {
    expect(formatInteger(2345)).toBe('2,345');
    expect(formatThreshold(20)).toBe('20');
    expect(formatThreshold(2.5)).toBe('2.5');
  });
});
