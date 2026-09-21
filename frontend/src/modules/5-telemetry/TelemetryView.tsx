import React, { useRef, useMemo } from 'react';
import { useTelemetryQuery } from '@/services/api';
import { InflightSemaphoreGauge } from './InflightSemaphoreGauge';
import { StreamingAreaChart } from './StreamingAreaChart';
import { PercentileLatencyCard } from './PercentileLatencyCard';
import { CooldownHeatmap } from './CooldownHeatmap';
import {
  Radio,
  Zap,
  TrendingUp,
  AlertTriangle,
  Users,
} from 'lucide-react';
import { cn } from '@/lib/utils';

/**
 * TelemetryView
 * Modul 5: Real-time Gateway Telemetry & Prometheus Observability.
 * Provides authentic, production-grade visibility into admission gates, throughput waveforms,
 * latency percentiles, and KeyRing 429 quota exhaustion.
 *
 * 100% REAL DATA — Zero dummy or simulated values.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - client-swr-dedup (via useTelemetryQuery)
 * - rendering-conditional-render
 */
export default React.memo(function TelemetryView() {
  const { data: telemetry } = useTelemetryQuery();

  // Track previous total requests to calculate live RPS across polling intervals
  const lastMetricsRef = useRef<{ timestamp: number; totalRequests: number } | null>(null);

  const currentRps = useMemo(() => {
    if (!telemetry) return 0;

    const now = Date.now();
    const currentTotal = telemetry.summary.total_requests;

    if (!lastMetricsRef.current) {
      lastMetricsRef.current = { timestamp: now, totalRequests: currentTotal };
      return 0;
    }

    const elapsedSec = (now - lastMetricsRef.current.timestamp) / 1000;
    const deltaRequests = currentTotal - lastMetricsRef.current.totalRequests;

    lastMetricsRef.current = { timestamp: now, totalRequests: currentTotal };

    if (elapsedSec <= 0 || deltaRequests < 0) return 0;
    return Math.round((deltaRequests / elapsedSec) * 10) / 10;
  }, [telemetry]);

  const summary = telemetry?.summary;
  const globalAdmission = telemetry?.global_admission;
  const models = telemetry?.models || [];
  const upstreams = telemetry?.upstreams || [];
  const tenantsUsage = telemetry?.tenants_usage || [];

  const activeStreams = summary?.active_streams ?? 0;
  const totalRequests = summary?.total_requests ?? 0;
  const p95Latency = summary?.p95_latency_ms ?? 0;
  const circuitTrips = summary?.circuit_trips ?? 0;
  const errorRate = summary?.error_rate_pct ?? 0;

  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      {/* Top Banner & Quick KPI Strip */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {/* Active Streams */}
        <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.12] transition-colors flex flex-col justify-between font-mono">
          <div className="flex items-center justify-between text-neutral-400">
            <span className="text-[11px]">Active Streams</span>
            <Radio
              className={cn(
                'w-3.5 h-3.5',
                activeStreams > 0 ? 'text-emerald-400 animate-pulse' : 'text-neutral-500'
              )}
            />
          </div>
          <div className="mt-2 flex items-baseline justify-between">
            <span className="text-2xl font-bold text-white tabular-nums">
              {activeStreams.toLocaleString()}
            </span>
            <span className="text-[10px] text-neutral-500">
              {activeStreams > 0 ? 'SSE in-flight' : 'idle'}
            </span>
          </div>
        </div>

        {/* P95 Latency */}
        <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.12] transition-colors flex flex-col justify-between font-mono">
          <div className="flex items-center justify-between text-neutral-400">
            <span className="text-[11px]">P95 Latency</span>
            <Zap className="w-3.5 h-3.5 text-neutral-500" />
          </div>
          <div className="mt-2 flex items-baseline justify-between">
            <span className="text-2xl font-bold text-white tabular-nums">
              {p95Latency > 0 ? (
                <>
                  {Math.round(p95Latency)}
                  <span className="text-xs font-normal text-neutral-500 ml-1">ms</span>
                </>
              ) : (
                '—'
              )}
            </span>
            <span className="text-[10px] text-neutral-500">TTFT / RTT</span>
          </div>
        </div>

        {/* Total Requests */}
        <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.12] transition-colors flex flex-col justify-between font-mono">
          <div className="flex items-center justify-between text-neutral-400">
            <span className="text-[11px]">Total Requests</span>
            <TrendingUp className="w-3.5 h-3.5 text-neutral-500" />
          </div>
          <div className="mt-2 flex items-baseline justify-between">
            <span className="text-2xl font-bold text-white tabular-nums">
              {totalRequests.toLocaleString()}
            </span>
            <span className="text-[10px] text-neutral-500">serviced</span>
          </div>
        </div>

        {/* Error Rate & Circuit Trips */}
        <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.12] transition-colors flex flex-col justify-between font-mono">
          <div className="flex items-center justify-between text-neutral-400">
            <span className="text-[11px]">Error Rate (4xx / 5xx)</span>
            <AlertTriangle
              className={cn('w-3.5 h-3.5', circuitTrips > 0 ? 'text-rose-400' : 'text-neutral-500')}
            />
          </div>
          <div className="mt-2 flex items-baseline justify-between">
            <span
              className={cn(
                'text-2xl font-bold tabular-nums',
                errorRate > 5 ? 'text-rose-400' : 'text-white'
              )}
            >
              {errorRate.toFixed(2)}%
            </span>
            <span className="text-[10px] text-neutral-500">
              {circuitTrips} {circuitTrips === 1 ? 'trip' : 'trips'}
            </span>
          </div>
        </div>
      </div>

      {/* Main 2-Column Responsive Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        {/* Left Column (7 cols): Throughput Chart & Global Admission Semaphore */}
        <div className="lg:col-span-7 space-y-6">
          <StreamingAreaChart
            currentRps={currentRps}
            currentInflight={globalAdmission?.inflight ?? activeStreams}
            totalRequests={totalRequests}
          />

          <InflightSemaphoreGauge
            activeInflight={globalAdmission?.inflight ?? activeStreams}
            maxSlots={globalAdmission?.capacity ?? 1500}
            queueDepth={globalAdmission?.queue_depth ?? 0}
          />
        </div>

        {/* Right Column (5 cols): Percentile Latencies & Cooldown Heatmap */}
        <div className="lg:col-span-5 space-y-6">
          <PercentileLatencyCard models={models} />
          <CooldownHeatmap upstreams={upstreams} />
        </div>
      </div>

      {/* Tenant Usage Real-Time Breakdown */}
      {tenantsUsage.length > 0 ? (
        <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs select-none">
          <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
            <div className="flex items-center gap-2">
              <Users className="w-4 h-4 text-neutral-400" />
              <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                Tenant Usage Quotas &amp; Ingestion
              </h3>
            </div>
            <span className="text-[10px] text-neutral-500">Recorded Counters</span>
          </div>

          <div className="mt-3 overflow-x-auto">
            <table className="w-full text-left font-mono text-xs">
              <thead>
                <tr className="text-neutral-500 border-b border-white/[0.04] text-[11px]">
                  <th className="pb-2 font-normal">Tenant</th>
                  <th className="pb-2 font-normal">Target Model</th>
                  <th className="pb-2 font-normal">Credential Ref</th>
                  <th className="pb-2 font-normal text-right">Total Requests</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/[0.04]">
                {tenantsUsage.map((row) => (
                  <tr
                    key={`${row.tenant}-${row.model}-${row.credential_ref || ''}`}
                    className="hover:bg-white/[0.015]"
                  >
                    <td className="py-2 font-medium text-neutral-200">{row.tenant}</td>
                    <td className="py-2 text-neutral-400">{row.model}</td>
                    <td className="py-2 text-neutral-500 text-[11px]">{row.credential_ref || '—'}</td>
                    <td className="py-2 text-right tabular-nums text-neutral-200">
                      {row.total_requests.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}
    </div>
  );
});
