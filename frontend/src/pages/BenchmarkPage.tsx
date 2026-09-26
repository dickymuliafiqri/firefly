import { useEffect, useRef, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import { PageHeader } from '@/components/ui/PageHeader';
import { useSettingsQuery, hasGateway } from '@/services/api';
import { useUiStore } from '@/state/store';

interface BenchResult {
  completed: number;
  errors: number;
  elapsedMs: number;
  durations: number[];
}

function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  const idx = Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length));
  return Math.round(sorted[idx]);
}

function fmtMs(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)}s` : `${ms}ms`;
}

const API_KEY_STORAGE = 'firefly-chat-api-key';

export function BenchmarkPage() {
  const settings = useSettingsQuery();
  const pushToast = useUiStore((s) => s.pushToast);

  const [model, setModel] = useState('');
  const [count, setCount] = useState('200');
  const [conc, setConc] = useState('20');
  const [apiKey, setApiKey] = useState(() => localStorage.getItem(API_KEY_STORAGE) ?? '');

  const [running, setRunning] = useState(false);
  const [progress, setProgress] = useState({ done: 0, total: 0 });
  const [result, setResult] = useState<BenchResult | null>(null);
  const abortRef = useRef(false);

  const modelOptions = (settings.data?.models ?? [])
    .filter((m) => m.enabled !== false)
    .map((m) => m.public_name);
  const activeModel = model || modelOptions[0] || 'gpt-4o';

  // Auto-fill API key from the first tenant with a plaintext key, only if
  // user has not manually filled this field yet.
  useEffect(() => {
    if (apiKey || !settings.data) return;
    const first = settings.data.tenants.find((t) => t.api_key && !t.api_key.includes('•'));
    if (first?.api_key) setApiKey(first.api_key);
  }, [apiKey, settings.data]);

  useEffect(() => {
    localStorage.setItem(API_KEY_STORAGE, apiKey);
  }, [apiKey]);

  async function requestOnce(m: string): Promise<number> {
    const start = performance.now();
    const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' };
    if (apiKey.trim()) headers['Authorization'] = `Bearer ${apiKey.trim()}`;

    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        model: m,
        messages: [{ role: 'user', content: 'ping' }],
        max_tokens: 8,
        stream: false,
      }),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    await res.json().catch(() => null);
    return performance.now() - start;
  }

  async function run() {
    if (running) return;
    if (!apiKey.trim()) {
      pushToast({
        type: 'error',
        title: 'Tenant API key required',
        message: 'Enter the Tenant API Key (sk-gw-…) so the gateway can authenticate the request.',
      });
      return;
    }
    if (!(await hasGateway())) {
      pushToast({
        type: 'info',
        title: 'Benchmark requires backend',
        message: 'This origin is not running a Firefly gateway — start the Go gateway and access the dashboard from there.',
      });
      return;
    }

    const total = Math.max(1, parseInt(count, 10) || 100);
    const concurrency = Math.max(1, Math.min(100, parseInt(conc, 10) || 10));

    setRunning(true);
    abortRef.current = false;
    setResult(null);
    setProgress({ done: 0, total });

    const durations: number[] = [];
    let errors = 0;
    const started = performance.now();
    let next = 0;
    let done = 0;

    async function worker() {
      for (;;) {
        if (abortRef.current || next >= total) return;
        const i = next++;
        try {
          durations.push(await requestOnce(activeModel));
        } catch {
          errors++;
        }
        done++;
        if (i % 5 === 0 || done === total) setProgress({ done, total });
      }
    }

    await Promise.all(Array.from({ length: concurrency }, worker));

    const elapsedMs = performance.now() - started;
    durations.sort((a, b) => a - b);
    setResult({
      completed: durations.length,
      errors,
      elapsedMs,
      durations,
    });
    setRunning(false);
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Benchmark"
        description="Gateway throughput and latency benchmarking."
        actions={
          running ? (
            <button className="btn btn-secondary" onClick={() => (abortRef.current = true)}>
              Stop
            </button>
          ) : (
            <button className="btn btn-primary" onClick={run}>
              Run Benchmark
            </button>
          )
        }
      />

      <div className="stack">
        <div className="card">
          <div className="card-header">
            <h2>Configuration</h2>
            <Badge tone={running ? 'ok' : 'neutral'}>{running ? 'RUNNING' : result ? 'DONE' : 'IDLE'}</Badge>
          </div>
          <div className="card-body">
            <div className="form-grid">
              <Field
                label="Tenant API key"
                htmlFor="bm-key"
                hint="Tenant sk-gw-… key; stored in this browser only."
              >
                <input
                  id="bm-key"
                  type="password"
                  className="mono"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="sk-gw-…"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                />
              </Field>
              <Field label="Target model" htmlFor="bm-model">
                <select id="bm-model" value={activeModel} onChange={(e) => setModel(e.target.value)}>
                  {modelOptions.length === 0 ? <option>gpt-4o</option> : null}
                  {modelOptions.map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
              </Field>
              <Field label="Request count" htmlFor="bm-requests" hint="Total non-streaming requests, max_tokens 8.">
                <input id="bm-requests" value={count} className="mono" onChange={(e) => setCount(e.target.value)} />
              </Field>
              <Field label="Concurrency" htmlFor="bm-conc" hint="Parallel workers (1–100).">
                <input id="bm-conc" value={conc} className="mono" onChange={(e) => setConc(e.target.value)} />
              </Field>
            </div>
            <div style={{ marginTop: 16 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                <span className="hint">Progress</span>
                <span className="mono faint">
                  {progress.done} / {progress.total} &middot;{' '}
                  {progress.total ? Math.round((progress.done / progress.total) * 100) : 0}%
                </span>
              </div>
              <div
                className="progress"
                role="progressbar"
                aria-valuenow={progress.total ? Math.round((progress.done / progress.total) * 100) : 0}
                aria-valuemin={0}
                aria-valuemax={100}
              >
                <div
                  className="fill"
                  style={{ width: `${progress.total ? (progress.done / progress.total) * 100 : 0}%` }}
                />
              </div>
            </div>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <h2>Results</h2>
            <span className="mono faint">{result ? `end-to-end · ${fmtMs(result.elapsedMs)} total` : 'waiting for run'}</span>
          </div>
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Metric</th>
                  <th className="num">Value</th>
                  <th>Notes</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>Throughput</td>
                  <td className="num">
                    {result ? `${(result.completed / (result.elapsedMs / 1000)).toFixed(1)} req/s` : '—'}
                  </td>
                  <td className="dim">
                    {result ? `concurrency ${conc}` : 'at concurrency N'}
                  </td>
                </tr>
                <tr>
                  <td>Completed</td>
                  <td className="num">{result ? `${result.completed} / ${progress.total}` : '—'}</td>
                  <td className="dim">successful responses</td>
                </tr>
                <tr>
                  <td>p50 latency</td>
                  <td className="num">{result ? fmtMs(percentile(result.durations, 50)) : '—'}</td>
                  <td className="dim">end-to-end request time</td>
                </tr>
                <tr>
                  <td>p95 latency</td>
                  <td className="num">{result ? fmtMs(percentile(result.durations, 95)) : '—'}</td>
                  <td className="dim">end-to-end request time</td>
                </tr>
                <tr>
                  <td>p99 latency</td>
                  <td className="num">{result ? fmtMs(percentile(result.durations, 99)) : '—'}</td>
                  <td className="dim">end-to-end request time</td>
                </tr>
                <tr>
                  <td>Errors</td>
                  <td className="num">{result ? `${result.errors} / ${progress.total}` : '—'}</td>
                  <td>
                    {result ? (
                      <span
                        style={{
                          color:
                            result.errors === 0
                              ? 'var(--ok)'
                              : result.errors > progress.total * 0.05
                                ? 'var(--danger)'
                                : 'var(--warn)',
                          fontWeight: 600,
                        }}
                      >
                        {progress.total ? Math.round((result.errors / progress.total) * 100) : 0}%
                      </span>
                    ) : (
                      <Badge tone="neutral">NO DATA</Badge>
                    )}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}
