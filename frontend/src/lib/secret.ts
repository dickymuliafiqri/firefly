/**
 * Mirrors the server's hint-shape check. Surfaces that only ever display masked
 * hints must refuse input in this shape: echoing a display string back would
 * otherwise store a placeholder as a routable credential. The harvester sync
 * path is deliberately exempt — it holds real secrets and never sees hints.
 */
export function looksMasked(value: string): boolean {
  return value.includes('...') || value === '[REDACTED]';
}

/**
 * Masks a credential for display, matching the server's `maskSecret` byte for
 * byte. The output carries the `...` marker (or is `[REDACTED]`), so a form that
 * echoes it back is still recognised by `looksMasked`.
 *
 * Already-masked input is returned untouched: read surfaces hand the dashboard
 * pre-masked values (`[REDACTED]` for a secret of 8 bytes or fewer, `<3>...<4>`
 * otherwise) and re-masking one would mangle it — `[REDACTED]` is longer than 8
 * bytes, so it would come back as `[RE...TED]`. Idempotence here is what makes
 * this safe to apply to a value of unknown provenance.
 */
export function maskSecret(value: string): string {
  if (!value) return '';
  if (looksMasked(value)) return value;
  if (value.length <= 8) return '[REDACTED]';
  return `${value.slice(0, 3)}...${value.slice(-4)}`;
}
