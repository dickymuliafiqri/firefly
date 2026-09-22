import React from 'react';
import type { ProviderKeyRecordDTO } from '@/services/schema';
import { Pencil, PowerOff, Power, Trash2, KeyRound } from 'lucide-react';
import { cn } from '@/lib/utils';
import { toMillis } from '@/lib/datetime';

export interface ProviderKeyTableProps {
  keys: ProviderKeyRecordDTO[];
  isLoading?: boolean;
  onEdit: (keyRecord: ProviderKeyRecordDTO) => void;
  onDelete: (keyRecord: ProviderKeyRecordDTO) => void;
  onToggleRoutable: (keyRecord: ProviderKeyRecordDTO) => void;
}

function statusTone(record: ProviderKeyRecordDTO, expired: boolean) {
  if (expired) return { dot: 'bg-amber-400', text: 'text-amber-400/90' };
  if (record.status === 'active' && record.is_active)
    return { dot: 'bg-emerald-400', text: 'text-emerald-400/90' };
  return { dot: 'bg-neutral-500', text: 'text-neutral-400' };
}

const KeyRow = React.memo(function KeyRow({
  record,
  onEdit,
  onDelete,
  onToggleRoutable,
}: {
  record: ProviderKeyRecordDTO;
  onEdit: (keyRecord: ProviderKeyRecordDTO) => void;
  onDelete: (keyRecord: ProviderKeyRecordDTO) => void;
  onToggleRoutable: (keyRecord: ProviderKeyRecordDTO) => void;
}) {
  const expires = toMillis(record.expires_at);
  const isExpired = expires !== null && expires < Date.now();
  const isRoutable = record.status === 'active' && record.is_active && !isExpired;
  const tone = statusTone(record, isExpired);
  const lastUsed = toMillis(record.last_used_at);

  return (
    <tr className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors font-mono text-xs">
      <td className="py-2.5 px-4">
        <div className="flex items-center gap-2">
          <span className="text-[11px] tabular-nums text-neutral-500">#{record.id}</span>
          <span className="text-neutral-200">{record.api_key_hint || '[REDACTED]'}</span>
        </div>
        <span className="text-[10px] text-neutral-600">
          ref suffix -key-{record.id}
        </span>
      </td>

      <td className="py-2.5 px-4">
        <div className="flex items-center gap-1.5 text-[11px] uppercase">
          <span className={cn('w-1.5 h-1.5 rounded-full flex-shrink-0', tone.dot)} />
          <span className={cn('font-medium text-[10px]', tone.text)}>
            {isExpired ? 'expired' : record.status}
          </span>
        </div>
        <span className="text-[10px] text-neutral-500">
          {record.is_active ? 'routing enabled' : 'routing disabled'}
        </span>
      </td>

      <td className="py-2.5 px-4">
        {expires ? (
          <span className={cn('text-[11px]', isExpired ? 'text-rose-400/90' : 'text-neutral-300')}>
            {new Date(expires).toLocaleDateString(undefined, {
              year: 'numeric',
              month: 'short',
              day: 'numeric',
            })}
          </span>
        ) : (
          <span className="text-[11px] text-neutral-500">never</span>
        )}
      </td>

      <td className="py-2.5 px-4 text-neutral-400 tabular-nums text-[11px]">
        {record.total_requests.toLocaleString()}
        <span className="block text-[10px] text-neutral-600">
          {lastUsed ? new Date(lastUsed).toLocaleDateString() : 'never used'}
        </span>
      </td>

      <td className="py-2.5 px-4 text-right">
        <div className="inline-flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => onToggleRoutable(record)}
            className={cn(
              'p-1.5 rounded transition-colors cursor-pointer hover:bg-white/[0.05]',
              isRoutable
                ? 'text-neutral-400 hover:text-white'
                : 'text-emerald-400/90 hover:text-emerald-300'
            )}
            title={isRoutable ? 'Take out of rotation' : 'Put back into rotation'}
            aria-label={isRoutable ? 'Deactivate key' : 'Activate key'}
          >
            {isRoutable ? <PowerOff className="w-3.5 h-3.5" /> : <Power className="w-3.5 h-3.5" />}
          </button>

          <button
            type="button"
            onClick={() => onEdit(record)}
            className="p-1.5 rounded text-neutral-400 hover:text-white transition-colors cursor-pointer hover:bg-white/[0.05]"
            title="Edit status, expiry, or rotate the secret"
            aria-label="Edit key"
          >
            <Pencil className="w-3.5 h-3.5" />
          </button>

          <button
            type="button"
            onClick={() => onDelete(record)}
            className="p-1.5 rounded text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer hover:bg-white/[0.05]"
            title="Delete key and its bound credentials"
            aria-label="Delete key"
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </td>
    </tr>
  );
});

export const ProviderKeyTable = React.memo(function ProviderKeyTable({
  keys,
  isLoading = false,
  onEdit,
  onDelete,
  onToggleRoutable,
}: ProviderKeyTableProps) {
  if (isLoading) {
    return (
      <div className="p-8 text-center text-neutral-500 font-mono text-xs">Loading credentials…</div>
    );
  }

  if (keys.length === 0) {
    return (
      <div className="p-8 text-center flex flex-col items-center justify-center font-mono text-xs text-neutral-500">
        <KeyRound className="w-6 h-6 text-neutral-600 mb-2" />
        No credentials for this provider yet. Add keys here, or let the harvester sync them in.
      </div>
    );
  }

  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full text-left border-collapse font-mono">
        <thead>
          <tr className="border-b border-white/[0.06] text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
            <th className="py-2.5 px-4 font-normal">Key</th>
            <th className="py-2.5 px-4 font-normal">Status</th>
            <th className="py-2.5 px-4 font-normal">Expires</th>
            <th className="py-2.5 px-4 font-normal">Usage</th>
            <th className="py-2.5 px-4 font-normal text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {keys.map((record) => (
            <KeyRow
              key={record.id}
              record={record}
              onEdit={onEdit}
              onDelete={onDelete}
              onToggleRoutable={onToggleRoutable}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
});
