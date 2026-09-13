import React from 'react';
import type { ComboDTO } from '@/services/schema';
import { Edit3, Trash2, ArrowRight } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface ComboCardProps {
  combo: ComboDTO;
  onToggleEnabled?: (name: string, enabled: boolean) => void;
  onEdit?: (combo: ComboDTO) => void;
  onDelete?: (combo: ComboDTO) => void;
}

/**
 * ComboCard
 * Minimalist Virtual Model Combo card matching ModelCard and UpstreamCard styling.
 * Adheres strictly to frontend-design: quiet, consistent, zero generic glow.
 * Vercel React Best Practices: rerender-memo
 */
export const ComboCard = React.memo(function ComboCard({
  combo,
  onToggleEnabled,
  onEdit,
  onDelete,
}: ComboCardProps) {
  const isEnabled = combo.enabled !== false;
  const strategy = combo.strategy || 'least_inflight';

  const strategyLabel = {
    least_inflight: 'least-inflight pool',
    round_robin: 'round-robin pool',
    failover: 'failover chain',
  }[strategy] || `${strategy} pool`;

  const strategyTag = {
    least_inflight: 'least-inflight',
    round_robin: 'round-robin',
    failover: 'failover',
  }[strategy] || strategy;

  return (
    <div className="group flex flex-col justify-between p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.14] hover:bg-white/[0.015] transition-all duration-200 font-mono text-xs select-none">
      {/* Top Header Row */}
      <div className="flex items-baseline justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-baseline gap-2.5 truncate">
          <span className="text-white font-medium text-[13px] tracking-tight">
            {combo.name}
          </span>
          <span className="text-[11px] text-neutral-500 truncate">
            {strategyLabel}
          </span>
        </div>

        <div className="flex items-center gap-2.5 flex-shrink-0">
          <button
            type="button"
            onClick={() => onToggleEnabled?.(combo.name, !isEnabled)}
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
            onClick={() => onEdit?.(combo)}
            className="text-neutral-400 hover:text-white transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Edit Combo"
            aria-label={`Edit ${combo.name}`}
          >
            <Edit3 className="w-3.5 h-3.5" />
          </button>

          <button
            type="button"
            onClick={() => onDelete?.(combo)}
            className="text-neutral-500 hover:text-rose-400 transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Delete Combo"
            aria-label={`Delete ${combo.name}`}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      {/* Target Member Flow */}
      <div className="flex items-center gap-2 py-2.5 font-mono text-xs overflow-x-auto text-neutral-400">
        <span className="text-neutral-500 text-[11px]">
          {strategy === 'failover' ? 'Chain:' : 'Pool:'}
        </span>
        {combo.models.map((modName, idx) => (
          <React.Fragment key={modName}>
            {idx > 0 && (
              strategy === 'failover' ? (
                <ArrowRight className="w-3 h-3 text-neutral-600 flex-shrink-0" />
              ) : (
                <span className="text-neutral-600 text-[11px]">/</span>
              )
            )}
            <span className={idx === 0 && strategy === 'failover' ? 'text-neutral-200 font-medium' : 'text-neutral-300'}>
              {modName}
            </span>
          </React.Fragment>
        ))}
      </div>

      {/* Metadata & Tag Footer */}
      <div className="flex flex-wrap items-center gap-2.5 pt-2.5 border-t border-white/[0.04] text-[11px] text-neutral-500">
        <span>{strategyTag}</span>
        <span>•</span>
        <span>{combo.models.length} {combo.models.length === 1 ? 'model' : 'models'}</span>
        <span className="ml-auto tabular-nums text-[10px] text-neutral-500">
          combo route
        </span>
      </div>
    </div>
  );
});
