import React, { useState, useEffect } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Copy, Check, AlertTriangle } from 'lucide-react';
import type { TenantDTO } from '@/services/schema';
import { useStoreActions, buildSettingsPayload } from '@/core/state/store';
import { useSaveSettingsMutation } from '@/services/api';
import { computeSha256Hex, generateRandomGatewayKey } from '@/lib/crypto';
import { copyToClipboard } from '@/lib/utils';

export interface KeyGeneratorModalProps {
  isOpen: boolean;
  onClose: () => void;
}

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

  const [generatedKey, setGeneratedKey] = useState('');
  const [generatedHash, setGeneratedHash] = useState('');
  const [copied, setCopied] = useState(false);
  const [step, setStep] = useState<'configure' | 'revealed'>('configure');

  useEffect(() => {
    if (isOpen) {
      setTenantName('');
      setAllowedModels('*');
      setRps('20');
      setMaxConcurrent('20');
      setGeneratedKey('');
      setGeneratedHash('');
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

    const rawKey = generateRandomGatewayKey();
    const keyHash = await computeSha256Hex(rawKey);

    const modelsList =
      allowedModels.trim() === '*'
        ? ['*']
        : allowedModels
            .split(',')
            .map((m) => m.trim())
            .filter(Boolean);

    const newTenant: TenantDTO = {
      name: tenantName.trim(),
      key_hash: keyHash,
      status: 'active',
      allowed_models: modelsList,
      rate_limit: {
        rps: parsedRps,
        burst: parsedRps * 2,
        max_concurrent: parsedMaxConcurrent,
      },
    };

    // Save to Zustand and Go backend
    addOrUpdateTenant(newTenant);
    saveMutation.mutate(buildSettingsPayload());

    setGeneratedKey(rawKey);
    setGeneratedHash(keyHash);
    setStep('revealed');
  };

  const handleCopy = async () => {
    const success = await copyToClipboard(generatedKey);
    if (success) {
      setCopied(true);
      addToast({
        title: 'Copied to Clipboard',
        message: 'API key secret copied successfully.',
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
      title={step === 'configure' ? 'Issue New Tenant API Key' : 'API Key Secret Issued'}
      description={
        step === 'configure'
          ? 'Provision rate limits and access permissions for a new client tenant.'
          : 'Make sure to copy your API key now. You won’t be able to see it again!'
      }
      size="md"
    >
      {step === 'configure' ? (
        <form onSubmit={handleGenerate} className="flex flex-col gap-4 font-mono text-xs">
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Tenant / Team Name</label>
            <input
              type="text"
              required
              value={tenantName}
              onChange={(e) => setTenantName(e.target.value)}
              placeholder="e.g. backend-agent-cluster"
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
          </div>

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
              Generate Key
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-col gap-4 font-mono text-xs">
          {/* Warning Banner */}
          <div className="p-3.5 rounded-xl bg-transparent border border-amber-500/20 text-amber-300 flex items-start gap-2.5">
            <AlertTriangle className="w-4 h-4 flex-shrink-0 mt-0.5 text-neutral-400" />
            <div>
              <strong className="block font-medium">One-time View Guard</strong>
              <span className="text-neutral-400 text-[11px]">
                Firefly hashes this key using SHA-256 and never stores the plaintext secret.
              </span>
            </div>
          </div>

          {/* Generated Plaintext Key Box */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Plaintext Secret Key</label>
            <div className="flex items-center gap-2 p-2.5 rounded-xl bg-transparent border border-white/[0.08]">
              <input
                type="text"
                readOnly
                value={generatedKey}
                className="flex-1 bg-transparent text-white font-mono text-xs focus:outline-none"
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

          {/* Key Hash Details */}
          <div className="p-3 rounded-xl bg-transparent border border-white/[0.04] flex flex-col gap-1 text-[11px]">
            <span className="text-neutral-500">Stored SHA-256 Hash:</span>
            <span className="text-neutral-300 break-all">{generatedHash}</span>
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
