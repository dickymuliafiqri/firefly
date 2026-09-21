/**
 * Generates random gateway API key: sk-gw-<32-hex-chars>.
 * Uses window.crypto.getRandomValues if available, falling back to Math.random.
 */
export function generateRandomGatewayKey(): string {
  const bytes = new Uint8Array(16);
  if (
    typeof window !== 'undefined' &&
    window.crypto &&
    typeof window.crypto.getRandomValues === 'function'
  ) {
    window.crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < 16; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  const hex = Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
  return `sk-gw-${hex}`;
}
