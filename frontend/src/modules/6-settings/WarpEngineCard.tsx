import React from 'react';
import { Button } from '@/components/ui/Button';
import { Shield, RefreshCw, Globe, Zap } from 'lucide-react';
import { useWarpStatusQuery, useRotateWarpMutation } from '@/services/api';
import { cn } from '@/lib/utils';

export const WarpEngineCard = React.memo(function WarpEngineCard() {
  const { data: status, isFetching, refetch } = useWarpStatusQuery();
  const rotateMutation = useRotateWarpMutation();

  const isConnected = Boolean(status?.enabled && status?.public_ip);
  const isEnabled = Boolean(status?.enabled);

  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
      {/* Header */}
      <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Shield className="w-4 h-4 text-cyan-400" />
          <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
            Cloudflare WARP Egress Engine
          </h3>
        </div>
        {isConnected ? (
          <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-emerald-500/20 bg-emerald-500/10 text-emerald-400 text-[10px]">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse" />
            CONNECTED
          </span>
        ) : isEnabled ? (
          <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-cyan-500/20 bg-cyan-500/10 text-cyan-400 text-[10px]">
            <span className="w-1.5 h-1.5 rounded-full bg-cyan-400" />
            READY (ON-DEMAND)
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-white/[0.06] bg-white/[0.02] text-neutral-400 text-[10px]">
            <span className="w-1.5 h-1.5 rounded-full bg-neutral-500" />
            DISABLED
          </span>
        )}
      </div>

      {/* Info Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Egress Public IP
          </span>
          <div className="flex items-center gap-1.5">
            <Globe className="w-3.5 h-3.5 text-neutral-400" />
            <span className="text-white font-medium text-xs truncate">
              {status?.public_ip || 'Negotiated on first request'}
            </span>
          </div>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Edge Colocation (Colo)
          </span>
          <div className="flex items-center gap-1.5">
            <Zap className="w-3.5 h-3.5 text-cyan-400" />
            <span className="text-neutral-200 font-medium text-xs truncate">
              {status?.colo ? `${status.colo} (Cloudflare Edge)` : 'Anycast Best Route'}
            </span>
          </div>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            WireGuard Endpoint
          </span>
          <span className="text-neutral-300 font-medium text-xs block truncate">
            {status?.endpoint || '162.159.192.1:2408'}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Tunnel Internal IP
          </span>
          <span className="text-neutral-300 font-medium text-xs block truncate">
            {status?.internal_ip || '—'}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Handshake Latency
          </span>
          <span className="text-neutral-300 font-medium text-xs block">
            {status?.latency_ms ? `${status.latency_ms} ms` : '—'}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Active Outbound Streams
          </span>
          <span className="text-neutral-300 font-medium text-xs block">
            {status?.active_connections ?? 0}
            {status?.draining_sessions ? (
              <span className="text-neutral-500">
                {' '}
                (+{status.draining_sessions} draining)
              </span>
            ) : null}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Last Session Rotation
          </span>
          <span className="text-neutral-300 font-medium text-xs block truncate">
            {status?.last_rotated_at
              ? new Date(status.last_rotated_at).toLocaleTimeString()
              : 'Initial session'}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Auto-Rotation Interval
          </span>
          <span className="text-cyan-300 font-medium text-xs block truncate">
            {status?.auto_rotate_interval_seconds
              ? `Every ${Math.round(status.auto_rotate_interval_seconds / 60)}m`
              : '5m (default)'}
          </span>
        </div>

        <div className="p-2.5 rounded-lg bg-white/[0.01] border border-white/[0.04] space-y-1">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">
            Next Scheduled Rotation
          </span>
          <span className="text-neutral-300 font-medium text-xs block truncate">
            {status?.next_rotation_at
              ? new Date(status.next_rotation_at).toLocaleTimeString()
              : 'In ~5 minutes'}
          </span>
        </div>
      </div>

      {/* Description Callout */}
      <div className="p-3 rounded-lg bg-transparent border border-white/[0.04] space-y-1 text-[11px] text-neutral-400">
        <p className="text-neutral-300 font-medium flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-cyan-400" />
          Zero-Privilege Userspace WireGuard &amp; gVisor Netstack
        </p>
        <p>
          Embedded directly in Firefly without requiring root/TUN permissions or system daemons.
          Routes traffic cleanly across Cloudflare’s global Anycast edge network to bypass IP-based rate limits.
        </p>
        <p className="text-neutral-500">
          Upstreams configured with <code className="text-neutral-300">egress_mode: &quot;warp&quot;</code> route all requests through this tunnel. Auto-rotation automatically refreshes identity and outbound IP every 5 minutes in the background without dropping active streams.
        </p>
      </div>

      {/* Action Footer */}
      <div className="flex items-center gap-2 pt-1">
        <Button
          variant="minimal"
          size="sm"
          onClick={() => rotateMutation.mutate()}
          isLoading={rotateMutation.isPending}
          disabled={rotateMutation.isPending}
        >
          <RefreshCw className={cn('w-3.5 h-3.5 mr-1.5', rotateMutation.isPending && 'animate-spin')} />
          Rotate Session &amp; Egress IP
        </Button>

        <Button
          variant="minimal"
          size="sm"
          onClick={() => refetch()}
          disabled={isFetching}
          className="text-neutral-500 hover:text-neutral-300"
        >
          Refresh Status
        </Button>
      </div>
    </div>
  );
});
