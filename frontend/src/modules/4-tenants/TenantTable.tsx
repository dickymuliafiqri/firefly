import React, { useState } from 'react';
import type { TenantDTO } from '@/services/schema';
import {
  Shield,
  ShieldOff,
  Trash2,
  Coins,
  Copy,
  Check,
  Eye,
  EyeOff,
  AlertCircle,
} from 'lucide-react';
import { cn, copyToClipboard } from '@/lib/utils';
import { useStoreActions } from '@/core/state/store';

export interface TenantTableProps {
  tenants: TenantDTO[];
  onToggleStatus?: (tenant: TenantDTO) => void;
  onDelete?: (tenant: TenantDTO) => void;
  onTopup?: (tenant: TenantDTO) => void;
}

function formatTokens(count?: number | null): string {
  if (count === undefined || count === null) return '0';
  if (count >= 1_000_000_000) return `${(count / 1_000_000_000).toFixed(2)}B`;
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`;
  if (count >= 1_000) return `${(count / 1_000).toFixed(1)}K`;
  return count.toLocaleString();
}

function maskApiKey(key: string): string {
  if (key.length <= 12) return '••••••••';
  return `${key.slice(0, 7)}••••${key.slice(-4)}`;
}

const TenantRow = React.memo(function TenantRow({
  tenant,
  onToggleStatus,
  onDelete,
  onTopup,
}: {
  tenant: TenantDTO;
  onToggleStatus?: (tenant: TenantDTO) => void;
  onDelete?: (tenant: TenantDTO) => void;
  onTopup?: (tenant: TenantDTO) => void;
}) {
  const { addToast } = useStoreActions();
  const [showKey, setShowKey] = useState(false);
  const [copied, setCopied] = useState(false);

  const models = tenant.allowed_models || ['*'];
  const rps = tenant.rate_limit?.rps ?? 20;
  const maxC = tenant.rate_limit?.max_concurrent ?? 20;

  const nowSec = Math.floor(Date.now() / 1000);
  const isExpired = tenant.expires_at !== undefined && tenant.expires_at !== null && tenant.expires_at > 0 && tenant.expires_at < nowSec;
  const maxTokens = tenant.max_tokens ?? 0;
  const usedTokens = tenant.used_tokens ?? 0;
  const isQuotaExceeded = maxTokens > 0 && usedTokens >= maxTokens;

  // Determine computed status
  let displayStatus = tenant.status || 'active';
  if (tenant.status === 'suspended') {
    displayStatus = 'suspended';
  } else if (isExpired) {
    displayStatus = 'expired';
  } else if (isQuotaExceeded) {
    displayStatus = 'exhausted';
  }

  const isActive = displayStatus === 'active';

  // Quota percentage
  const usagePercent = maxTokens > 0 ? Math.min(100, Math.round((usedTokens / maxTokens) * 100)) : 0;

  const handleCopyKey = async () => {
    const textToCopy = tenant.api_key || tenant.key_hash || '';
    if (!textToCopy) return;

    const success = await copyToClipboard(textToCopy);
    if (success) {
      setCopied(true);
      addToast({
        title: 'Key Copied',
        message: 'Tenant credential copied to clipboard.',
        type: 'success',
      });
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const keyDisplay = tenant.api_key ? (
    <div className="flex items-center gap-1.5 mt-0.5">
      <span className="font-mono text-[11px] text-neutral-400">
        {showKey ? tenant.api_key : maskApiKey(tenant.api_key)}
      </span>
      <button
        type="button"
        onClick={() => setShowKey(!showKey)}
        className="p-0.5 text-neutral-500 hover:text-white transition-colors"
        title={showKey ? 'Hide key' : 'Show key'}
      >
        {showKey ? <EyeOff className="w-3 h-3" /> : <Eye className="w-3 h-3" />}
      </button>
      <button
        type="button"
        onClick={handleCopyKey}
        className="p-0.5 text-neutral-500 hover:text-white transition-colors"
        title="Copy key"
      >
        {copied ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3" />}
      </button>
    </div>
  ) : (
    <div className="flex items-center gap-1.5 mt-0.5">
      <span className="font-mono text-[10px] text-neutral-500 truncate max-w-[160px]" title={tenant.key_hash}>
        {tenant.key_hash ? `hash:${tenant.key_hash.slice(0, 12)}…` : 'No key recorded'}
      </span>
      {tenant.key_hash && (
        <button
          type="button"
          onClick={handleCopyKey}
          className="p-0.5 text-neutral-500 hover:text-white transition-colors"
          title="Copy hash"
        >
          {copied ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3" />}
        </button>
      )}
    </div>
  );

  return (
    <tr className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors font-mono text-xs">
      {/* Tenant Name & Plain Key / Hash */}
      <td className="py-3 px-4">
        <div className="font-medium text-white text-[13px]">{tenant.name}</div>
        {keyDisplay}
      </td>

      {/* Quota & Token Usage */}
      <td className="py-3 px-4 min-w-[170px]">
        <div className="flex items-center justify-between text-[11px] mb-1">
          <span className="text-neutral-300 font-medium">
            {formatTokens(usedTokens)}
            <span className="text-neutral-500"> / {maxTokens > 0 ? formatTokens(maxTokens) : '∞'}</span>
          </span>
          {maxTokens > 0 && (
            <span
              className={cn(
                'text-[10px] tabular-nums',
                usagePercent >= 100
                  ? 'text-rose-400 font-semibold'
                  : usagePercent >= 80
                  ? 'text-amber-400'
                  : 'text-neutral-400'
              )}
            >
              {usagePercent}%
            </span>
          )}
        </div>
        {maxTokens > 0 ? (
          <div className="w-full h-1.5 rounded-full bg-white/[0.06] overflow-hidden">
            <div
              className={cn(
                'h-full rounded-full transition-all duration-300',
                usagePercent >= 100
                  ? 'bg-rose-500'
                  : usagePercent >= 80
                  ? 'bg-amber-500'
                  : 'bg-emerald-500'
              )}
              style={{ width: `${Math.min(100, usagePercent)}%` }}
            />
          </div>
        ) : (
          <span className="text-[10px] text-neutral-500">Unlimited quota</span>
        )}
      </td>

      {/* Validity / Expiry */}
      <td className="py-3 px-4">
        {tenant.expires_at ? (
          <div className="flex flex-col">
            <span
              className={cn(
                'text-[11px]',
                isExpired ? 'text-rose-400 font-medium flex items-center gap-1' : 'text-neutral-300'
              )}
            >
              {isExpired && <AlertCircle className="w-3 h-3 text-rose-400" />}
              {new Date(tenant.expires_at * 1000).toLocaleDateString(undefined, {
                year: 'numeric',
                month: 'short',
                day: 'numeric',
              })}
            </span>
            <span className="text-[10px] text-neutral-500">
              {isExpired ? 'Expired' : 'Valid'}
            </span>
          </div>
        ) : (
          <span className="text-[11px] text-neutral-400">Never expires</span>
        )}
      </td>

      {/* Allowed Models */}
      <td className="py-3 px-4">
        <div className="flex flex-wrap gap-1 max-w-xs">
          {models.map((m) => (
            <span
              key={m}
              className="text-[10px] font-mono text-neutral-400 border border-white/[0.06] px-1.5 py-0.2 rounded bg-transparent"
            >
              {m}
            </span>
          ))}
        </div>
      </td>

      {/* Rate Limits */}
      <td className="py-3 px-4 text-neutral-400">
        <div className="tabular-nums text-neutral-200 font-medium">{rps} RPS</div>
        <div className="text-[10px] text-neutral-500 tabular-nums">{maxC} in-flight</div>
      </td>

      {/* Status */}
      <td className="py-3 px-4">
        <div className="flex items-center gap-1.5 text-[11px] tabular-nums uppercase">
          <span
            className={cn(
              'w-1.5 h-1.5 rounded-full flex-shrink-0',
              displayStatus === 'active'
                ? 'bg-emerald-400'
                : displayStatus === 'expired'
                ? 'bg-amber-400'
                : 'bg-rose-400'
            )}
          />
          <span
            className={cn(
              'font-medium text-[10px]',
              displayStatus === 'active'
                ? 'text-emerald-400/90'
                : displayStatus === 'expired'
                ? 'text-amber-400/90'
                : 'text-rose-400/90'
            )}
          >
            {displayStatus}
          </span>
        </div>
      </td>

      {/* Actions */}
      <td className="py-3 px-4 text-right">
        <div className="inline-flex items-center gap-1.5">
          {/* Top-up Button */}
          <button
            type="button"
            onClick={() => onTopup?.(tenant)}
            className="p-1.5 rounded text-emerald-400/90 hover:text-emerald-300 transition-colors cursor-pointer hover:bg-emerald-500/10"
            title="Top-up quota and extend expiration"
            aria-label="Top-up tenant"
          >
            <Coins className="w-3.5 h-3.5" />
          </button>

          {/* Suspend / Activate Button */}
          <button
            type="button"
            onClick={() => onToggleStatus?.(tenant)}
            className="p-1.5 rounded text-neutral-400 hover:text-white transition-colors cursor-pointer hover:bg-white/[0.05]"
            title={isActive ? 'Suspend tenant' : 'Activate tenant'}
            aria-label={isActive ? 'Suspend tenant' : 'Activate tenant'}
          >
            {isActive ? (
              <ShieldOff className="w-3.5 h-3.5" />
            ) : (
              <Shield className="w-3.5 h-3.5" />
            )}
          </button>

          {/* Delete Button */}
          <button
            type="button"
            onClick={() => onDelete?.(tenant)}
            className="p-1.5 rounded text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer hover:bg-white/[0.05]"
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

export const TenantTable = React.memo(function TenantTable({
  tenants,
  onToggleStatus,
  onDelete,
  onTopup,
}: TenantTableProps) {
  if (tenants.length === 0) {
    return (
      <div className="p-8 text-center text-neutral-500 font-mono text-xs">
        No tenants provisioned. Click "Issue Tenant Key" to create commercial API credentials.
      </div>
    );
  }

  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full text-left border-collapse font-mono">
        <thead>
          <tr className="border-b border-white/[0.06] text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
            <th className="py-2.5 px-4 font-normal">Tenant / API Key</th>
            <th className="py-2.5 px-4 font-normal">Token Quota</th>
            <th className="py-2.5 px-4 font-normal">Expiry</th>
            <th className="py-2.5 px-4 font-normal">Allowed Models</th>
            <th className="py-2.5 px-4 font-normal">Rate Limits</th>
            <th className="py-2.5 px-4 font-normal">Status</th>
            <th className="py-2.5 px-4 font-normal text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {tenants.map((t, idx) => (
            <TenantRow
              key={t.api_key || t.key_hash || t.name || idx}
              tenant={t}
              onToggleStatus={onToggleStatus}
              onDelete={onDelete}
              onTopup={onTopup}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
});
