import React, { useEffect, useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { RotateCcw, Save } from 'lucide-react';
import type { ProviderKeyRecordDTO, ProviderKeyStatus } from '@/services/schema';
import { PROVIDER_KEY_STATUSES, parseApiError } from '@/services/schema';
import { usePatchProviderKeyMutation } from '@/services/api';
import { useStoreActions } from '@/core/state/store';
import { endOfDayMs, toDateInputValue, toMillis } from '@/lib/datetime';
import { looksMasked } from '@/lib/secret';

export interface KeyPatchModalProps {
  isOpen: boolean;
  onClose: () => void;
  keyRecord: ProviderKeyRecordDTO | null;
}

export const KeyPatchModal = React.memo(function KeyPatchModal({
  isOpen,
  onClose,
  keyRecord,
}: KeyPatchModalProps) {
  const { addToast } = useStoreActions();
  const patchMutation = usePatchProviderKeyMutation();

  const [status, setStatus] = useState<ProviderKeyStatus>('active');
  const [isActive, setIsActive] = useState(true);
  const [expiryDate, setExpiryDate] = useState('');
  const [clearExpiry, setClearExpiry] = useState(false);
  const [newSecret, setNewSecret] = useState('');

  useEffect(() => {
    if (!isOpen || !keyRecord) return;
    setStatus((keyRecord.status as ProviderKeyStatus) ?? 'active');
    setIsActive(keyRecord.is_active);
    setExpiryDate(toDateInputValue(toMillis(keyRecord.expires_at)));
    setClearExpiry(false);
    setNewSecret('');
  }, [isOpen, keyRecord]);

  if (!keyRecord) return null;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (newSecret && looksMasked(newSecret)) {
      addToast({
        title: 'Masked Hint Detected',
        message:
          'The rotation field holds a display hint, not a secret. Paste the real credential — the hint shown here can never be substituted back.',
        type: 'error',
      });
      return;
    }

    const patch: {
      api_key?: string;
      status?: ProviderKeyStatus;
      is_active?: boolean;
      expires_at?: number | null;
    } = { status, is_active: isActive };

    if (newSecret) {
      patch.api_key = newSecret;
    }
    if (clearExpiry) {
      patch.expires_at = null;
    } else if (expiryDate) {
      patch.expires_at = endOfDayMs(expiryDate);
    }

    try {
      const res = await patchMutation.mutateAsync({ id: keyRecord.id, patch });
      addToast({
        title: 'Key Updated',
        message: `Key #${res.key.id} saved: status ${res.key.status}, routing ${
          res.key.is_active ? 'on' : 'off'
        }${newSecret ? '; the secret was rotated in place and mirror-credentials were rewritten.' : '.'}`,
        type: 'success',
      });
      onClose();
    } catch (err) {
      addToast({
        title: 'Update Failed',
        message: parseApiError(err),
        type: 'error',
      });
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={`Key #${keyRecord.id}`}
      description="Hints are read-only: the stored secret can never be read back, only replaced."
      size="md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        <div className="p-3 rounded-xl bg-transparent border border-white/[0.06] flex items-center justify-between gap-3">
          <div>
            <span className="text-[10px] text-neutral-500 uppercase tracking-wider block">Masked hint</span>
            <span className="text-neutral-200">{keyRecord.api_key_hint || '[REDACTED]'}</span>
          </div>
          <div className="text-right text-[10px] text-neutral-500">
            <div>{keyRecord.total_requests.toLocaleString()} request(s)</div>
            <div>
              {keyRecord.last_used_at
                ? `last used ${new Date(keyRecord.last_used_at).toLocaleString()}`
                : 'never used'}
            </div>
          </div>
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
          </div>

          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Expires</label>
            <input
              type="date"
              value={expiryDate}
              disabled={clearExpiry}
              onChange={(e) => setExpiryDate(e.target.value)}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-40"
            />
            <label className="flex items-center gap-2 text-[10px] text-neutral-400 select-none cursor-pointer">
              <input
                type="checkbox"
                checked={clearExpiry}
                onChange={(e) => setClearExpiry(e.target.checked)}
                className="accent-emerald-400"
              />
              Clear the stored expiry (never expires)
            </label>
          </div>
        </div>

        <label className="flex items-center gap-2 text-neutral-300 select-none cursor-pointer">
          <input
            type="checkbox"
            checked={isActive}
            onChange={(e) => setIsActive(e.target.checked)}
            className="accent-emerald-400"
          />
          Routable — routing needs both an `active` status and this flag to agree
        </label>

        <div className="flex flex-col gap-1.5 pt-3 border-t border-white/[0.04]">
          <label className="text-neutral-400 font-medium flex items-center gap-1.5">
            <RotateCcw className="w-3.5 h-3.5 text-neutral-400" />
            Rotate secret (optional)
          </label>
          <input
            type="password"
            value={newSecret}
            onChange={(e) => setNewSecret(e.target.value)}
            autoComplete="new-password"
            placeholder="Leave empty to keep the current credential"
            className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
          />
          <span className="text-[10px] text-neutral-500">
            The key keeps its id, so <span className="text-neutral-400">&lt;upstream&gt;-key-&lt;id&gt;</span> refs
            survive, and every credential bound to it is rewritten in the same transaction.
          </span>
        </div>

        <div className="pt-4 border-t border-white/[0.04] flex items-center justify-end gap-3">
          <Button variant="minimal" size="sm" type="button" onClick={onClose} disabled={patchMutation.isPending}>
            Cancel
          </Button>
          <Button
            variant="minimal"
            size="sm"
            type="submit"
            isLoading={patchMutation.isPending}
            leftIcon={<Save className="w-3.5 h-3.5 text-neutral-400" />}
          >
            Apply Changes
          </Button>
        </div>
      </form>
    </Modal>
  );
});
