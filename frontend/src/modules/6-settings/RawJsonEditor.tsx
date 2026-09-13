import React, { useState, useDeferredValue, useMemo, useCallback } from 'react';
import { Button } from '@/components/ui/Button';
import {
  Code,
  AlertCircle,
  Sparkles,
  RotateCcw,
  Copy,
  Check,
} from 'lucide-react';
import type { SettingsDTO } from '@/services/schema';
import { copyToClipboard } from '@/lib/utils';

export interface RawJsonEditorProps {
  value: string;
  onChange: (value: string) => void;
  onReset: () => void;
  serverValue: string;
}

interface ValidationResult {
  isValid: boolean;
  error?: string;
  line?: number;
  parsed?: SettingsDTO;
}

/**
 * RawJsonEditor
 * Monospace raw JSON configuration editor with immediate syntax validation,
 * line numbers, formatting, and deferring for lag-free typing.
 *
 * Vercel React Best Practices:
 * - rerender-use-deferred-value (defers JSON parsing/validation of large inputs)
 * - rerender-memo
 * - rendering-conditional-render
 */
export const RawJsonEditor = React.memo(function RawJsonEditor({
  value,
  onChange,
  onReset,
  serverValue,
}: RawJsonEditorProps) {
  const [copied, setCopied] = useState(false);

  // Defer the expensive parsing/validation while user is typing rapidly
  const deferredValue = useDeferredValue(value);

  // Validate deferred JSON
  const validation: ValidationResult = useMemo(() => {
    if (!deferredValue.trim()) {
      return { isValid: false, error: 'Configuration cannot be empty' };
    }

    try {
      const parsed = JSON.parse(deferredValue) as SettingsDTO;
      if (typeof parsed !== 'object' || parsed === null) {
        return { isValid: false, error: 'Root must be a JSON object' };
      }
      if (!Array.isArray(parsed.upstreams)) {
        return { isValid: false, error: 'Missing or invalid "upstreams" array' };
      }
      if (!Array.isArray(parsed.models)) {
        return { isValid: false, error: 'Missing or invalid "models" array' };
      }
      if (!Array.isArray(parsed.tenants)) {
        return { isValid: false, error: 'Missing or invalid "tenants" array' };
      }

      return { isValid: true, parsed };
    } catch (e: unknown) {
      let line: number | undefined;
      const message = e instanceof Error ? e.message : 'Invalid JSON format';

      // Extract line number if present in error message
      const match = message.match(/line (\d+)/i) || message.match(/position (\d+)/i);
      if (match) {
        line = parseInt(match[1], 10);
      }

      return { isValid: false, error: message, line };
    }
  }, [deferredValue]);

  // Line numbers calculation
  const lineCount = useMemo(() => {
    return value.split('\n').length;
  }, [value]);

  // Prettify / Format JSON
  const handlePrettify = useCallback(() => {
    try {
      const parsed = JSON.parse(value);
      const formatted = JSON.stringify(parsed, null, 2);
      onChange(formatted);
    } catch (e) {
      // Cannot format invalid JSON
    }
  }, [value, onChange]);

  // Copy to clipboard
  const handleCopy = useCallback(async () => {
    const success = await copyToClipboard(value);
    if (success) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [value]);

  const hasDiffWithServer = value !== serverValue;

  return (
    <div className="space-y-3">
      {/* Editor Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-3 p-3 rounded-xl bg-transparent border border-white/[0.06]">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-1.5 font-mono text-xs">
            <Code className="w-4 h-4 text-neutral-400" />
            <span className="text-neutral-200 font-medium">settings.json</span>
          </div>

          {validation.isValid ? (
            <div className="flex items-center gap-1.5 text-[11px] text-emerald-400/90 font-mono">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
              <span>Valid Schema</span>
            </div>
          ) : (
            <div className="flex items-center gap-1.5 text-[11px] text-rose-400/90 font-mono">
              <span className="w-1.5 h-1.5 rounded-full bg-rose-400" />
              <span>Syntax Error</span>
            </div>
          )}

          <span className="text-[11px] text-neutral-500 font-mono">
            · {hasDiffWithServer ? 'Unsaved Changes' : 'Synced'}
          </span>
        </div>

        {/* Action buttons */}
        <div className="flex items-center gap-2">
          <Button
            variant="minimal"
            size="sm"
            onClick={handlePrettify}
            disabled={!validation.isValid}
            leftIcon={<Sparkles className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
          >
            Format JSON
          </Button>

          <Button
            variant="minimal"
            size="sm"
            onClick={handleCopy}
            leftIcon={copied ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
          >
            {copied ? 'Copied' : 'Copy'}
          </Button>

          <Button
            variant="minimal"
            size="sm"
            onClick={onReset}
            disabled={!hasDiffWithServer}
            leftIcon={<RotateCcw className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
          >
            Revert
          </Button>
        </div>
      </div>

      {/* Error alert banner if invalid */}
      {!validation.isValid && validation.error ? (
        <div className="flex items-start gap-2.5 p-3 rounded-xl bg-transparent border border-rose-500/20 font-mono text-xs text-rose-300 animate-in fade-in duration-150">
          <AlertCircle className="w-4 h-4 text-rose-400 shrink-0 mt-0.5" />
          <div>
            <span className="font-semibold">JSON Validation Error: </span>
            <span>{validation.error}</span>
          </div>
        </div>
      ) : null}

      {/* Monospace Code Editor Area */}
      <div className="relative flex rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs overflow-hidden focus-within:border-white/[0.14] transition-colors">
        {/* Line Numbers Gutter */}
        <div className="py-4 pl-3 pr-3 text-right bg-white/[0.01] select-none text-neutral-600 text-[11px] border-r border-white/[0.04] tabular-nums">
          {Array.from({ length: lineCount }).map((_, i) => (
            <div key={i + 1} className="leading-6">
              {i + 1}
            </div>
          ))}
        </div>

        {/* Textarea Input */}
        <textarea
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
          className="flex-1 p-4 bg-transparent text-neutral-300 resize-y min-h-[460px] font-mono text-xs leading-6 outline-none focus:outline-none overflow-x-auto whitespace-pre selection:bg-white/[0.1]"
          placeholder="Paste or edit Firefly JSON configuration..."
        />
      </div>
    </div>
  );
});
