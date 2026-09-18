import React, { useState, useEffect } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Coins, Calendar, PlusCircle } from 'lucide-react';
import type { TenantDTO } from '@/services/schema';
import { useTopupTenantMutation } from '@/services/api';

export interface TopupModalProps {
  isOpen: boolean;
  onClose: () => void;
  tenant: TenantDTO | null;
}

const TOKEN_TOPUP_PRESETS = [
  { label: 'None (+0)', value: 0 },
  { label: '+1M', value: 1_000_000 },
  { label: '+5M', value: 5_000_000 },
  { label: '+10M', value: 10_000_000 },
  { label: '+50M', value: 50_000_000 },
];

const EXTEND_DAYS_PRESETS = [
  { label: 'None (+0)', days: 0 },
  { label: '+7 Days', days: 7 },
  { label: '+30 Days', days: 30 },
  { label: '+90 Days', days: 90 },
  { label: '+1 Year', days: 365 },
];

export const TopupModal = React.memo(function TopupModal({
  isOpen,
  onClose,
  tenant,
}: TopupModalProps) {
  const topupMutation = useTopupTenantMutation();

  const [selectedTokensPreset, setSelectedTokensPreset] = useState<number>(5_000_000);
  const [customTokens, setCustomTokens] = useState('');

  const [selectedDaysPreset, setSelectedDaysPreset] = useState<number>(30);
  const [customDays, setCustomDays] = useState('');

  useEffect(() => {
    if (isOpen) {
      setSelectedTokensPreset(5_000_000);
      setCustomTokens('');
      setSelectedDaysPreset(30);
      setCustomDays('');
    }
  }, [isOpen, tenant]);

  if (!tenant) return null;

  const currentMax = tenant.max_tokens ?? 0;
  const currentUsed = tenant.used_tokens ?? 0;
  const isUnlimited = currentMax <= 0;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    let addTokens = selectedTokensPreset;
    if (selectedTokensPreset === -1) {
      addTokens = Number(customTokens) || 0;
    }

    let extendDays = selectedDaysPreset;
    if (selectedDaysPreset === -1) {
      extendDays = Number(customDays) || 0;
    }

    if (addTokens <= 0 && extendDays <= 0) {
      return;
    }

    await topupMutation.mutateAsync({
      api_key: tenant.api_key,
      key_hash: tenant.key_hash,
      add_tokens: addTokens > 0 ? addTokens : undefined,
      extend_days: extendDays > 0 ? extendDays : undefined,
    });

    onClose();
  };

  const formatExpiry = (epoch?: number | null) => {
    if (!epoch) return 'Never expires';
    const epochMs = epoch < 100_000_000_000 ? epoch * 1000 : epoch;
    const d = new Date(epochMs);
    const isPast = epochMs < Date.now();
    return `${d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })} ${
      isPast ? '(Expired)' : ''
    }`;
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={`Top-Up Tenant: ${tenant.name}`}
      description="Refill token quota balance and/or extend tenant API key expiration."
      size="md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        {/* Current Balance Overview */}
        <div className="p-3 rounded-xl bg-transparent border border-white/[0.06] grid grid-cols-2 gap-3 text-[11px]">
          <div>
            <span className="text-neutral-500 block">Current Quota:</span>
            <span className="text-white font-medium">
              {isUnlimited
                ? `Unlimited (${currentUsed.toLocaleString()} used)`
                : `${currentUsed.toLocaleString()} / ${currentMax.toLocaleString()}`}
            </span>
          </div>
          <div>
            <span className="text-neutral-500 block">Current Expiry:</span>
            <span className="text-white font-medium">{formatExpiry(tenant.expires_at)}</span>
          </div>
        </div>

        {/* Add Tokens Section */}
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium flex items-center gap-1.5">
            <Coins className="w-3.5 h-3.5 text-neutral-400" />
            Add Tokens
          </label>
          <div className="grid grid-cols-5 gap-1.5">
            {TOKEN_TOPUP_PRESETS.map((p) => (
              <button
                key={p.label}
                type="button"
                onClick={() => setSelectedTokensPreset(p.value)}
                className={`py-1 px-2 rounded text-[11px] border transition-colors ${
                  selectedTokensPreset === p.value
                    ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                    : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
                }`}
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-2 mt-1">
            <button
              type="button"
              onClick={() => setSelectedTokensPreset(-1)}
              className={`py-1 px-2 rounded text-[11px] border transition-colors flex-shrink-0 ${
                selectedTokensPreset === -1
                  ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                  : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
              }`}
            >
              Custom Tokens
            </button>
            {selectedTokensPreset === -1 && (
              <input
                type="number"
                min={0}
                placeholder="e.g. 2000000"
                value={customTokens}
                onChange={(e) => setCustomTokens(e.target.value)}
                className="flex-1 px-3 py-1 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            )}
          </div>
        </div>

        {/* Extend Validity Days Section */}
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium flex items-center gap-1.5">
            <Calendar className="w-3.5 h-3.5 text-neutral-400" />
            Extend Validity
          </label>
          <div className="grid grid-cols-5 gap-1.5">
            {EXTEND_DAYS_PRESETS.map((p) => (
              <button
                key={p.label}
                type="button"
                onClick={() => setSelectedDaysPreset(p.days)}
                className={`py-1 px-2 rounded text-[11px] border transition-colors ${
                  selectedDaysPreset === p.days
                    ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                    : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
                }`}
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-2 mt-1">
            <button
              type="button"
              onClick={() => setSelectedDaysPreset(-1)}
              className={`py-1 px-2 rounded text-[11px] border transition-colors flex-shrink-0 ${
                selectedDaysPreset === -1
                  ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                  : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
              }`}
            >
              Custom Days
            </button>
            {selectedDaysPreset === -1 && (
              <input
                type="number"
                min={0}
                placeholder="e.g. 60"
                value={customDays}
                onChange={(e) => setCustomDays(e.target.value)}
                className="flex-1 px-3 py-1 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            )}
          </div>
        </div>

        {/* Actions */}
        <div className="pt-4 border-t border-white/[0.04] flex items-center justify-end gap-3">
          <Button variant="minimal" size="sm" type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="minimal"
            size="sm"
            type="submit"
            isLoading={topupMutation.isPending}
            leftIcon={<PlusCircle className="w-3.5 h-3.5 text-emerald-400" />}
          >
            Apply Top-Up
          </Button>
        </div>
      </form>
    </Modal>
  );
});
