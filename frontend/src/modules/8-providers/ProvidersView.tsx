import { useState, useCallback, useMemo, useEffect, useRef } from 'react';
import type { ProviderKeyRecordDTO, ProviderRecordDTO } from '@/services/schema';
import { parseApiError } from '@/services/schema';
import {
  useDeleteProviderKeyMutation,
  useDeleteProviderMutation,
  usePatchProviderKeyMutation,
  useProviderKeysQuery,
  useProvidersQuery,
} from '@/services/api';
import { ProviderFormModal } from './ProviderFormModal';
import { KeyUpsertModal } from './KeyUpsertModal';
import { KeyPatchModal } from './KeyPatchModal';
import { ProviderKeyTable } from './ProviderKeyTable';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { useStoreActions } from '@/core/state/store';
import {
  AlertCircle,
  Database,
  KeyRound,
  Lock,
  Pencil,
  Plus,
  RefreshCw,
  Server,
  Trash2,
} from 'lucide-react';
import { cn } from '@/lib/utils';

export default function ProvidersView() {
  const { addToast } = useStoreActions();

  const providersQuery = useProvidersQuery();
  const providers = useMemo(() => providersQuery.data?.providers ?? [], [providersQuery.data]);
  // File-config mode projects the catalog read-only from the running
  // configuration: there is no row to edit, and the server answers 501 to every
  // mutation. Rendering the surface as a viewer keeps that from reading as a
  // broken page.
  const readOnly = providersQuery.data?.read_only === true;

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [formTarget, setFormTarget] = useState<ProviderRecordDTO | null>(null);
  const [upsertTarget, setUpsertTarget] = useState<ProviderRecordDTO | null>(null);
  const [patchTarget, setPatchTarget] = useState<ProviderKeyRecordDTO | null>(null);
  const [deletingProvider, setDeletingProvider] = useState<ProviderRecordDTO | null>(null);
  const [deletingKey, setDeletingKey] = useState<ProviderKeyRecordDTO | null>(null);

  const selected = useMemo(
    () => providers.find((p) => p.id === selectedId) ?? null,
    [providers, selectedId]
  );

  // Select the first provider once the list lands, and drop a selection whose
  // provider was deleted elsewhere (harvester sync, another operator tab). An id
  // that was never in a loaded list is a just-created provider whose refetch has
  // not landed yet — resetting on that would steal the selection back.
  const seenIdsRef = useRef<Set<number>>(new Set());
  useEffect(() => {
    const ids = new Set(providers.map((p) => p.id));
    const wasListed = selectedId !== null && seenIdsRef.current.has(selectedId);
    for (const id of ids) seenIdsRef.current.add(id);

    if (ids.size === 0) {
      if (selectedId !== null) setSelectedId(null);
      return;
    }
    if (selectedId === null || (wasListed && !ids.has(selectedId))) {
      setSelectedId(providers[0].id);
    }
  }, [providers, selectedId]);

  const keysQuery = useProviderKeysQuery(selected?.id ?? null);
  const keys = keysQuery.data?.keys ?? [];

  const deleteProviderMutation = useDeleteProviderMutation();
  const deleteKeyMutation = useDeleteProviderKeyMutation();
  const patchKeyMutation = usePatchProviderKeyMutation();

  const handleCreate = useCallback(() => {
    setFormTarget(null);
    setFormOpen(true);
  }, []);

  const handleEdit = useCallback((provider: ProviderRecordDTO) => {
    setFormTarget(provider);
    setFormOpen(true);
  }, []);

  const handleCloseForm = useCallback(() => {
    setFormOpen(false);
    setFormTarget(null);
  }, []);

  const handleCreated = useCallback((provider: ProviderRecordDTO) => {
    setSelectedId(provider.id);
  }, []);

  const handleToggleRoutable = useCallback(
    async (record: ProviderKeyRecordDTO) => {
      const nextActive = !record.is_active;
      try {
        await patchKeyMutation.mutateAsync({ id: record.id, patch: { is_active: nextActive } });
        addToast({
          title: nextActive ? 'Key In Rotation' : 'Key Out Of Rotation',
          message: `${record.api_key_hint || `#${record.id}`} is now ${
            nextActive ? 'routable' : 'parked (kept stored, skipped by the keyring)'
          }.`,
          type: nextActive ? 'success' : 'info',
        });
      } catch (err) {
        addToast({
          title: 'Update Failed',
          message: parseApiError(err),
          type: 'error',
        });
      }
    },
    [patchKeyMutation, addToast]
  );

  const handleConfirmDeleteProvider = useCallback(async () => {
    if (!deletingProvider) return;
    const target = deletingProvider;
    try {
      const res = await deleteProviderMutation.mutateAsync(target.id);
      addToast({
        title: 'Provider Deleted',
        message: `${target.name} removed along with ${res.deleted_keys} credential(s).`,
        type: 'info',
      });
      setDeletingProvider(null);
    } catch (err) {
      // A 409 here means upstreams still point at the provider; the server
      // names them in the message, so keep the dialog open to be read.
      addToast({
        title: 'Cannot Delete Provider',
        message: parseApiError(err),
        type: 'error',
      });
    }
  }, [deletingProvider, deleteProviderMutation, addToast]);

  const handleConfirmDeleteKey = useCallback(async () => {
    if (!deletingKey) return;
    const target = deletingKey;
    try {
      await deleteKeyMutation.mutateAsync(target.id);
      addToast({
        title: 'Key Deleted',
        message: `${target.api_key_hint || `#${target.id}`} and its bound credential slots were removed.`,
        type: 'info',
      });
      setDeletingKey(null);
    } catch (err) {
      addToast({
        title: 'Delete Failed',
        message: parseApiError(err),
        type: 'error',
      });
    }
  }, [deletingKey, deleteKeyMutation, addToast]);

  const isMutating = deleteProviderMutation.isPending || deleteKeyMutation.isPending;

  if (providersQuery.isLoading) {
    return (
      <div className="flex items-center justify-center py-20 font-mono text-xs text-neutral-500">
        Loading providers…
      </div>
    );
  }

  if (providersQuery.isError) {
    return (
      <div className="p-12 text-center flex flex-col items-center justify-center font-mono rounded-xl border border-white/[0.06] bg-transparent">
        <AlertCircle className="w-8 h-8 text-rose-400 mb-3" />
        <h3 className="text-sm font-medium text-neutral-200">Provider Store Unavailable</h3>
        <p className="text-xs text-neutral-500 mt-1 max-w-md">{parseApiError(providersQuery.error)}</p>
        <p className="text-[11px] text-neutral-600 mt-2 max-w-md">
          The provider surface needs a configured storage engine or a loaded catalog; with neither,
          the endpoints fail closed.
        </p>
        <Button
          variant="minimal"
          size="sm"
          onClick={() => providersQuery.refetch()}
          leftIcon={<RefreshCw className="w-3.5 h-3.5 text-neutral-400" />}
          className="mt-4"
        >
          Retry
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6 w-full animate-in fade-in duration-300">
      {/* Top Action Bar — or the read-only explanation that replaces it */}
      {readOnly ? (
        <div className="flex items-start gap-3 px-4 py-3 rounded-xl border border-white/[0.06] bg-white/[0.02]">
          <Lock className="w-4 h-4 text-neutral-400 flex-shrink-0 mt-0.5" />
          <div className="font-mono text-[11px] leading-relaxed">
            <p className="text-neutral-300">
              Read-only view: this catalog is projected from the running configuration.
            </p>
            <p className="text-neutral-500 mt-0.5">
              Credentials are declared in <span className="text-neutral-400">upstreams.json</span> and
              the environment, so they are edited there and picked up on the next reload. Connect
              Turso storage to manage provider keys from this page.
            </p>
          </div>
        </div>
      ) : (
        <div className="flex items-center justify-end">
          <Button
            variant="minimal"
            size="sm"
            onClick={handleCreate}
            leftIcon={
              <Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />
            }
          >
            Add Provider
          </Button>
        </div>
      )}

      {providers.length === 0 ? (
        <div className="p-12 text-center flex flex-col items-center justify-center font-mono rounded-xl border border-white/[0.06] bg-transparent">
          <Database className="w-8 h-8 text-neutral-500 mb-3" />
          <h3 className="text-sm font-medium text-neutral-200">
            {readOnly ? 'No Configured Credential Pools' : 'No Providers Stored'}
          </h3>
          <p className="text-xs text-neutral-500 mt-1 max-w-md">
            {readOnly
              ? 'None of the configured upstreams carries a credential pool: they either authenticate with a fixed public token or have no keys yet. Declare keys for an upstream in upstreams.json to see them here.'
              : 'A provider owns a credential pool. Bind its name to an upstream to have the gateway pool those keys server-side — secrets never reach the browser.'}
          </p>
          {readOnly ? null : (
            <Button
              variant="minimal"
              size="sm"
              onClick={handleCreate}
              leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
              className="mt-4"
            >
              Add Provider
            </Button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-[minmax(240px,300px)_1fr] gap-4 items-start">
          {/* Provider List */}
          <div className="rounded-xl border border-white/[0.06] bg-transparent overflow-hidden">
            <div className="px-3 py-2 border-b border-white/[0.06] text-[10px] font-mono uppercase tracking-wider text-neutral-500">
              {providers.length} provider{providers.length === 1 ? '' : 's'}
            </div>
            <ul className="flex flex-col">
              {providers.map((provider) => {
                const isSelected = provider.id === selected?.id;
                return (
                  <li key={provider.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(provider.id)}
                      className={cn(
                        'w-full text-left px-3 py-2.5 border-b border-white/[0.04] transition-colors cursor-pointer',
                        isSelected ? 'bg-white/[0.05]' : 'hover:bg-white/[0.02]'
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className={cn(
                            'w-1.5 h-1.5 rounded-full flex-shrink-0',
                            provider.is_active ? 'bg-emerald-400' : 'bg-neutral-500'
                          )}
                        />
                        <span className="font-mono text-[12px] text-white truncate">
                          {provider.name}
                        </span>
                      </div>
                      <div className="flex items-center justify-between mt-1 font-mono text-[10px] text-neutral-500">
                        <span className="truncate pr-2">{provider.base_url}</span>
                        <span className="tabular-nums flex-shrink-0">
                          {provider.active_keys} key{provider.active_keys === 1 ? '' : 's'}
                        </span>
                      </div>
                    </button>
                  </li>
                );
              })}
            </ul>
          </div>

          {/* Selected Provider Detail */}
          {selected ? (
            <div className="flex flex-col gap-4">
              <div className="rounded-xl border border-white/[0.06] bg-transparent p-4">
                <div className="flex items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <Server className="w-4 h-4 text-neutral-400 flex-shrink-0" />
                      <h2 className="font-mono text-sm text-white truncate">{selected.name}</h2>
                      <span className="font-mono text-[10px] text-neutral-500 tabular-nums">
                        #{selected.id}
                      </span>
                      <Badge variant={selected.is_active ? 'emerald' : 'neutral'} dot>
                        {selected.is_active ? 'active' : 'inactive'}
                      </Badge>
                    </div>
                    <p className="font-mono text-[11px] text-neutral-400 mt-1.5 truncate">
                      {selected.base_url}
                    </p>
                    {selected.description ? (
                      <p className="font-mono text-[11px] text-neutral-500 mt-1 truncate">
                        {selected.description}
                      </p>
                    ) : null}
                  </div>

                  {readOnly ? null : (
                    <div className="flex items-center gap-1.5 flex-shrink-0">
                      <button
                        type="button"
                        onClick={() => handleEdit(selected)}
                        className="p-1.5 rounded text-neutral-400 hover:text-white transition-colors cursor-pointer hover:bg-white/[0.05]"
                        title="Edit provider"
                        aria-label="Edit provider"
                      >
                        <Pencil className="w-3.5 h-3.5" />
                      </button>
                      <button
                        type="button"
                        onClick={() => setDeletingProvider(selected)}
                        className="p-1.5 rounded text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer hover:bg-white/[0.05]"
                        title="Delete provider and its keys"
                        aria-label="Delete provider"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  )}
                </div>
              </div>

              <div className="rounded-xl border border-white/[0.06] bg-transparent overflow-hidden">
                <div className="px-4 py-2.5 border-b border-white/[0.06] flex items-center justify-between">
                  <span className="font-mono text-[10px] uppercase tracking-wider text-neutral-500">
                    Credentials ({keys.length.toLocaleString()})
                  </span>
                  {readOnly ? null : (
                    <Button
                      variant="minimal"
                      size="sm"
                      onClick={() => setUpsertTarget(selected)}
                      leftIcon={<KeyRound className="w-3.5 h-3.5 text-neutral-400" />}
                    >
                      Add / Update Keys
                    </Button>
                  )}
                </div>

                <ProviderKeyTable
                  providerId={selected.id}
                  keys={keys}
                  isLoading={keysQuery.isLoading}
                  readOnly={readOnly}
                  onEdit={setPatchTarget}
                  onDelete={setDeletingKey}
                  onToggleRoutable={handleToggleRoutable}
                />
              </div>
            </div>
          ) : null}
        </div>
      )}

      {/* Create / Edit Provider */}
      <ProviderFormModal
        isOpen={formOpen}
        onClose={handleCloseForm}
        provider={formTarget}
        onCreated={handleCreated}
      />

      {/* Batch Key Upsert */}
      {upsertTarget ? (
        <KeyUpsertModal
          isOpen={upsertTarget !== null}
          onClose={() => setUpsertTarget(null)}
          provider={upsertTarget}
        />
      ) : null}

      {/* Single Key Patch */}
      <KeyPatchModal
        isOpen={patchTarget !== null}
        onClose={() => setPatchTarget(null)}
        keyRecord={patchTarget}
      />

      {/* Provider Delete Confirmation */}
      <Modal
        isOpen={deletingProvider !== null}
        onClose={() => setDeletingProvider(null)}
        title="Delete Provider"
        description="Removing a provider deletes its stored credential pool."
        size="sm"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <p className="text-neutral-300">
            This permanently removes provider{' '}
            <span className="text-white font-medium">{deletingProvider?.name}</span> along with{' '}
            <span className="text-white font-medium">{keys.length}</span> stored credential(s).
          </p>
          <p className="text-neutral-500">
            Upstreams and harvesters match this provider by name; a provider still referenced by an
            upstream is refused by the server with a 409 naming the upstreams.
          </p>
          <div className="pt-2 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => setDeletingProvider(null)}
              disabled={deleteProviderMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={handleConfirmDeleteProvider}
              isLoading={deleteProviderMutation.isPending}
              className="text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20 hover:border-rose-500/40"
            >
              Delete
            </Button>
          </div>
        </div>
      </Modal>

      {/* Key Delete Confirmation */}
      <Modal
        isOpen={deletingKey !== null}
        onClose={() => setDeletingKey(null)}
        title="Delete Credential"
        description="This removes the stored key and any credential slots bound to it."
        size="sm"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <p className="text-neutral-300">
            Delete key{' '}
            <span className="text-white font-medium">
              {deletingKey?.api_key_hint || `#${deletingKey?.id}`}
            </span>
            ?
          </p>
          <p className="text-neutral-500">
            Upstreams pooling this provider lose the slot ref{' '}
            <span className="text-neutral-400">-key-{deletingKey?.id}</span> on the next catalog
            reload. To pause it instead, flip the routing toggle.
          </p>
          <div className="pt-2 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => setDeletingKey(null)}
              disabled={isMutating}
            >
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={handleConfirmDeleteKey}
              isLoading={deleteKeyMutation.isPending}
              className="text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20 hover:border-rose-500/40"
            >
              Delete
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
