// Week helpers for the UI. Phase 1 slice S4 extends this with labels and
// status text; the scaffold ships the identifier check only.

export const WEEK_ID_PATTERN = /^(\d{4})-W(\d{2})$/;

/** True when s has the YYYY-Www shape with a week number 01–53. */
export function isWeekId(s: string): boolean {
  const m = WEEK_ID_PATTERN.exec(s);
  if (!m) return false;
  const week = Number(m[2]);
  return week >= 1 && week <= 53;
}

/** Splits a valid week id into its numeric parts. */
export function parseWeekId(s: string): { year: number; week: number } {
  const m = WEEK_ID_PATTERN.exec(s);
  if (!m || !isWeekId(s)) throw new Error(`invalid week id: ${s}`);
  return { year: Number(m[1]), week: Number(m[2]) };
}
