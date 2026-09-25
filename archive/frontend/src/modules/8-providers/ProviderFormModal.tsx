import React, { useEffect, useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Save, Server } from 'lucide-react';
import type { ProviderRecordDTO } from '@/services/schema';
import { parseApiError } from '@/services/schema';
import {
  useCreateProviderMutation,
  useUpdateProviderMutation,
} from '@/services/api';
import { useStoreActions } from '@/core/state/store';

export interface ProviderFormModalProps {
  isOpen: boolean;
  onClose: () => void;
  /** null creates a provider; a record edits it. */
  provider: ProviderRecordDTO | null;
  onCreated?: (provider: ProviderRecordDTO) => void;
}

export const ProviderFormModal = React.memo(function ProviderFormModal({
  isOpen,
  onClose,
  provider,
  onCreated,
}: ProviderFormModalProps) {
  const { addToast } = useStoreActions();
  const createMutation = useCreateProviderMutation();
  const updateMutation = useUpdateProviderMutation();

  const isEdit = provider !== null;

  const [name, setName] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [description, setDescription] = useState('');
  const [isActive, setIsActive] = useState(true);

  useEffect(() => {
    if (!isOpen) return;
    setName(provider?.name ?? '');
    setBaseUrl(provider?.base_url ?? '');
    setDescription(provider?.description ?? '');
    setIsActive(provider?.is_active ?? true);
  }, [isOpen, provider]);

  const isPending = createMutation.isPending || updateMutation.isPending;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    const trimmedName = name.trim();
    const trimmedBaseUrl = baseUrl.trim();
    if (!trimmedName || !trimmedBaseUrl) {
      addToast({
        title: 'Incomplete Provider',
        message: 'A name and a base URL are both required.',
        type: 'error',
      });
      return;
    }

    try {
      if (isEdit && provider) {
        await updateMutation.mutateAsync({
          id: provider.id,
          patch: {
            base_url: trimmedBaseUrl,
            description: description.trim(),
            is_active: isActive,
          },
        });
        addToast({
          title: 'Provider Updated',
          message: `${trimmedName} saved. Routing picks the change up on the next catalog reload.`,
          type: 'success',
        });
      } else {
        const res = await createMutation.mutateAsync({
          name: trimmedName,
          base_url: trimmedBaseUrl,
          description: description.trim() || undefined,
          is_active: isActive,
        });
        addToast({
          title: 'Provider Created',
          message: `${res.provider.name} (#${res.provider.id}) is ready for keys.`,
          type: 'success',
        });
        onCreated?.(res.provider);
      }
      onClose();
    } catch (err) {
      addToast({
        title: isEdit ? 'Update Failed' : 'Create Failed',
        message: parseApiError(err),
        type: 'error',
      });
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={isEdit ? `Edit Provider: ${provider?.name}` : 'Add Provider'}
      description={
        isEdit
          ? 'The name is the harvester natural key and is not editable; a rename would orphan the credential pool.'
          : 'Providers own a credential pool. Keys are stored here and pooled server-side, never in the browser.'
      }
      size="md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium flex items-center gap-1.5">
            <Server className="w-3.5 h-3.5 text-neutral-400" />
            Name
          </label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={isEdit}
            placeholder="e.g. openai-pool"
            autoComplete="off"
            className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-50"
          />
          {isEdit ? (
            <span className="text-[10px] text-neutral-500">
              Immutable: upstreams and the harvester match this provider by name.
            </span>
          ) : null}
        </div>

        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium">Base URL</label>
          <input
            type="text"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="https://api.openai.com/v1"
            autoComplete="off"
            className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium">Description</label>
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Optional operator note"
            autoComplete="off"
            className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
          />
        </div>

        <label className="flex items-center gap-2 text-neutral-300 select-none cursor-pointer">
          <input
            type="checkbox"
            checked={isActive}
            onChange={(e) => setIsActive(e.target.checked)}
            className="accent-emerald-400"
          />
          Active (inactive providers stay stored but leave routing decisions)
        </label>

        <div className="pt-4 border-t border-white/[0.04] flex items-center justify-end gap-3">
          <Button variant="minimal" size="sm" type="button" onClick={onClose} disabled={isPending}>
            Cancel
          </Button>
          <Button
            variant="minimal"
            size="sm"
            type="submit"
            isLoading={isPending}
            leftIcon={<Save className="w-3.5 h-3.5 text-neutral-400" />}
          >
            {isEdit ? 'Save Provider' : 'Create Provider'}
          </Button>
        </div>
      </form>
    </Modal>
  );
});
