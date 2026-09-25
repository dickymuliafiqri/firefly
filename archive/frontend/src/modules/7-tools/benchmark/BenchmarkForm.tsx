import React, { useMemo } from 'react';
import { Play, Square } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { cn } from '@/lib/utils';
import { useModelOptions } from '../shared/useModelOptions';
import {
  BENCHMARK_MAX_CONCURRENCY,
  BENCHMARK_MAX_REQUESTS,
  BENCHMARK_MIN_CONCURRENCY,
  BENCHMARK_MIN_REQUESTS,
} from '@/core/state/benchmarkSlice';
import {
  useBenchmarkActions,
  useBenchmarkConcurrency,
  useBenchmarkModel,
  useBenchmarkPrompt,
  useBenchmarkRequests,
  useBenchmarkStatus,
  usePlaygroundApiKey,
} from '@/core/state/store';

export interface BenchmarkFormProps {
  onStart: () => void;
  onStop: () => void;
}

const INPUT_CLASS =
  'px-2 py-1 rounded bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20';

/**
 * BenchmarkForm
 * Run parameters for the benchmark. Every knob is fixed except these four, so two
 * runs of the same numbers are comparable.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: atomic selector hooks
 */
export const BenchmarkForm = React.memo(function BenchmarkForm({
  onStart,
  onStop,
}: BenchmarkFormProps) {
  const model = useBenchmarkModel();
  const prompt = useBenchmarkPrompt();
  const requests = useBenchmarkRequests();
  const concurrency = useBenchmarkConcurrency();
  const status = useBenchmarkStatus();
  const apiKey = usePlaygroundApiKey();
  const { models, options } = useModelOptions();
  const {
    setBenchmarkModel,
    setBenchmarkPrompt,
    setBenchmarkRequests,
    setBenchmarkConcurrency,
  } = useBenchmarkActions();

  const isRunning = status === 'running';
  const selected = options.find((o) => o.id === model);
  const upstreamName = models.find((m) => m.public_name === model)?.upstream;

  const blockReason = useMemo(() => {
    if (!apiKey.trim()) return 'Tenant API Key is required. Enter one in the Chat tool first.';
    if (options.length === 0) return 'No model or combo is available on this gateway.';
    if (!selected?.available) {
      return selected?.kind === 'combo'
        ? `Combo "${model}" is unavailable because all member models or upstreams are offline.`
        : `Model "${model}" is unavailable because upstream "${upstreamName || 'unknown'}" is closed.`;
    }
    if (!prompt.trim()) return 'Prompt is empty.';
    return null;
  }, [apiKey, options.length, selected, model, upstreamName, prompt]);

  return (
    <div className="p-3 rounded-xl bg-transparent border border-white/[0.06] space-y-3 text-xs">
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">Model</span>
          <select
            value={model}
            onChange={(e) => setBenchmarkModel(e.target.value)}
            disabled={isRunning}
            className={cn(INPUT_CLASS, 'w-full max-w-full truncate')}
          >
            {options.map((o) => (
              <option key={o.id} value={o.id} className="bg-[#0a0d14] text-neutral-200">
                {o.id}
                {o.kind === 'combo' ? ' [combo]' : ''}
                {o.available ? '' : ' [CLOSED]'}
              </option>
            ))}
          </select>
        </label>

        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">Requests (1–{BENCHMARK_MAX_REQUESTS})</span>
          <input
            type="number"
            min={BENCHMARK_MIN_REQUESTS}
            max={BENCHMARK_MAX_REQUESTS}
            value={requests}
            disabled={isRunning}
            onChange={(e) => setBenchmarkRequests(Number(e.target.value))}
            className={cn(INPUT_CLASS, 'w-full')}
          />
        </label>

        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">
            Concurrency (1–{BENCHMARK_MAX_CONCURRENCY})
          </span>
          <input
            type="number"
            min={BENCHMARK_MIN_CONCURRENCY}
            max={BENCHMARK_MAX_CONCURRENCY}
            value={concurrency}
            disabled={isRunning}
            onChange={(e) => setBenchmarkConcurrency(Number(e.target.value))}
            className={cn(INPUT_CLASS, 'w-full')}
          />
        </label>

        <div className="flex items-end">
          {isRunning ? (
            <Button
              variant="minimal"
              size="md"
              onClick={onStop}
              className="text-rose-400 hover:text-rose-300"
              leftIcon={<Square className="w-3.5 h-3.5 fill-current" />}
            >
              Stop
            </Button>
          ) : (
            <Button
              variant="minimal"
              size="md"
              onClick={onStart}
              disabled={blockReason !== null}
              title={blockReason ?? 'Run the benchmark'}
              rightIcon={
                <Play className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />
              }
            >
              Run
            </Button>
          )}
        </div>
      </div>

      <label className="space-y-1 block">
        <span className="text-[11px] text-neutral-500">Prompt</span>
        <textarea
          value={prompt}
          onChange={(e) => setBenchmarkPrompt(e.target.value)}
          disabled={isRunning}
          rows={2}
          className={cn(INPUT_CLASS, 'w-full resize-none')}
        />
      </label>

      <div className="flex flex-wrap items-center justify-between gap-2 text-[10px] text-neutral-500">
        <span>Fixed per request: stream: true · temperature: 0 · max_tokens: 128</span>
        <span>A run sends real requests upstream and keeps running while you switch tools.</span>
      </div>

      {blockReason ? <p className="text-[11px] text-amber-300/90">{blockReason}</p> : null}
    </div>
  );
});
