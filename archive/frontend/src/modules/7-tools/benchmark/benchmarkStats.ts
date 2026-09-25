import type { BenchmarkRequestResult } from './benchmarkRunner';

export interface BenchmarkSummary {
  ok: number;
  errors: number;
  aborted: number;
  errorRate: number | null;
  totalTokens: number;
  aggregateTps: number | null;
  ttftP50: number | null;
  ttftP95: number | null;
}

/** Nearest-rank percentile over unsorted input: sorted[ceil(p * n) - 1]. */
export function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const rank = Math.ceil(p * sorted.length);
  return sorted[Math.min(Math.max(rank, 1), sorted.length) - 1];
}

/**
 * summarize
 * Aggregates over the successful requests only. Aborted requests are reported
 * separately and excluded from the error rate, because a user-initiated stop is not
 * a gateway failure.
 */
export function summarize(results: BenchmarkRequestResult[]): BenchmarkSummary {
  const okResults = results.filter((r) => r.status === 'ok');
  const errors = results.filter((r) => r.status === 'error').length;
  const aborted = results.filter((r) => r.status === 'aborted').length;

  const ttfts = okResults.map((r) => r.ttftMs).filter((v): v is number => v !== null);
  const totalTokens = okResults.reduce((sum, r) => sum + r.tokens, 0);
  const totalMs = okResults.reduce((sum, r) => sum + r.totalMs, 0);

  return {
    ok: okResults.length,
    errors,
    aborted,
    errorRate: okResults.length + errors > 0 ? errors / (okResults.length + errors) : null,
    totalTokens,
    aggregateTps: totalMs > 0 ? Math.round((totalTokens / (totalMs / 1000)) * 10) / 10 : null,
    ttftP50: percentile(ttfts, 0.5),
    ttftP95: percentile(ttfts, 0.95),
  };
}
