import React from 'react';
import type { TenantDTO } from '@/services/schema';
import { Shield, ShieldOff, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface TenantTableProps {
  tenants: TenantDTO[];
  onToggleStatus?: (tenant: TenantDTO) => void;
  onDelete?: (tenant: TenantDTO) => void;
}

const TenantRow = React.memo(function TenantRow({
  tenant,
  onToggleStatus,
  onDelete,
}: {
  tenant: TenantDTO;
  onToggleStatus?: (tenant: TenantDTO) => void;
  onDelete?: (tenant: TenantDTO) => void;
}) {
  const isActive = tenant.status !== 'suspended';
  const models = tenant.allowed_models || ['*'];
  const rps = tenant.rate_limit?.rps ?? 20;
  const maxC = tenant.rate_limit?.max_concurrent ?? 20;

  return (
    <tr className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors font-mono text-xs">
      {/* Name & Hash */}
      <td className="py-3 px-4">
        <div className="font-medium text-white text-[13px]">{tenant.name}</div>
        <div className="text-[10px] text-neutral-500 truncate max-w-[200px]" title={tenant.key_hash}>
          {tenant.key_hash || tenant.api_key || 'No hash recorded'}
        </div>
      </td>

      {/* Allowed Models */}
      <td className="py-3 px-4">
        <div className="flex flex-wrap gap-1.5 max-w-xs">
          {models.map((m) => (
            <span
              key={m}
              className="text-[11px] font-mono text-neutral-400 border border-white/[0.06] px-1.5 py-0.5 rounded bg-transparent"
            >
              {m}
            </span>
          ))}
        </div>
      </td>

      {/* Rate Limits */}
      <td className="py-3 px-4 text-neutral-400">
        <div className="tabular-nums text-neutral-200 font-medium">{rps} RPS</div>
        <div className="text-[10px] text-neutral-500 tabular-nums">{maxC} max inflight</div>
      </td>

      {/* Status */}
      <td className="py-3 px-4">
        <div className="flex items-center gap-1.5 text-[11px] tabular-nums">
          <span
            className={cn(
              'w-1.5 h-1.5 rounded-full flex-shrink-0',
              isActive ? 'bg-emerald-400' : 'bg-rose-400'
            )}
          />
          <span className={isActive ? 'text-emerald-400/90' : 'text-rose-400/90'}>
            {isActive ? 'ACTIVE' : 'SUSPENDED'}
          </span>
        </div>
      </td>

      {/* Actions */}
      <td className="py-3 px-4 text-right">
        <div className="inline-flex items-center gap-2">
          <button
            type="button"
            onClick={() => onToggleStatus?.(tenant)}
            className="p-1 rounded text-neutral-400 hover:text-white transition-colors cursor-pointer hover:bg-white/[0.05]"
            title={isActive ? 'Suspend tenant' : 'Activate tenant'}
            aria-label={isActive ? 'Suspend tenant' : 'Activate tenant'}
          >
            {isActive ? (
              <ShieldOff className="w-3.5 h-3.5" />
            ) : (
              <Shield className="w-3.5 h-3.5" />
            )}
          </button>

          <button
            type="button"
            onClick={() => onDelete?.(tenant)}
            className="p-1 rounded text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer hover:bg-white/[0.05]"
            title="Delete tenant"
            aria-label="Delete tenant"
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </td>
    </tr>
  );
});

/**
 * Minimalist TenantTable
 * Harmonized with Overview and Upstream design language.
 * Vercel React Best Practices: rerender-memo
 */
export const TenantTable = React.memo(function TenantTable({
  tenants,
  onToggleStatus,
  onDelete,
}: TenantTableProps) {
  if (tenants.length === 0) {
    return (
      <div className="p-8 text-center text-neutral-500 font-mono text-xs">
        No tenants provisioned. Click "Issue Tenant Key" to create credentials.
      </div>
    );
  }

  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full text-left border-collapse font-mono">
        <thead>
          <tr className="border-b border-white/[0.06] text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
            <th className="py-2.5 px-4 font-normal">Tenant / Key Hash</th>
            <th className="py-2.5 px-4 font-normal">Allowed Models</th>
            <th className="py-2.5 px-4 font-normal">Rate Limits</th>
            <th className="py-2.5 px-4 font-normal">Status</th>
            <th className="py-2.5 px-4 font-normal text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {tenants.map((t, idx) => (
            <TenantRow
              key={t.key_hash || t.name || idx}
              tenant={t}
              onToggleStatus={onToggleStatus}
              onDelete={onDelete}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
});
