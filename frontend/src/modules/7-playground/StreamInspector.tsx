import React, { useState, useCallback } from 'react';
import { Button } from '@/components/ui/Button';
import { Terminal, Copy, Check } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface StreamInspectorProps {
  rawPackets: string[];
}

/**
 * StreamInspector
 * Inspects raw Server-Sent Events (SSE) packets flowing through the proxy.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const StreamInspector = React.memo(function StreamInspector({
  rawPackets,
}: StreamInspectorProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(rawPackets.join('\n\n'));
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [rawPackets]);

  return (
    <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs space-y-3 select-none">
      <div className="flex items-center justify-between pb-2 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Terminal className="w-4 h-4 text-neutral-400" />
          <h4 className="text-xs uppercase tracking-wider text-neutral-300 font-medium">
            Raw SSE Packet Inspector
          </h4>
          <span className="text-[10px] text-neutral-500 font-mono">
            ({rawPackets.length} frames)
          </span>
        </div>

        <Button
          variant="minimal"
          size="sm"
          onClick={handleCopy}
          disabled={rawPackets.length === 0}
          leftIcon={copied ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
        >
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <div className="max-h-[220px] overflow-y-auto rounded-lg bg-transparent p-3 border border-white/[0.04] font-mono text-[11px] select-text">
        {rawPackets.length === 0 ? (
          <div className="p-6 text-center text-neutral-500">
            No active stream frames captured yet.
          </div>
        ) : (
          <div className="space-y-1.5 divide-y divide-white/[0.04]">
            {rawPackets.map((pkt, idx) => (
              <div key={idx} className="pt-1.5 first:pt-0">
                <span className="text-neutral-500 select-none mr-2">
                  #{idx + 1}:
                </span>
                <span
                  className={cn(
                    'whitespace-pre-wrap break-all',
                    pkt.includes('[DONE]') ? 'text-amber-400/90 font-medium' : 'text-neutral-300'
                  )}
                >
                  {pkt}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
});
