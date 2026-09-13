import React, { useState, useMemo, useTransition, useCallback } from 'react';
import { useModels, useCombos, useStoreActions, useAppStore } from '@/core/state/store';
import type { ModelDTO, ComboDTO } from '@/services/schema';
import { ModelCard } from './ModelCard';
import { ModelModal } from './ModelModal';
import { ComboCard } from './ComboCard';
import { ComboModal } from './ComboModal';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useSaveSettingsMutation } from '@/services/api';
import { Plus, Search, Layers, Network } from 'lucide-react';
import { cn } from '@/lib/utils';

export default function ModelsView() {
  const models = useModels();
  const combos = useCombos();
  const { addOrUpdateModel, removeModel, addOrUpdateCombo, removeCombo, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  // Sub-tab selection: 'models' vs 'combos'
  const [activeSubTab, setActiveSubTab] = useState<'models' | 'combos'>('models');
  const [searchQuery, setSearchQuery] = useState('');

  // Model modal states
  const [modelModalOpen, setModelModalOpen] = useState(false);
  const [editingModel, setEditingModel] = useState<ModelDTO | null>(null);
  const [deletingModel, setDeletingModel] = useState<ModelDTO | null>(null);

  // Combo modal states
  const [comboModalOpen, setComboModalOpen] = useState(false);
  const [editingCombo, setEditingCombo] = useState<ComboDTO | null>(null);
  const [deletingCombo, setDeletingCombo] = useState<ComboDTO | null>(null);

  const [, startTransition] = useTransition();

  // Search filter query wrapped in transition
  const handleSearchChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const val = e.target.value;
    startTransition(() => {
      setSearchQuery(val);
    });
  };

  // Vercel Best Practice: rerender-derived-state-no-effect (Derived during render)
  const filteredModels = useMemo(() => {
    if (!searchQuery.trim()) return models;
    const q = searchQuery.toLowerCase();
    return models.filter(
      (m) =>
        m.public_name.toLowerCase().includes(q) ||
        m.upstream.toLowerCase().includes(q) ||
        m.upstream_model.toLowerCase().includes(q)
    );
  }, [models, searchQuery]);

  const filteredCombos = useMemo(() => {
    if (!searchQuery.trim()) return combos;
    const q = searchQuery.toLowerCase();
    return combos.filter(
      (c) =>
        c.name.toLowerCase().includes(q) ||
        (c.strategy && c.strategy.toLowerCase().includes(q)) ||
        c.models.some((m) => m.toLowerCase().includes(q))
    );
  }, [combos, searchQuery]);

  // Model actions
  const handleToggleModelEnabled = useCallback(
    (publicName: string, enabled: boolean) => {
      const target = models.find((m) => m.public_name === publicName);
      if (!target) return;

      const updated: ModelDTO = { ...target, enabled };
      addOrUpdateModel(updated);

      const currentSettings = {
        upstreams: useAppStore.getState().upstreams,
        models: useAppStore.getState().models,
        tenants: useAppStore.getState().tenants,
        combos: useAppStore.getState().combos,
      };
      saveMutation.mutate(currentSettings);
    },
    [models, addOrUpdateModel, saveMutation]
  );

  const handleCreateModel = useCallback(() => {
    setEditingModel(null);
    setModelModalOpen(true);
  }, []);

  const handleEditModel = useCallback((model: ModelDTO) => {
    setEditingModel(model);
    setModelModalOpen(true);
  }, []);

  const handleCloseModelModal = useCallback(() => {
    setModelModalOpen(false);
    setEditingModel(null);
  }, []);

  const handleDeleteModelRequest = useCallback((model: ModelDTO) => {
    setDeletingModel(model);
  }, []);

  const handleConfirmDeleteModel = useCallback(() => {
    if (!deletingModel) return;
    const publicName = deletingModel.public_name;
    removeModel(publicName);

    const currentSettings = {
      upstreams: useAppStore.getState().upstreams,
      models: useAppStore.getState().models.filter((m) => m.public_name !== publicName),
      tenants: useAppStore.getState().tenants,
      combos: useAppStore.getState().combos,
    };
    saveMutation.mutate(currentSettings);

    addToast({
      title: 'Model Route Deleted',
      message: `${publicName} has been removed.`,
      type: 'info',
    });

    setDeletingModel(null);
  }, [deletingModel, removeModel, saveMutation, addToast]);

  // Combo actions
  const handleToggleComboEnabled = useCallback(
    (name: string, enabled: boolean) => {
      const target = combos.find((c) => c.name === name);
      if (!target) return;

      const updated: ComboDTO = { ...target, enabled };
      addOrUpdateCombo(updated);

      const currentSettings = {
        upstreams: useAppStore.getState().upstreams,
        models: useAppStore.getState().models,
        tenants: useAppStore.getState().tenants,
        combos: useAppStore.getState().combos,
      };
      saveMutation.mutate(currentSettings);
    },
    [combos, addOrUpdateCombo, saveMutation]
  );

  const handleCreateCombo = useCallback(() => {
    setEditingCombo(null);
    setComboModalOpen(true);
  }, []);

  const handleEditCombo = useCallback((combo: ComboDTO) => {
    setEditingCombo(combo);
    setComboModalOpen(true);
  }, []);

  const handleCloseComboModal = useCallback(() => {
    setComboModalOpen(false);
    setEditingCombo(null);
  }, []);

  const handleDeleteComboRequest = useCallback((combo: ComboDTO) => {
    setDeletingCombo(combo);
  }, []);

  const handleConfirmDeleteCombo = useCallback(() => {
    if (!deletingCombo) return;
    const comboName = deletingCombo.name;
    removeCombo(comboName);

    const currentSettings = {
      upstreams: useAppStore.getState().upstreams,
      models: useAppStore.getState().models,
      tenants: useAppStore.getState().tenants,
      combos: useAppStore.getState().combos.filter((c) => c.name !== comboName),
    };
    saveMutation.mutate(currentSettings);

    addToast({
      title: 'Combo Deleted',
      message: `Virtual combo ${comboName} has been removed.`,
      type: 'info',
    });

    setDeletingCombo(null);
  }, [deletingCombo, removeCombo, saveMutation, addToast]);

  return (
    <div className="flex flex-col gap-6 w-full animate-in fade-in duration-300">
      {/* Top Header: SubTab Switcher & Actions */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        {/* Left: View Mode Tabs (Models vs Combos) */}
        <div className="flex items-center p-1 rounded-xl bg-white/[0.02] border border-white/[0.06] font-mono text-xs">
          <button
            type="button"
            onClick={() => setActiveSubTab('models')}
            className={cn(
              'flex items-center gap-2 px-3 py-1.5 rounded-lg transition-all cursor-pointer',
              activeSubTab === 'models'
                ? 'bg-white/[0.08] text-white font-medium shadow-sm'
                : 'text-neutral-400 hover:text-neutral-200'
            )}
          >
            <Layers className="w-3.5 h-3.5 text-neutral-400" />
            <span>Model Routes</span>
            <span className="text-[10px] px-1.5 py-0.2 rounded-full bg-white/[0.06] text-neutral-400 tabular-nums">
              {models.length}
            </span>
          </button>

          <button
            type="button"
            onClick={() => setActiveSubTab('combos')}
            className={cn(
              'flex items-center gap-2 px-3 py-1.5 rounded-lg transition-all cursor-pointer',
              activeSubTab === 'combos'
                ? 'bg-white/[0.08] text-white font-medium shadow-sm'
                : 'text-neutral-400 hover:text-neutral-200'
            )}
          >
            <Network className="w-3.5 h-3.5 text-neutral-400" />
            <span>Virtual Combos</span>
            <span className="text-[10px] px-1.5 py-0.2 rounded-full bg-white/[0.06] text-neutral-400 tabular-nums">
              {combos.length}
            </span>
          </button>
        </div>

        {/* Right: Search Input & Create Button */}
        <div className="flex items-center gap-3">
          <div className="relative">
            <Search className="w-3.5 h-3.5 text-neutral-400 absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              placeholder={activeSubTab === 'models' ? 'Search route or target...' : 'Search combo or model...'}
              value={searchQuery}
              onChange={handleSearchChange}
              className="pl-8 pr-3.5 py-1.5 rounded-lg bg-transparent border border-white/[0.08] focus:border-white/20 text-neutral-200 placeholder:text-neutral-500 font-mono text-xs focus:outline-none w-52 sm:w-64 transition-colors"
            />
          </div>

          {activeSubTab === 'models' ? (
            <Button
              variant="minimal"
              size="sm"
              onClick={handleCreateModel}
              leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              Add Route
            </Button>
          ) : (
            <Button
              variant="minimal"
              size="sm"
              onClick={handleCreateCombo}
              leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              Add Combo
            </Button>
          )}
        </div>
      </div>

      {/* Main Content Area */}
      {activeSubTab === 'models' ? (
        /* Models Grid */
        filteredModels.length > 0 ? (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            {filteredModels.map((m) => (
              <ModelCard
                key={m.public_name}
                model={m}
                onToggleEnabled={handleToggleModelEnabled}
                onEdit={handleEditModel}
                onDelete={handleDeleteModelRequest}
              />
            ))}
          </div>
        ) : (
          <div className="p-12 text-center flex flex-col items-center justify-center font-mono rounded-xl border border-white/[0.06] bg-transparent">
            <Layers className="w-8 h-8 text-neutral-500 mb-3" />
            <h3 className="text-sm font-medium text-neutral-200">No Model Routes Found</h3>
            <p className="text-xs text-neutral-500 mt-1 max-w-sm">
              {searchQuery
                ? `No model route matches "${searchQuery}". Try another keyword.`
                : 'Create your first public model route to begin proxying requests.'}
            </p>
            <Button
              variant="minimal"
              size="sm"
              onClick={handleCreateModel}
              leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
              className="mt-4"
            >
              Add Route
            </Button>
          </div>
        )
      ) : (
        /* Combos Grid */
        filteredCombos.length > 0 ? (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            {filteredCombos.map((c) => (
              <ComboCard
                key={c.name}
                combo={c}
                onToggleEnabled={handleToggleComboEnabled}
                onEdit={handleEditCombo}
                onDelete={handleDeleteComboRequest}
              />
            ))}
          </div>
        ) : (
          <div className="p-12 text-center flex flex-col items-center justify-center font-mono rounded-xl border border-white/[0.06] bg-transparent">
            <Network className="w-8 h-8 text-neutral-500 mb-3" />
            <h3 className="text-sm font-medium text-neutral-200">No Virtual Combos Found</h3>
            <p className="text-xs text-neutral-500 mt-1 max-w-sm">
              {searchQuery
                ? `No combo matches "${searchQuery}". Try another keyword.`
                : 'Create a Virtual Model Combo to pool multiple models across providers with automated load balancing.'}
            </p>
            <Button
              variant="minimal"
              size="sm"
              onClick={handleCreateCombo}
              leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
              className="mt-4"
            >
              Add Combo
            </Button>
          </div>
        )
      )}

      {/* Central Configuration Modals */}
      <ModelModal
        isOpen={modelModalOpen}
        onClose={handleCloseModelModal}
        modelToEdit={editingModel}
        onDelete={handleDeleteModelRequest}
      />

      <ComboModal
        isOpen={comboModalOpen}
        onClose={handleCloseComboModal}
        comboToEdit={editingCombo}
        onDelete={handleDeleteComboRequest}
      />

      {/* Model Delete Confirmation Modal */}
      <Modal
        isOpen={deletingModel !== null}
        onClose={() => setDeletingModel(null)}
        title="Delete Model Route"
        description="Are you sure you want to remove this model route?"
        size="sm"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <p className="text-neutral-300">
            This will permanently remove model route{' '}
            <span className="text-white font-medium">{deletingModel?.public_name}</span> from the gateway routing configuration.
          </p>

          <div className="pt-2 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => setDeletingModel(null)}
              disabled={saveMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={handleConfirmDeleteModel}
              isLoading={saveMutation.isPending}
              className="text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20 hover:border-rose-500/40"
            >
              Delete
            </Button>
          </div>
        </div>
      </Modal>

      {/* Combo Delete Confirmation Modal */}
      <Modal
        isOpen={deletingCombo !== null}
        onClose={() => setDeletingCombo(null)}
        title="Delete Virtual Combo"
        description="Are you sure you want to remove this virtual combo?"
        size="sm"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <p className="text-neutral-300">
            This will permanently remove virtual combo{' '}
            <span className="text-white font-medium">{deletingCombo?.name}</span>. Clients targeting this combo name will receive HTTP 404.
          </p>

          <div className="pt-2 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => setDeletingCombo(null)}
              disabled={saveMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={handleConfirmDeleteCombo}
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
