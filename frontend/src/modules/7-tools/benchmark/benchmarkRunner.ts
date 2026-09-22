export type BenchmarkRequestStatus = 'ok' | 'error' | 'aborted';

export interface BenchmarkRequestResult {
  index: number;
  status: BenchmarkRequestStatus;
  ttftMs: number | null;
  totalMs: number;
  tokens: number;
  tps: number | null;
  errorMessage: string | null;
}

export interface BenchmarkRequestOptions {
  index: number;
  apiKey: string;
  model: string;
  prompt: string;
  signal: AbortSignal;
}

/**
 * benchmarkRequest
 * One measured streaming chat completion through the gateway. Framework-free on
 * purpose (no React, no store) so the pool in useBenchmarkRun owns all state.
 *
 * Metrics use the same definitions as the chat waterfall: TTFT is the first read
 * batch that carried content, tokens count content deltas, TPS is end-to-end.
 */
export async function benchmarkRequest({
  index,
  apiKey,
  model,
  prompt,
  signal,
}: BenchmarkRequestOptions): Promise<BenchmarkRequestResult> {
  const startTime = performance.now();
  let firstTokenTime: number | null = null;
  let tokens = 0;

  const finish = (
    status: BenchmarkRequestStatus,
    errorMessage: string | null
  ): BenchmarkRequestResult => {
    const totalMs = Math.round(performance.now() - startTime);
    return {
      index,
      status,
      ttftMs: firstTokenTime,
      totalMs,
      tokens,
      tps: tokens > 0 ? Math.round((tokens / (totalMs / 1000)) * 10) / 10 : null,
      errorMessage,
    };
  };

  try {
    const response = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${apiKey}`,
      },
      signal,
      // Same reason as the chat window: a cacheable SSE body can be buffered whole
      // by some browsers, which would destroy every timing measured here.
      cache: 'no-store',
      body: JSON.stringify({
        model,
        messages: [{ role: 'user', content: prompt }],
        temperature: 0,
        max_tokens: 128,
        stream: true,
      }),
    });

    if (!response.ok) {
      const errJson = await response.json().catch(() => ({}));
      return finish(
        'error',
        errJson?.error?.message || `HTTP error ${response.status}: ${response.statusText}`
      );
    }
    if (!response.body) {
      return finish('error', 'response carried no body');
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    try {
      for (;;) {
        const { done, value } = await reader.read();
        const readEnd = performance.now();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        let batchTokens = 0;
        let streamError: string | null = null;
        let sawDone = false;

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed || trimmed.startsWith(':')) continue;
          if (!trimmed.startsWith('data: ')) continue;

          const payload = trimmed.slice(6);
          if (payload === '[DONE]') {
            sawDone = true;
            continue;
          }

          try {
            const parsed = JSON.parse(payload);
            if (parsed?.error) {
              streamError = parsed.error.message || parsed.error.type || 'upstream stream error';
              continue;
            }
            if (parsed?.choices?.[0]?.delta?.content) batchTokens++;
          } catch {
            // Ignore a partial frame; the next read completes it.
          }
        }

        if (batchTokens > 0) {
          tokens += batchTokens;
          if (firstTokenTime === null) firstTokenTime = Math.round(readEnd - startTime);
        }
        if (streamError) return finish('error', streamError);
        if (sawDone) break;
      }
    } finally {
      // Release the connection without draining the frames that follow [DONE].
      await reader.cancel().catch(() => {});
    }

    return finish('ok', null);
  } catch (err) {
    if (signal.aborted) return finish('aborted', null);
    return finish('error', err instanceof Error ? err.message : 'request failed');
  }
}
