import { useState, useCallback } from 'react';
import { useUpstreams, useUpstreamBreakers, useStoreActions, useAppStore } from '@/core/state/store';
import type { UpstreamDTO } from '@/services/schema';
import { UpstreamCard } from './UpstreamCard';
import { UpstreamModal } from './UpstreamModal';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useSaveSettingsMutation, useOAuthConnectionsQuery } from '@/services/api';
import { Plus, Server } from 'lucide-react';

export default function UpstreamsView() {
  const upstreams = useUpstreams();
  const upstreamBreakers = useUpstreamBreakers();
  const { toggleUpstreamBreaker, removeUpstream, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();
  const { data: oauthConnections = [] } = useOAuthConnectionsQuery();

  const [modalOpen, setModalOpen] = useState(false);
  const [editingUpstream, setEditingUpstream] = useState<UpstreamDTO | null>(null);
  const [deletingUpstream, setDeletingUpstream] = useState<UpstreamDTO | null>(null);

  const handleCreate = useCallback(() => {
    setEditingUpstream(null);
    setModalOpen(true);
  }, []);

  const handleEdit = useCallback((upstream: UpstreamDTO) => {
    setEditingUpstream(upstream);
    setModalOpen(true);
  }, []);

  const handleClose = useCallback(() => {
    setModalOpen(false);
    setEditingUpstream(null);
  }, []);

  const handleDeleteRequest = useCallback((upstream: UpstreamDTO) => {
    setDeletingUpstream(upstream);
  }, []);

  const handleConfirmDelete = useCallback(() => {
    if (!deletingUpstream) return;
    const name = deletingUpstream.name;
    removeUpstream(name);

    const currentSettings = {
      upstreams: useAppStore.getState().upstreams.filter((u) => u.name !== name),
      models: useAppStore.getState().models,
      tenants: useAppStore.getState().tenants,
      combos: useAppStore.getState().combos,
    };
    saveMutation.mutate(currentSettings);

    addToast({
      title: 'Upstream Deleted',
      message: `${name} has been removed.`,
      type: 'info',
    });

    setDeletingUpstream(null);
  }, [deletingUpstream, removeUpstream, saveMutation, addToast]);

  const handleToggleBreaker = useCallback(
    (name: string) => {
      const currentState = upstreamBreakers[name] || 'CLOSED';
      const nextState = currentState === 'OPEN' ? 'CLOSED' : 'OPEN';
      toggleUpstreamBreaker(name);

      if (nextState === 'CLOSED') {
        addToast({
          title: name,
          message: 'Upstream activated.',
          type: 'success',
        });
      } else {
        addToast({
          title: name,
          message: 'Upstream disabled.',
          type: 'info',
        });
      }
    },
    [upstreamBreakers, toggleUpstreamBreaker, addToast]
  );

  return (
    <div className="flex flex-col gap-6 w-full animate-in fade-in duration-300">
      {/* Top Action Bar */}
      <div className="flex items-center justify-end">
        <Button
          variant="minimal"
          size="sm"
          onClick={handleCreate}
          leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
        >
          Add Upstream
        </Button>
      </div>

      {/* Grid of Upstream Cards */}
      {upstreams.length > 0 ? (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          {upstreams.map((u) => (
            <UpstreamCard
              key={u.name}
              upstream={u}
              breakerState={upstreamBreakers[u.name] || (u.enabled === false ? 'OPEN' : 'CLOSED')}
              connections={oauthConnections}
              onToggleBreaker={handleToggleBreaker}
              onEdit={handleEdit}
              onDelete={handleDeleteRequest}
            />
          ))}
        </div>
      ) : (
        <div className="p-12 text-center flex flex-col items-center justify-center font-mono rounded-xl border border-white/[0.06] bg-transparent">
          <Server className="w-8 h-8 text-neutral-500 mb-3" />
          <h3 className="text-sm font-medium text-neutral-200">No Upstreams Configured</h3>
          <p className="text-xs text-neutral-500 mt-1 max-w-sm">
            Add your first OpenAI, Anthropic, or OAuth provider upstream (Google Antigravity, Cline, CodeBuddy) to start routing traffic.
          </p>
          <Button
            variant="minimal"
            size="sm"
            onClick={handleCreate}
            leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            className="mt-4"
          >
            Add Upstream
          </Button>
        </div>
      )}

      {/* Central Configuration Modal */}
      <UpstreamModal
        isOpen={modalOpen}
        onClose={handleClose}
        upstreamToEdit={editingUpstream}
        onDelete={handleDeleteRequest}
      />

      {/* Delete Confirmation Modal */}
      <Modal
        isOpen={deletingUpstream !== null}
        onClose={() => setDeletingUpstream(null)}
        title="Delete Upstream"
        description="Are you sure you want to remove this upstream configuration?"
        size="sm"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <p className="text-neutral-300">
            This will permanently remove upstream <span className="text-white font-medium">{deletingUpstream?.name}</span> from the gateway routing configuration.
          </p>

          <div className="pt-2 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => setDeletingUpstream(null)}
              disabled={saveMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={handleConfirmDelete}
              isLoading={saveMutation.isPending}
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
