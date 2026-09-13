import React from 'react';
import type { ModelDTO } from '@/services/schema';
import { Edit3, Trash2, ArrowRight } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface ModelCardProps {
  model: ModelDTO;
  onToggleEnabled?: (publicName: string, enabled: boolean) => void;
  onEdit?: (model: ModelDTO) => void;
  onDelete?: (model: ModelDTO) => void;
}

/**
 * ModelCard
 * Transparent, minimal model route card matching UpstreamCard and Overview.
 * Vercel React Best Practices: rerender-memo
 */
export const ModelCard = React.memo(function ModelCard({
  model,
  onToggleEnabled,
  onEdit,
  onDelete,
}: ModelCardProps) {
  const isEnabled = model.enabled !== false;
  const caps = model.capabilities || {};

  return (
    <div className="group flex flex-col justify-between p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.14] hover:bg-white/[0.015] transition-all duration-200 font-mono text-xs select-none">
      {/* Top Header Row */}
      <div className="flex items-baseline justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-baseline gap-2.5 truncate">
          <span className="text-white font-medium text-[13px] tracking-tight">
            {model.public_name}
          </span>
          <span className="text-[11px] text-neutral-500 truncate">
            {model.upstream_model}
          </span>
        </div>

        <div className="flex items-center gap-2.5 flex-shrink-0">
          <button
            type="button"
            onClick={() => onToggleEnabled?.(model.public_name, !isEnabled)}
            className="flex items-center gap-1.5 text-[11px] tabular-nums hover:opacity-80 transition-opacity cursor-pointer"
            title={isEnabled ? 'Click to disable' : 'Click to enable'}
          >
            <span
              className={cn(
                'w-1.5 h-1.5 rounded-full',
                isEnabled ? 'bg-emerald-400' : 'bg-neutral-600'
              )}
            />
            <span
              className={cn(
                isEnabled ? 'text-emerald-400/90' : 'text-neutral-500'
              )}
            >
              {isEnabled ? 'ACTIVE' : 'DISABLED'}
            </span>
          </button>

          <button
            type="button"
            onClick={() => onEdit?.(model)}
            className="text-neutral-400 hover:text-white transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Edit Route"
            aria-label={`Edit ${model.public_name}`}
          >
            <Edit3 className="w-3.5 h-3.5" />
          </button>

          <button
            type="button"
            onClick={() => onDelete?.(model)}
            className="text-neutral-500 hover:text-rose-400 transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Delete Route"
            aria-label={`Delete ${model.public_name}`}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      {/* Target Upstream Route */}
      <div className="flex items-center gap-2 py-2.5 font-mono text-xs overflow-x-auto text-neutral-400">
        <span className="text-neutral-500 text-[11px]">Target:</span>
        <span className="text-neutral-200 font-medium">{model.upstream}</span>
        <ArrowRight className="w-3 h-3 text-neutral-600 flex-shrink-0" />
        <span className="text-neutral-400 text-[11px] truncate">{model.upstream_model}</span>
      </div>

      {/* Capabilities & Metadata Footer */}
      <div className="flex flex-wrap items-center gap-2.5 pt-2.5 border-t border-white/[0.04] text-[11px] text-neutral-500">
        {caps.stream ? <span>stream</span> : null}
        {caps.tools ? <span>tools</span> : null}
        {caps.vision ? <span>vision</span> : null}
        {caps.json_mode ? <span>json</span> : null}
        {caps.embeddings ? <span>embeddings</span> : null}
        {model.max_context ? (
          <span className="ml-auto tabular-nums text-[10px] text-neutral-500">
            {Math.round(model.max_context / 1000)}k ctx
          </span>
        ) : null}
      </div>
    </div>
  );
});
