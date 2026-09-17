import React, { useState, useEffect, useCallback } from 'react';
import { Button } from '@/components/ui/Button';
import { Sliders } from 'lucide-react';
import type { TokenSaverDTO } from '@/services/schema';

interface TokenSaverCardProps {
  tokenSaver?: TokenSaverDTO;
  onSave: (tokenSaver: TokenSaverDTO) => void;
  isSaving?: boolean;
}

export const TokenSaverCard = React.memo(function TokenSaverCard({
  tokenSaver,
  onSave,
  isSaving,
}: TokenSaverCardProps) {
  const [draft, setDraft] = useState<TokenSaverDTO>({
    enabled: false,
    compress_tool_output: true,
    terse_output: false,
    minimal_code: false,
    compress_context: false,
    max_tool_output_chars: 12000,
    context_threshold: 32000,
  });

  useEffect(() => {
    if (tokenSaver) {
      setDraft({
        enabled: tokenSaver.enabled ?? false,
        compress_tool_output: tokenSaver.compress_tool_output ?? true,
        terse_output: tokenSaver.terse_output ?? false,
        minimal_code: tokenSaver.minimal_code ?? false,
        compress_context: tokenSaver.compress_context ?? false,
        max_tool_output_chars: tokenSaver.max_tool_output_chars ?? 12000,
        context_threshold: tokenSaver.context_threshold ?? 32000,
      });
    }
  }, [tokenSaver]);

  const handleSave = useCallback(() => {
    onSave(draft);
  }, [draft, onSave]);

  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
      {/* Header matching other cards */}
      <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Sliders className="w-4 h-4 text-neutral-400" />
          <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
            Token Saver Optimization Suite
          </h3>
        </div>
        {draft.enabled ? (
          <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-emerald-500/20 bg-emerald-500/10 text-emerald-400 text-[10px]">
            <span className="w-1 h-1 rounded-full bg-emerald-400" />
            Enabled
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-white/[0.06] bg-white/[0.02] text-neutral-400 text-[10px]">
            Disabled
          </span>
        )}
      </div>

      {/* Master Enable Checkbox */}
      <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={(e) => setDraft((current) => ({ ...current, enabled: e.target.checked }))}
          className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300"
        />
        Enable gateway prompt and output token optimization
      </label>

      {/* Feature Checkboxes Grid */}
      <div className="space-y-3 pt-1">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {/* RTK */}
          <div className="space-y-1.5">
            <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                disabled={!draft.enabled}
                checked={draft.compress_tool_output}
                onChange={(e) =>
                  setDraft((current) => ({ ...current, compress_tool_output: e.target.checked }))
                }
                className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300 disabled:opacity-50"
              />
              <span>Compress Tool Output (RTK)</span>
            </label>
            <p className="text-[11px] font-mono text-neutral-500 pl-5.5 leading-relaxed">
              Strips ANSI escapes, deduplicates log loops, and compacts git diff context.
            </p>
          </div>

          {/* Caveman */}
          <div className="space-y-1.5">
            <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                disabled={!draft.enabled}
                checked={draft.terse_output}
                onChange={(e) =>
                  setDraft((current) => ({ ...current, terse_output: e.target.checked }))
                }
                className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300 disabled:opacity-50"
              />
              <span>Compress LLM Output (Caveman)</span>
            </label>
            <p className="text-[11px] font-mono text-neutral-500 pl-5.5 leading-relaxed">
              Injects terse instructions into system prompt to eliminate filler (~65% fewer output tokens).
            </p>
          </div>

          {/* Ponytail */}
          <div className="space-y-1.5">
            <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                disabled={!draft.enabled}
                checked={draft.minimal_code}
                onChange={(e) =>
                  setDraft((current) => ({ ...current, minimal_code: e.target.checked }))
                }
                className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300 disabled:opacity-50"
              />
              <span>Lazy Senior Dev (Ponytail)</span>
            </label>
            <p className="text-[11px] font-mono text-neutral-500 pl-5.5 leading-relaxed">
              Biases code generation toward YAGNI, standard library reuse, and minimal additions.
            </p>
          </div>

          {/* Headroom */}
          <div className="space-y-1.5">
            <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                disabled={!draft.enabled}
                checked={draft.compress_context}
                onChange={(e) =>
                  setDraft((current) => ({ ...current, compress_context: e.target.checked }))
                }
                className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300 disabled:opacity-50"
              />
              <span>Compress Context (Headroom)</span>
            </label>
            <p className="text-[11px] font-mono text-neutral-500 pl-5.5 leading-relaxed">
              Prunes older middle tool outputs when conversation approaches context threshold.
            </p>
          </div>
        </div>

        {/* Threshold Inputs */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
          <div className="space-y-1.5">
            <label className="text-xs font-mono text-neutral-400 block">
              Max Tool Output Characters
            </label>
            <input
              type="number"
              disabled={!draft.enabled || !draft.compress_tool_output}
              value={draft.max_tool_output_chars ?? 12000}
              onChange={(e) =>
                setDraft((current) => ({
                  ...current,
                  max_tool_output_chars: parseInt(e.target.value, 10) || 12000,
                }))
              }
              placeholder="12000"
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs disabled:opacity-50 focus:outline-none focus:border-white/20"
            />
            <span className="text-[10px] text-neutral-500 block">
              Threshold for head-and-tail smart truncation (default: 12000 chars)
            </span>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-mono text-neutral-400 block">
              Context Threshold (Tokens)
            </label>
            <input
              type="number"
              disabled={!draft.enabled || !draft.compress_context}
              value={draft.context_threshold ?? 32000}
              onChange={(e) =>
                setDraft((current) => ({
                  ...current,
                  context_threshold: parseInt(e.target.value, 10) || 32000,
                }))
              }
              placeholder="32000"
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs disabled:opacity-50 focus:outline-none focus:border-white/20"
            />
            <span className="text-[10px] text-neutral-500 block">
              Token limit before Headroom middle pruning kicks in (default: 32000 tokens)
            </span>
          </div>
        </div>
      </div>

      {/* Information / Policy Box */}
      <div className="p-3 rounded-lg bg-transparent border border-white/[0.04] space-y-1 text-[11px] text-neutral-400">
        <p className="flex items-center gap-1.5 text-neutral-300 font-medium">
          <span className="w-1 h-1 rounded-full bg-neutral-400" />
          Optimization Policies:
        </p>
        <p className="text-neutral-500 pl-2.5">
          • <span className="text-neutral-300">Zero-alloc fast-path</span>: when disabled, requests pass directly through the gateway without memory allocation or overhead.
        </p>
        <p className="text-neutral-500 pl-2.5">
          • <span className="text-neutral-300">Hot-swap reload</span>: changes are applied atomically across all active streams without service interruption.
        </p>
        <p className="text-neutral-500 pl-2.5">
          • <span className="text-neutral-300">API Access</span>: prompt and context compression is also available directly via <code className="text-neutral-400">POST /v1/compress</code>.
        </p>
      </div>

      {/* Save Button */}
      <Button
        variant="minimal"
        size="sm"
        onClick={handleSave}
        isLoading={isSaving}
        disabled={isSaving}
      >
        Save Token Saver
      </Button>
    </div>
  );
});
