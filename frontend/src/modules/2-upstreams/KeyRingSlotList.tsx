import React from 'react';
import type { CredentialKeyDTO } from '@/services/schema';
import { maskSecret } from '@/lib/secret';
import { Key } from 'lucide-react';

export interface KeyRingSlotListProps {
  slots?: CredentialKeyDTO[];
  strategy?: string;
}

export const KeyRingSlotList = React.memo(function KeyRingSlotList({
  slots = [],
  strategy = 'round_robin',
}: KeyRingSlotListProps) {
  if (slots.length === 0) {
    return (
      <div className="text-[11px] text-neutral-500 font-mono py-1.5 flex items-center justify-between border-t border-white/[0.04]">
        <span>Credentials</span>
        <span className="text-neutral-400">Default Global Key</span>
      </div>
    );
  }

  return (
    <div className="pt-2 border-t border-white/[0.04] flex flex-col gap-1.5">
      <div className="flex items-center justify-between text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
        <span>Credential KeyRing ({slots.length})</span>
        <span className="lowercase">{strategy}</span>
      </div>

      <div className="space-y-1">
        {slots.slice(0, 2).map((slot, index) => {
          const isRevoked = slot.secret?.includes('revoked');
          const isCooldown = false;
          // Slot secrets arrive from the snapshot's KeyRing, but a slot list can
          // also be rendered from the settings DTO, where the server already
          // masked them — maskSecret is idempotent so either provenance renders
          // correctly.
          const rawSecret = slot.secret || slot.api_key || '';
          const maskedSecret = rawSecret ? maskSecret(rawSecret) : 'sk-***';

          return (
            <div
              key={slot.ref || index}
              className="flex items-center justify-between py-1 px-2 rounded font-mono text-xs hover:bg-white/[0.02] transition-colors"
            >
              <div className="flex items-center gap-2 truncate">
                <Key className="w-3 h-3 text-neutral-500 flex-shrink-0" />
                <span className="text-neutral-300 font-medium text-[11px]">
                  {slot.ref || `slot-${index + 1}`}
                </span>
                <span className="text-neutral-500 text-[10px] truncate max-w-[150px]">
                  {maskedSecret}
                </span>
              </div>

              <div className="flex items-center gap-2 flex-shrink-0 text-[10px] tabular-nums">
                {slot.max_concurrent ? (
                  <span className="text-neutral-500">{slot.max_concurrent} max</span>
                ) : null}
                {slot.rps ? (
                  <span className="text-neutral-500">· {slot.rps} rps</span>
                ) : null}
                {isRevoked ? (
                  <span className="text-rose-400/90 font-medium">REVOKED</span>
                ) : isCooldown ? (
                  <span className="text-amber-400/90 font-medium">COOLDOWN</span>
                ) : (
                  <span className="text-emerald-400/90 font-medium">READY</span>
                )}
              </div>
            </div>
          );
        })}

        {slots.length > 2 ? (
          <div className="mt-1 px-2 py-1 rounded bg-white/[0.02] border border-white/[0.04] text-[10px] text-neutral-400 flex items-center justify-between font-mono">
            <span>+{slots.length - 2} more keys in keyring</span>
            <span className="text-neutral-500 group-hover:text-neutral-300 transition-colors">Manage & Test &rarr;</span>
          </div>
        ) : null}
      </div>
    </div>
  );
});
