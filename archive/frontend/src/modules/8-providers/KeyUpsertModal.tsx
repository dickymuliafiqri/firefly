import React, { useEffect, useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { KeyRound, Upload } from 'lucide-react';
import type { ProviderKeyStatus, ProviderRecordDTO } from '@/services/schema';
import { PROVIDER_KEY_STATUSES, parseApiError } from '@/services/schema';
import { useUpsertProviderKeysMutation } from '@/services/api';
import { useStoreActions } from '@/core/state/store';
import { endOfDayMs } from '@/lib/datetime';
import { looksMasked } from '@/lib/secret';

export interface KeyUpsertModalProps {
  isOpen: boolean;
  onClose: () => void;
  provider: ProviderRecordDTO;
}

export const KeyUpsertModal = React.memo(function KeyUpsertModal({
  isOpen,
  onClose,
  provider,
}: KeyUpsertModalProps) {
  const { addToast } = useStoreActions();
  const upsertMutation = useUpsertProviderKeysMutation();

  const [rawSecrets, setRawSecrets] = useState('');
  const [status, setStatus] = useState<ProviderKeyStatus>('active');
  const [expiryDate, setExpiryDate] = useState('');
  const [reassign, setReassign] = useState(false);

  useEffect(() => {
    if (!isOpen) return;
    setRawSecrets('');
    setStatus('active');
    setExpiryDate('');
    setReassign(false);
  }, [isOpen, provider.id]);

  const secrets = React.useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const line of rawSecrets.split('\n')) {
      const value = line.trim();
      if (!value || seen.has(value)) continue;
      seen.add(value);
      out.push(value);
    }
    return out;
  }, [rawSecrets]);

  const maskedCount = React.useMemo(
    () => secrets.filter(looksMasked).length,
    [secrets]
  );

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (secrets.length === 0) {
      addToast({
        title: 'No Keys Provided',
        message: 'Paste at least one secret, one per line.',
        type: 'error',
      });
      return;
    }
    if (maskedCount > 0) {
      addToast({
        title: 'Masked Hint Detected',
        message: `${maskedCount} line(s) look like a display hint (they contain "..." or "[REDACTED]"). Paste the real secret — this dashboard never receives it back.`,
        type: 'error',
      });
      return;
    }

    const expiresAt = endOfDayMs(expiryDate);

    try {
      const res = await upsertMutation.mutateAsync({
        providerId: provider.id,
        reassign,
        keys: secrets.map((api_key) => ({
          api_key,
          status,
          ...(expiryDate ? { expires_at: expiresAt } : {}),
        })),
      });

      addToast({
        title: 'Keys Stored',
        message: `${provider.name}: ${res.created} created, ${res.updated} updated (${res.reassigned} moved), ${res.unchanged} unchanged. ${res.key_ids.length} key(s) routable.`,
        type: 'success',
      });
      onClose();
    } catch (err) {
      addToast({
        title: 'Batch Rejected',
        message: parseApiError(err),
        type: 'error',
      });
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={`Add Keys to ${provider.name}`}
      description="Secrets are sent straight to the credential store and are never read back — every later view shows a masked hint only."
      size="md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium flex items-center gap-1.5">
            <KeyRound className="w-3.5 h-3.5 text-neutral-400" />
            Secrets (one per line)
          </label>
          <textarea
            value={rawSecrets}
            onChange={(e) => setRawSecrets(e.target.value)}
            rows={6}
            spellCheck={false}
            autoComplete="off"
            placeholder={'sk-...\nsk-...'}
            className="w-full px-3 py-2 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 resize-y"
          />
          <span className="text-[10px] text-neutral-500">
            {secrets.length} unique secret(s) parsed, blank lines and duplicates dropped.
            Keys are matched by the secret itself, so re-sending a batch updates instead of duplicating
            and never renumbers a key id.
          </span>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Status</label>
            <select
              value={status}
              onChange={(e) => setStatus(e.target.value as ProviderKeyStatus)}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            >
              {PROVIDER_KEY_STATUSES.map((s) => (
                <option key={s} value={s} className="bg-[#090b10]">
                  {s}
                </option>
              ))}
            </select>
            <span className="text-[10px] text-neutral-500">Only `active` routes traffic.</span>
          </div>

          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Expires (optional)</label>
            <input
              type="date"
              value={expiryDate}
              onChange={(e) => setExpiryDate(e.target.value)}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
            <span className="text-[10px] text-neutral-500">
              Left empty, any expiry already stored on those rows is kept.
            </span>
          </div>
        </div>

        <label className="flex items-start gap-2 text-neutral-300 select-none cursor-pointer">
          <input
            type="checkbox"
            checked={reassign}
            onChange={(e) => setReassign(e.target.checked)}
            className="accent-emerald-400 mt-0.5"
          />
          <span>
            Move keys already owned by another provider
            <span className="block text-[10px] text-neutral-500">
              Without this, a secret that belongs to another provider fails the whole batch with 409.
            </span>
          </span>
        </label>

        <div className="pt-4 border-t border-white/[0.04] flex items-center justify-end gap-3">
          <Button variant="minimal" size="sm" type="button" onClick={onClose} disabled={upsertMutation.isPending}>
            Cancel
          </Button>
          <Button
            variant="minimal"
            size="sm"
            type="submit"
            isLoading={upsertMutation.isPending}
            leftIcon={<Upload className="w-3.5 h-3.5 text-emerald-400" />}
          >
            Store {secrets.length > 0 ? `${secrets.length} Key${secrets.length === 1 ? '' : 's'}` : 'Keys'}
          </Button>
        </div>
      </form>
    </Modal>
  );
});
