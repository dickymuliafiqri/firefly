import React from 'react';
import type { CredentialKeyDTO, ConnectionDTO } from '@/services/schema';
import { UserCheck, ShieldAlert } from 'lucide-react';

export interface AccountRingSlotListProps {
  slots?: CredentialKeyDTO[];
  strategy?: string;
  connections?: ConnectionDTO[];
  protocol?: string;
}

export const AccountRingSlotList = React.memo(function AccountRingSlotList({
  slots = [],
  strategy = 'least_inflight',
  connections = [],
}: AccountRingSlotListProps) {
  if (slots.length === 0) {
    return (
      <div className="text-[11px] text-neutral-500 font-mono py-1.5 flex items-center justify-between border-t border-white/[0.04]">
        <span>Account Pool</span>
        <span className="text-amber-400/90 flex items-center gap-1">
          <ShieldAlert className="w-3 h-3" /> No account bound
        </span>
      </div>
    );
  }

  // Create a quick lookup map for connections
  const connMap = new Map<string, ConnectionDTO>();
  for (const c of connections) {
    connMap.set(c.id, c);
    if (c.email) {
      connMap.set(c.email, c);
    }
  }

  return (
    <div className="pt-2 border-t border-white/[0.04] flex flex-col gap-1.5">
      <div className="flex items-center justify-between text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
        <span>Account Ring ({slots.length} {slots.length === 1 ? 'account' : 'accounts'})</span>
        <span className="lowercase">{strategy}</span>
      </div>

      <div className="space-y-1">
        {slots.slice(0, 3).map((slot, index) => {
          const rawRef = slot.ref || '';
          const cleanRef = rawRef.startsWith('oauth:') ? rawRef.slice(6) : rawRef;
          const conn = connMap.get(cleanRef) || connMap.get(rawRef);

          const displayIdentity = conn?.email || cleanRef || `account-${index + 1}`;
          const projectId = conn?.provider_specific_data?.project_id || conn?.provider_specific_data?.region;
          const isExpired = conn?.is_expired === true;

          return (
            <div
              key={slot.ref || index}
              className="flex items-center justify-between py-1 px-2 rounded font-mono text-xs hover:bg-white/[0.02] transition-colors"
            >
              <div className="flex items-center gap-2 truncate min-w-0 flex-1">
                <span className="text-[10px] text-neutral-500 tabular-nums shrink-0">
                  #{index + 1}
                </span>
                <UserCheck className="w-3 h-3 text-neutral-400 shrink-0" />
                <span className="text-neutral-200 font-medium text-[11px] truncate max-w-[140px] sm:max-w-[180px]" title={displayIdentity}>
                  {displayIdentity}
                </span>
                {projectId ? (
                  <span className="text-[10px] text-neutral-500 truncate shrink-0 max-w-[90px]" title={`Project: ${projectId}`}>
                    ({projectId})
                  </span>
                ) : null}
              </div>

              <div className="flex items-center gap-2 flex-shrink-0 text-[10px] tabular-nums">
                {slot.max_concurrent ? (
                  <span className="text-neutral-500">{slot.max_concurrent} max</span>
                ) : null}
                {isExpired ? (
                  <span className="text-rose-400/90 font-medium flex items-center gap-1">
                    <span className="w-1.5 h-1.5 rounded-full bg-rose-400" />
                    EXPIRED
                  </span>
                ) : (
                  <span className="text-emerald-400/90 font-medium flex items-center gap-1">
                    <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
                    READY
                  </span>
                )}
              </div>
            </div>
          );
        })}

        {slots.length > 3 ? (
          <div className="mt-1 px-2 py-1 rounded bg-white/[0.02] border border-white/[0.04] text-[10px] text-neutral-400 flex items-center justify-between font-mono">
            <span>+{slots.length - 3} more accounts in pool</span>
            <span className="text-neutral-500 group-hover:text-neutral-300 transition-colors">Manage Accounts &rarr;</span>
          </div>
        ) : null}
      </div>
    </div>
  );
});
