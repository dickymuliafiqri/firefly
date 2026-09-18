import React, { useState, useEffect } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Copy, Check, Coins, Calendar, CheckCircle2 } from 'lucide-react';
import type { TenantDTO } from '@/services/schema';
import { useStoreActions, buildSettingsPayload } from '@/core/state/store';
import { useSaveSettingsMutation } from '@/services/api';
import { generateRandomGatewayKey } from '@/lib/crypto';
import { copyToClipboard } from '@/lib/utils';

export interface KeyGeneratorModalProps {
  isOpen: boolean;
  onClose: () => void;
}

const QUOTA_PRESETS = [
  { label: 'Unlimited', value: 0 },
  { label: '1M', value: 1_000_000 },
  { label: '5M', value: 5_000_000 },
  { label: '10M', value: 10_000_000 },
  { label: '50M', value: 50_000_000 },
];

const EXPIRY_PRESETS = [
  { label: 'No Expiry', days: 0 },
  { label: '7 Days', days: 7 },
  { label: '30 Days', days: 30 },
  { label: '90 Days', days: 90 },
  { label: '1 Year', days: 365 },
];

export const KeyGeneratorModal = React.memo(function KeyGeneratorModal({
  isOpen,
  onClose,
}: KeyGeneratorModalProps) {
  const { addOrUpdateTenant, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  const [tenantName, setTenantName] = useState('');
  const [allowedModels, setAllowedModels] = useState('*');
  const [rps, setRps] = useState('20');
  const [maxConcurrent, setMaxConcurrent] = useState('20');

  // Token Quota state
  const [selectedQuotaPreset, setSelectedQuotaPreset] = useState<number>(10_000_000);
  const [customQuota, setCustomQuota] = useState('');

  // Expiration state
  const [selectedExpiryPreset, setSelectedExpiryPreset] = useState<number>(30);
  const [customExpiryDays, setCustomExpiryDays] = useState('');

  // Generated state
  const [generatedKey, setGeneratedKey] = useState('');
  const [summaryInfo, setSummaryInfo] = useState<{ quotaText: string; expiryText: string }>({
    quotaText: '',
    expiryText: '',
  });
  const [copied, setCopied] = useState(false);
  const [step, setStep] = useState<'configure' | 'revealed'>('configure');

  useEffect(() => {
    if (isOpen) {
      setTenantName('');
      setAllowedModels('*');
      setRps('20');
      setMaxConcurrent('20');
      setSelectedQuotaPreset(10_000_000);
      setCustomQuota('');
      setSelectedExpiryPreset(30);
      setCustomExpiryDays('');
      setGeneratedKey('');
      setCopied(false);
      setStep('configure');
    }
  }, [isOpen]);

  const handleGenerate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!tenantName.trim()) return;

    const parsedRps = Number(rps);
    const parsedMaxConcurrent = Number(maxConcurrent);
    if (!Number.isFinite(parsedRps) || parsedRps < 1 || !Number.isFinite(parsedMaxConcurrent) || parsedMaxConcurrent < 1) {
      addToast({
        title: 'Validation Error',
        message: 'RPS and max concurrent must each be at least 1.',
        type: 'error',
      });
      return;
    }

    // Determine max tokens
    let maxTokens = selectedQuotaPreset;
    if (selectedQuotaPreset === -1) {
      const parsedCustom = Number(customQuota);
      if (!Number.isFinite(parsedCustom) || parsedCustom < 0) {
        addToast({
          title: 'Validation Error',
          message: 'Please enter a valid token quota (>= 0).',
          type: 'error',
        });
        return;
      }
      maxTokens = Math.floor(parsedCustom);
    }

    // Determine expiration
    let expiryDays = selectedExpiryPreset;
    if (selectedExpiryPreset === -1) {
      const parsedDays = Number(customExpiryDays);
      if (!Number.isFinite(parsedDays) || parsedDays < 0) {
        addToast({
          title: 'Validation Error',
          message: 'Please enter a valid number of days for expiration.',
          type: 'error',
        });
        return;
      }
      expiryDays = Math.floor(parsedDays);
    }

    const nowSec = Math.floor(Date.now() / 1000);
    const expiresAt = expiryDays > 0 ? nowSec + expiryDays * 86400 : null;

    // Plaintext API Key generation
    const rawKey = generateRandomGatewayKey();

    const modelsList =
      allowedModels.trim() === '*'
        ? ['*']
        : allowedModels
            .split(',')
            .map((m) => m.trim())
            .filter(Boolean);

    const newTenant: TenantDTO = {
      name: tenantName.trim(),
      api_key: rawKey,
      status: 'active',
      allowed_models: modelsList,
      max_tokens: maxTokens,
      used_tokens: 0,
      expires_at: expiresAt,
      rate_limit: {
        rps: parsedRps,
        burst: parsedRps * 2,
        max_concurrent: parsedMaxConcurrent,
      },
    };

    // Save to Zustand and Go backend
    addOrUpdateTenant(newTenant);
    saveMutation.mutate(buildSettingsPayload());

    const quotaText = maxTokens === 0 ? 'Unlimited Tokens' : `${maxTokens.toLocaleString()} Tokens`;
    const expiryText =
      expiresAt === null
        ? 'Never expires'
        : new Date(expiresAt * 1000).toLocaleDateString(undefined, {
            year: 'numeric',
            month: 'short',
            day: 'numeric',
          });

    setGeneratedKey(rawKey);
    setSummaryInfo({ quotaText, expiryText });
    setStep('revealed');
  };

  const handleCopy = async () => {
    const success = await copyToClipboard(generatedKey);
    if (success) {
      setCopied(true);
      addToast({
        title: 'Copied to Clipboard',
        message: 'API key copied successfully.',
        type: 'success',
      });
      setTimeout(() => setCopied(false), 2000);
    } else {
      addToast({
        title: 'Copy Failed',
        message: 'Unable to copy to clipboard automatically. Please copy the key manually.',
        type: 'error',
      });
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={step === 'configure' ? 'Issue Commercial Tenant API Key' : 'Tenant API Key Issued'}
      description={
        step === 'configure'
          ? 'Provision plain API key with token quota and expiration for commercial sale or team access.'
          : 'The API key is ready. Share this key with your customer or tenant.'
      }
      size="md"
    >
      {step === 'configure' ? (
        <form onSubmit={handleGenerate} className="flex flex-col gap-4 font-mono text-xs">
          {/* Tenant Name */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Tenant / Customer Name</label>
            <input
              type="text"
              required
              value={tenantName}
              onChange={(e) => setTenantName(e.target.value)}
              placeholder="e.g. VIP Customer - Enterprise Plan"
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
          </div>

          {/* Token Quota */}
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <label className="text-neutral-400 font-medium flex items-center gap-1.5">
                <Coins className="w-3.5 h-3.5 text-neutral-400" />
                Token Quota (Prompt + Completion)
              </label>
            </div>
            <div className="grid grid-cols-5 gap-1.5">
              {QUOTA_PRESETS.map((p) => (
                <button
                  key={p.label}
                  type="button"
                  onClick={() => setSelectedQuotaPreset(p.value)}
                  className={`py-1 px-2 rounded text-[11px] border transition-colors ${
                    selectedQuotaPreset === p.value
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
                onClick={() => setSelectedQuotaPreset(-1)}
                className={`py-1 px-2 rounded text-[11px] border transition-colors flex-shrink-0 ${
                  selectedQuotaPreset === -1
                    ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                    : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
                }`}
              >
                Custom Quota
              </button>
              {selectedQuotaPreset === -1 && (
                <input
                  type="number"
                  min={0}
                  placeholder="e.g. 2500000"
                  value={customQuota}
                  onChange={(e) => setCustomQuota(e.target.value)}
                  className="flex-1 px-3 py-1 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                />
              )}
            </div>
          </div>

          {/* Expiration */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium flex items-center gap-1.5">
              <Calendar className="w-3.5 h-3.5 text-neutral-400" />
              Validity / Expiration
            </label>
            <div className="grid grid-cols-5 gap-1.5">
              {EXPIRY_PRESETS.map((p) => (
                <button
                  key={p.label}
                  type="button"
                  onClick={() => setSelectedExpiryPreset(p.days)}
                  className={`py-1 px-2 rounded text-[11px] border transition-colors ${
                    selectedExpiryPreset === p.days
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
                onClick={() => setSelectedExpiryPreset(-1)}
                className={`py-1 px-2 rounded text-[11px] border transition-colors flex-shrink-0 ${
                  selectedExpiryPreset === -1
                    ? 'border-white/40 bg-white/[0.08] text-white font-medium'
                    : 'border-white/[0.06] text-neutral-400 hover:text-white hover:bg-white/[0.03]'
                }`}
              >
                Custom Days
              </button>
              {selectedExpiryPreset === -1 && (
                <input
                  type="number"
                  min={1}
                  placeholder="e.g. 45"
                  value={customExpiryDays}
                  onChange={(e) => setCustomExpiryDays(e.target.value)}
                  className="flex-1 px-3 py-1 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                />
              )}
            </div>
          </div>

          {/* Allowed Models */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Allowed Models</label>
            <input
              type="text"
              required
              value={allowedModels}
              onChange={(e) => setAllowedModels(e.target.value)}
              placeholder="* for all, or comma-separated: gpt-4o, claude-3-5-sonnet"
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
          </div>

          {/* Concurrency & RPS */}
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <label className="text-neutral-400 font-medium">Rate Limit (RPS)</label>
              <input
                type="number"
                min={1}
                max={5000}
                value={rps}
                onChange={(e) => setRps(e.target.value)}
                className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            </div>

            <div className="flex flex-col gap-1.5">
              <label className="text-neutral-400 font-medium">Max Concurrent In-Flight</label>
              <input
                type="number"
                min={1}
                max={500}
                value={maxConcurrent}
                onChange={(e) => setMaxConcurrent(e.target.value)}
                className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            </div>
          </div>

          <div className="pt-4 border-t border-white/[0.04] flex items-center justify-end gap-3">
            <Button variant="minimal" size="sm" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="minimal" size="sm" type="submit" isLoading={saveMutation.isPending}>
              Generate Plain Key
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-col gap-4 font-mono text-xs">
          {/* Success Banner */}
          <div className="p-3.5 rounded-xl bg-transparent border border-emerald-500/20 text-emerald-300 flex items-start gap-2.5">
            <CheckCircle2 className="w-4 h-4 flex-shrink-0 mt-0.5 text-emerald-400" />
            <div>
              <strong className="block font-medium">API Key Successfully Provisioned</strong>
              <span className="text-neutral-400 text-[11px]">
                The key is saved in plaintext for transparent credential and commercial management.
              </span>
            </div>
          </div>

          {/* Generated Plaintext Key Box */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Tenant API Key</label>
            <div className="flex items-center gap-2 p-2.5 rounded-xl bg-transparent border border-white/[0.08]">
              <input
                type="text"
                readOnly
                value={generatedKey}
                className="flex-1 bg-transparent text-white font-mono text-xs focus:outline-none select-all"
              />
              <Button
                variant="minimal"
                size="sm"
                onClick={handleCopy}
                leftIcon={copied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
              >
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
          </div>

          {/* Commercial Package Summary */}
          <div className="p-3 rounded-xl bg-transparent border border-white/[0.04] grid grid-cols-2 gap-2 text-[11px]">
            <div>
              <span className="text-neutral-500 block">Token Quota:</span>
              <span className="text-white font-medium">{summaryInfo.quotaText}</span>
            </div>
            <div>
              <span className="text-neutral-500 block">Expiration:</span>
              <span className="text-white font-medium">{summaryInfo.expiryText}</span>
            </div>
          </div>

          <div className="pt-2 flex justify-end">
            <Button variant="minimal" size="sm" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
});
