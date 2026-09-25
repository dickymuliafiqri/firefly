import React, { useMemo } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { GitCommit, AlertTriangle, CheckCircle2, ArrowRight } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface DiffLine {
  type: 'added' | 'removed' | 'unchanged';
  text: string;
  oldLineNumber?: number;
  newLineNumber?: number;
}

export interface DiffModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: () => void;
  isSaving: boolean;
  originalJson: string;
  modifiedJson: string;
}

/**
 * Computes a line-by-line diff between two text strings using standard LCS algorithm.
 * Lightweight, zero third-party dependencies (Vercel Best Practice: bundle-defer-third-party).
 */
function computeLineDiff(oldText: string, newText: string): { diff: DiffLine[]; additions: number; deletions: number } {
  const oldLines = oldText.split('\n');
  const newLines = newText.split('\n');

  const m = oldLines.length;
  const n = newLines.length;

  // Build LCS table
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));

  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      if (oldLines[i - 1] === newLines[j - 1]) {
        dp[i][j] = dp[i - 1][j - 1] + 1;
      } else {
        dp[i][j] = Math.max(dp[i - 1][j], dp[i][j - 1]);
      }
    }
  }

  // Backtrack to reconstruct diff
  const diff: DiffLine[] = [];
  let i = m;
  let j = n;
  let additions = 0;
  let deletions = 0;

  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && oldLines[i - 1] === newLines[j - 1]) {
      diff.unshift({
        type: 'unchanged',
        text: oldLines[i - 1],
        oldLineNumber: i,
        newLineNumber: j,
      });
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      diff.unshift({
        type: 'added',
        text: newLines[j - 1],
        newLineNumber: j,
      });
      additions++;
      j--;
    } else if (i > 0 && (j === 0 || dp[i][j - 1] < dp[i - 1][j])) {
      diff.unshift({
        type: 'removed',
        text: oldLines[i - 1],
        oldLineNumber: i,
      });
      deletions++;
      i--;
    }
  }

  return { diff, additions, deletions };
}

/**
 * DiffModal
 * Displays line-by-line before/after diff viewer before hot-swapping configuration on the Go server.
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-derived-state-no-effect
 * - rendering-conditional-render
 */
export const DiffModal = React.memo(function DiffModal({
  isOpen,
  onClose,
  onConfirm,
  isSaving,
  originalJson,
  modifiedJson,
}: DiffModalProps) {
  const { diff, additions, deletions } = useMemo(() => {
    if (!isOpen) return { diff: [], additions: 0, deletions: 0 };
    return computeLineDiff(originalJson, modifiedJson);
  }, [isOpen, originalJson, modifiedJson]);

  const hasChanges = additions > 0 || deletions > 0;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={
        <div className="flex items-center gap-2">
          <GitCommit className="w-4 h-4 text-neutral-400" />
          <span>Review Configuration Diff</span>
        </div>
      }
      description="Inspect line-by-line snapshot modifications before triggering atomic hot-swap on Go server."
      size="xl"
    >
      <div className="space-y-4">
        {/* Diff summary pills */}
        <div className="flex items-center justify-between p-3 rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs">
          <div className="flex items-center gap-3">
            <span className="text-neutral-500">Delta Summary:</span>
            <span className="text-emerald-400/90 font-mono text-xs">+{additions} additions</span>
            <span className="text-rose-400/90 font-mono text-xs">-{deletions} deletions</span>
          </div>

          <div className="text-[11px] text-neutral-500 flex items-center gap-1.5">
            <CheckCircle2 className="w-3.5 h-3.5 text-neutral-400" />
            <span>Zero-Downtime Hot Swap via atomic.Pointer</span>
          </div>
        </div>

        {/* Diff scroll container */}
        <div className="max-h-[380px] overflow-y-auto rounded-xl bg-transparent border border-white/[0.06] font-mono text-[12px] p-2 select-text">
          {!hasChanges ? (
            <div className="p-8 text-center text-neutral-500">
              No configuration changes detected between local draft and server snapshot.
            </div>
          ) : (
            <table className="w-full border-collapse">
              <tbody>
                {diff.map((line, idx) => {
                  let bg = 'hover:bg-white/[0.015]';
                  let symbol = ' ';
                  let textColor = 'text-neutral-300';

                  if (line.type === 'added') {
                    bg = 'bg-emerald-500/10 text-emerald-300';
                    symbol = '+';
                    textColor = 'text-emerald-300';
                  } else if (line.type === 'removed') {
                    bg = 'bg-rose-500/10 text-rose-300';
                    symbol = '-';
                    textColor = 'text-rose-300 line-through opacity-80';
                  }

                  return (
                    <tr key={idx} className={cn('leading-relaxed', bg)}>
                      <td className="w-10 text-right pr-2 select-none text-neutral-600 text-[10px] tabular-nums">
                        {line.oldLineNumber || ''}
                      </td>
                      <td className="w-10 text-right pr-2 select-none text-neutral-600 text-[10px] tabular-nums">
                        {line.newLineNumber || ''}
                      </td>
                      <td className="w-6 text-center select-none font-bold opacity-70">
                        {symbol}
                      </td>
                      <td className={cn('whitespace-pre pl-1', textColor)}>
                        {line.text}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>

        {/* Bottom Actions */}
        <div className="flex items-center justify-between pt-3 border-t border-white/[0.04]">
          <div className="text-[11px] font-mono text-neutral-500 flex items-center gap-1.5">
            <AlertTriangle className="w-3.5 h-3.5 text-neutral-400" />
            <span>Hot-swap validates all upstreams, keys, and models before committing.</span>
          </div>

          <div className="flex items-center gap-2">
            <Button variant="minimal" size="sm" onClick={onClose} disabled={isSaving}>
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              onClick={onConfirm}
              isLoading={isSaving}
              disabled={!hasChanges || isSaving}
              rightIcon={<ArrowRight className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              Confirm Hot-Swap & Reload
            </Button>
          </div>
        </div>
      </div>
    </Modal>
  );
});
