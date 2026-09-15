import React, { useState, useEffect, useCallback } from 'react';
import type { ComboDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useModels, useCombos, useStoreActions, buildSettingsPayload } from '@/core/state/store';
import { useSaveSettingsMutation } from '@/services/api';
import { Trash2, ArrowUp, ArrowDown, Check, Scale, Shuffle, ArrowRight, ShieldAlert } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface ComboModalProps {
  isOpen: boolean;
  onClose: () => void;
  comboToEdit?: ComboDTO | null;
  onDelete?: (combo: ComboDTO) => void;
}

type StrategyType = 'least_inflight' | 'round_robin' | 'failover';

export const ComboModal = React.memo(function ComboModal({
  isOpen,
  onClose,
  comboToEdit,
  onDelete,
}: ComboModalProps) {
  const models = useModels();
  const combos = useCombos();
  const { addOrUpdateCombo, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  const [name, setName] = useState('');
  const [strategy, setStrategy] = useState<StrategyType>('least_inflight');
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (comboToEdit) {
      setName(comboToEdit.name);
      setStrategy((comboToEdit.strategy as StrategyType) || 'least_inflight');
      setSelectedModels(comboToEdit.models || []);
      setEnabled(comboToEdit.enabled !== false);
      setError(null);
    } else {
      setName('');
      setStrategy('least_inflight');
      setSelectedModels(models.slice(0, 2).map((m) => m.public_name));
      setEnabled(true);
      setError(null);
    }
  }, [comboToEdit, isOpen, models]);

  const handleToggleModel = useCallback((modelPublicName: string) => {
    setSelectedModels((prev) => {
      if (prev.includes(modelPublicName)) {
        return prev.filter((m) => m !== modelPublicName);
      } else {
        return [...prev, modelPublicName];
      }
    });
  }, []);

  const handleMoveModel = useCallback((index: number, direction: 'up' | 'down') => {
    setSelectedModels((prev) => {
      const targetIndex = direction === 'up' ? index - 1 : index + 1;
      if (targetIndex < 0 || targetIndex >= prev.length) return prev;
      const next = [...prev];
      const temp = next[index];
      next[index] = next[targetIndex];
      next[targetIndex] = temp;
      return next;
    });
  }, []);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const cleanName = name.trim();

    if (!cleanName) {
      setError('Combo name is required.');
      return;
    }

    const nameRegex = /^[a-zA-Z0-9._-]+$/;
    if (!nameRegex.test(cleanName)) {
      setError('Combo name must only contain alphanumeric characters, dots, dashes, or underscores.');
      return;
    }

    if (models.some((m) => m.public_name.toLowerCase() === cleanName.toLowerCase())) {
      setError(`Name "${cleanName}" collides with an existing concrete model route.`);
      return;
    }

    if (
      (!comboToEdit || comboToEdit.name !== cleanName) &&
      combos.some((c) => c.name.toLowerCase() === cleanName.toLowerCase())
    ) {
      setError(`A combo named "${cleanName}" already exists.`);
      return;
    }

    if (selectedModels.length === 0) {
      setError('At least one concrete model must be selected for this combo.');
      return;
    }

    const updated: ComboDTO = {
      name: cleanName,
      strategy,
      models: selectedModels,
      enabled,
    };

    addOrUpdateCombo(updated);

    saveMutation.mutate(buildSettingsPayload());

    addToast({
      title: comboToEdit ? 'Combo Updated' : 'Combo Created',
      message: `Virtual combo ${cleanName} saved.`,
      type: 'success',
    });

    onClose();
  };

  const isEditing = !!comboToEdit;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={isEditing ? `Edit Combo: ${comboToEdit.name}` : 'Create Model Combo'}
      description="Pool multiple models across providers with automated load balancing and failover."
      size="md"
    >
      <form onSubmit={handleSubmit} className="space-y-4 font-mono text-xs">
        {error ? (
          <div className="p-3 rounded-lg border border-rose-500/20 bg-rose-500/10 text-rose-300 flex items-center gap-2">
            <ShieldAlert className="w-4 h-4 text-rose-400 shrink-0" />
            <span>{error}</span>
          </div>
        ) : null}

        {/* Combo Public ID */}
        <div className="space-y-1.5">
          <label className="text-neutral-400 block font-medium">
            Public Model ID (Client Identifier) *
          </label>
          <input
            type="text"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setError(null);
            }}
            disabled={isEditing}
            placeholder="e.g. combo-deepseek, fast-pool"
            className={cn(
              'w-full px-3 py-1.5 rounded-lg bg-transparent border text-white font-mono text-xs focus:outline-none transition-colors',
              isEditing
                ? 'border-white/[0.04] text-neutral-500 cursor-not-allowed'
                : 'border-white/[0.08] focus:border-white/20'
            )}
          />
          <p className="text-[10px] text-neutral-500">
            Clients specify this identifier directly in the <code className="text-neutral-400">model</code> request field.
          </p>
        </div>

        {/* Load Balancing Strategy Selector */}
        <div className="space-y-2">
          <label className="text-neutral-400 block font-medium">Routing Strategy</label>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
            {/* Least Inflight */}
            <button
              type="button"
              onClick={() => setStrategy('least_inflight')}
              className={cn(
                'p-2.5 rounded-lg border text-left transition-all cursor-pointer flex flex-col justify-between',
                strategy === 'least_inflight'
                  ? 'bg-white/[0.08] border-white/25 text-white'
                  : 'bg-transparent border-white/[0.06] text-neutral-400 hover:border-white/12 hover:text-neutral-200'
              )}
            >
              <div className="flex items-center gap-1.5 font-medium text-[11px]">
                <Scale className="w-3.5 h-3.5 text-neutral-400" />
                <span>Least Inflight</span>
              </div>
              <p className="text-[10px] text-neutral-500 mt-1 leading-relaxed">
                Routes to model whose upstream has lowest active connections.
              </p>
            </button>

            {/* Round Robin */}
            <button
              type="button"
              onClick={() => setStrategy('round_robin')}
              className={cn(
                'p-2.5 rounded-lg border text-left transition-all cursor-pointer flex flex-col justify-between',
                strategy === 'round_robin'
                  ? 'bg-white/[0.08] border-white/25 text-white'
                  : 'bg-transparent border-white/[0.06] text-neutral-400 hover:border-white/12 hover:text-neutral-200'
              )}
            >
              <div className="flex items-center gap-1.5 font-medium text-[11px]">
                <Shuffle className="w-3.5 h-3.5 text-neutral-400" />
                <span>Round Robin</span>
              </div>
              <p className="text-[10px] text-neutral-500 mt-1 leading-relaxed">
                Distributes calls cyclically across healthy member models.
              </p>
            </button>

            {/* Failover */}
            <button
              type="button"
              onClick={() => setStrategy('failover')}
              className={cn(
                'p-2.5 rounded-lg border text-left transition-all cursor-pointer flex flex-col justify-between',
                strategy === 'failover'
                  ? 'bg-white/[0.08] border-white/25 text-white'
                  : 'bg-transparent border-white/[0.06] text-neutral-400 hover:border-white/12 hover:text-neutral-200'
              )}
            >
              <div className="flex items-center gap-1.5 font-medium text-[11px]">
                <ArrowRight className="w-3.5 h-3.5 text-neutral-400" />
                <span>Failover</span>
              </div>
              <p className="text-[10px] text-neutral-500 mt-1 leading-relaxed">
                Tries primary model, falling back down the chain if breaker opens.
              </p>
            </button>
          </div>
        </div>

        {/* Member Models Picker & Orderer */}
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-neutral-400 block font-medium">
              Member Models ({selectedModels.length} selected) *
            </label>
            <span className="text-[10px] text-neutral-500">
              {strategy === 'failover' ? 'First model is primary' : 'Pooled models'}
            </span>
          </div>

          {/* Selected Ordered List */}
          {selectedModels.length > 0 ? (
            <div className="space-y-1.5 p-2 rounded-lg bg-white/[0.02] border border-white/[0.06]">
              {selectedModels.map((modName, idx) => {
                const targetMod = models.find((m) => m.public_name === modName);
                return (
                  <div
                    key={modName}
                    className="flex items-center justify-between px-2.5 py-1.5 rounded bg-white/[0.03] border border-white/[0.04]"
                  >
                    <div className="flex items-center gap-2">
                      <span className="w-4 text-center text-neutral-500 text-[10px] tabular-nums">
                        {idx + 1}.
                      </span>
                      <span className="text-white font-medium">{modName}</span>
                      <span className="text-[11px] text-neutral-500">
                        ➔ {targetMod?.upstream || 'unknown'}
                      </span>
                    </div>

                    <div className="flex items-center gap-1">
                      <button
                        type="button"
                        onClick={() => handleMoveModel(idx, 'up')}
                        disabled={idx === 0}
                        className="p-1 text-neutral-400 hover:text-white disabled:opacity-20 transition-opacity"
                        title="Move Up"
                      >
                        <ArrowUp className="w-3 h-3" />
                      </button>
                      <button
                        type="button"
                        onClick={() => handleMoveModel(idx, 'down')}
                        disabled={idx === selectedModels.length - 1}
                        className="p-1 text-neutral-400 hover:text-white disabled:opacity-20 transition-opacity"
                        title="Move Down"
                      >
                        <ArrowDown className="w-3 h-3" />
                      </button>
                      <button
                        type="button"
                        onClick={() => handleToggleModel(modName)}
                        className="p-1 text-neutral-500 hover:text-rose-400 ml-1"
                        title="Remove from pool"
                      >
                        <Trash2 className="w-3 h-3" />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="p-3 text-center rounded-lg border border-dashed border-white/[0.08] text-neutral-500 text-[11px]">
              No models selected. Choose from available models below.
            </div>
          )}

          {/* Available Models to Add */}
          <div className="space-y-1 pt-1">
            <span className="text-[10px] text-neutral-500 block">Available Models:</span>
            <div className="flex flex-wrap gap-1.5">
              {models.map((m) => {
                const isSelected = selectedModels.includes(m.public_name);
                return (
                  <button
                    key={m.public_name}
                    type="button"
                    onClick={() => handleToggleModel(m.public_name)}
                    className={cn(
                      'flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[11px] border transition-all cursor-pointer',
                      isSelected
                        ? 'bg-white/[0.08] border-white/25 text-white'
                        : 'bg-transparent border-white/[0.06] text-neutral-400 hover:border-white/12 hover:text-neutral-200'
                    )}
                  >
                    {isSelected ? <Check className="w-3 h-3 text-neutral-300" /> : null}
                    <span>{m.public_name}</span>
                    <span className="text-neutral-500 text-[10px]">({m.upstream})</span>
                  </button>
                );
              })}
            </div>
          </div>
        </div>

        {/* Enabled Status Toggle */}
        <div className="flex items-center justify-between py-2 border-t border-white/[0.04]">
          <div className="space-y-0.5">
            <span className="text-neutral-300 block font-medium">Enable Route</span>
            <span className="text-[10px] text-neutral-500 block">
              When disabled, requests to this combo return HTTP 404/503.
            </span>
          </div>

          <button
            type="button"
            onClick={() => setEnabled((e) => !e)}
            className={cn(
              'w-9 h-5 rounded-full transition-colors relative cursor-pointer',
              enabled ? 'bg-emerald-500' : 'bg-neutral-700'
            )}
          >
            <span
              className={cn(
                'w-3.5 h-3.5 rounded-full bg-white absolute top-0.5 transition-transform',
                enabled ? 'left-4.5' : 'left-1'
              )}
            />
          </button>
        </div>

        {/* Footer Actions */}
        <div className="pt-3 border-t border-white/[0.06] flex items-center justify-between">
          {isEditing && onDelete ? (
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => {
                onDelete(comboToEdit);
                onClose();
              }}
              className="text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20"
              leftIcon={<Trash2 className="w-3.5 h-3.5" />}
            >
              Delete Combo
            </Button>
          ) : (
            <div />
          )}

          <div className="flex items-center gap-2">
            <Button variant="minimal" size="sm" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="submit"
              isLoading={saveMutation.isPending}
            >
              {isEditing ? 'Save Changes' : 'Create Combo'}
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
});
