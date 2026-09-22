const EN = 'en-US';

const numberFormatter = new Intl.NumberFormat(EN);

const currencyFormatter = new Intl.NumberFormat(EN, {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 4,
  maximumFractionDigits: 4,
});

/** Thousands-separated number, e.g. 12345 -> "12,345". */
export function formatNumber(value: number): string {
  return numberFormatter.format(value);
}

/** USD amount with 4 decimals, e.g. 0.0021 -> "$0.0021". */
export function formatCurrency(amount: number): string {
  return currencyFormatter.format(amount);
}

/** Compact token/request counts, e.g. 1234 -> "1.2K", 2500000 -> "2.5M". */
export function formatCompact(count?: number | null): string {
  if (count === undefined || count === null) return '0';
  if (count >= 1_000_000_000) return `${(count / 1_000_000_000).toFixed(2)}B`;
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`;
  if (count >= 1_000) return `${(count / 1_000).toFixed(1)}K`;
  return numberFormatter.format(count);
}
