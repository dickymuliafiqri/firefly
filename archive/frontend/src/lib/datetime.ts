/**
 * Normalises an epoch to milliseconds. Legacy rows written before Firefly owned
 * `api_keys` carry second-scale `expires_at`, and a second-scale value read as
 * milliseconds lands in 1970 — it would read as permanently expired.
 */
export function toMillis(epoch?: number | null): number | null {
  if (!epoch) return null;
  return epoch < 100_000_000_000 ? epoch * 1000 : epoch;
}

/** `YYYY-MM-DD` for a date input, empty when unset or unparseable. */
export function toDateInputValue(ms?: number | null): string {
  if (!ms) return '';
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/**
 * A `YYYY-MM-DD` field means "valid through that day", so it resolves to the
 * end of the day locally instead of midnight (which would expire it early).
 */
export function endOfDayMs(dateOnly: string): number | null {
  if (!dateOnly) return null;
  const parts = dateOnly.split('-').map(Number);
  if (parts.length !== 3 || parts.some((n) => Number.isNaN(n))) return null;
  const [year, month, day] = parts;
  return new Date(year, month - 1, day, 23, 59, 59, 999).getTime();
}
