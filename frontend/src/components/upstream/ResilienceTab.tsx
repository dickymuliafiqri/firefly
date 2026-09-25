import { Plus, Trash2 } from 'lucide-react';
import { Field } from '@/components/ui/Controls';
import type { KeyErrorRuleDTO } from '@/services/schema';

interface ResilienceTabProps {
  keyErrorAction: 'cooldown' | 'deactivate' | 'delete';
  keyErrorThreshold: number;
  keyCooldownDurationMs: number;
  keyErrorRules: KeyErrorRuleDTO[];
  onActionChange: (action: 'cooldown' | 'deactivate' | 'delete') => void;
  onThresholdChange: (threshold: number) => void;
  onCooldownChange: (ms: number) => void;
  onRulesChange: (rules: KeyErrorRuleDTO[]) => void;
}

export function ResilienceTab({
  keyErrorAction,
  keyErrorThreshold,
  keyCooldownDurationMs,
  keyErrorRules,
  onActionChange,
  onThresholdChange,
  onCooldownChange,
  onRulesChange,
}: ResilienceTabProps) {
  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <h2>Layer 1: Key Error Policy</h2>
        </div>
        <div className="card-body">
          <p className="hint" style={{ marginBottom: 14 }}>
            Tindakan saat upstream mengembalikan error kredensial (401/429/quota). Circuit breaker tidak pernah
            dijatuhkan oleh error Layer 1.
          </p>
          <div className="form-grid">
            <Field label="Key error action" htmlFor="u-act">
              <select
                id="u-act"
                value={keyErrorAction}
                onChange={(e) => onActionChange(e.target.value as 'cooldown' | 'deactivate' | 'delete')}
              >
                <option value="cooldown">cooldown (jeda sementara)</option>
                <option value="deactivate">deactivate (nonaktifkan permanen di memori & DB)</option>
                <option value="delete">delete (hapus permanen dari memori & DB)</option>
              </select>
            </Field>
            <Field label="Error threshold" htmlFor="u-thresh">
              <input
                id="u-thresh"
                type="number"
                value={keyErrorThreshold}
                onChange={(e) => onThresholdChange(Number(e.target.value))}
              />
            </Field>
          </div>
          <div className="form-grid" style={{ marginTop: 14 }}>
            <Field label="Cooldown duration (ms)" htmlFor="u-cd">
              <input
                id="u-cd"
                type="number"
                value={keyCooldownDurationMs}
                onChange={(e) => onCooldownChange(Number(e.target.value))}
              />
            </Field>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <h2>Granular Error Rules</h2>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => onRulesChange([...keyErrorRules, { status_code: 429, threshold: 3, action: 'cooldown' }])}
          >
            <Plus style={{ width: 14, height: 14 }} />
            Add rule
          </button>
        </div>
        <div className="card-body">
          {keyErrorRules.length === 0 ? (
            <span className="faint">Mengikuti aturan default di atas.</span>
          ) : (
            <div className="granular-rules-container">
              <div className="granular-rules-header">
                <span>HTTP Status</span>
                <span>Threshold</span>
                <span>Action</span>
                <span />
              </div>
              {keyErrorRules.map((rule, idx) => (
                <div key={idx} className="granular-rule-row">
                  <input
                    type="number"
                    placeholder="mis. 429"
                    value={rule.status_code}
                    onChange={(e) => {
                      const val = Number(e.target.value);
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, status_code: val } : r)));
                    }}
                  />
                  <input
                    type="number"
                    placeholder="mis. 3"
                    value={rule.threshold}
                    onChange={(e) => {
                      const val = Number(e.target.value);
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, threshold: val } : r)));
                    }}
                  />
                  <select
                    value={rule.action}
                    onChange={(e) => {
                      const val = e.target.value;
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, action: val } : r)));
                    }}
                  >
                    <option value="cooldown">cooldown (jeda sementara)</option>
                    <option value="deactivate">deactivate (nonaktifkan)</option>
                    <option value="delete">delete (hapus slot)</option>
                  </select>
                  <button
                    type="button"
                    className="btn btn-ghost p-2 text-danger hover:bg-danger/10"
                    title="Hapus rule"
                    onClick={() => onRulesChange(keyErrorRules.filter((_, i) => i !== idx))}
                  >
                    <Trash2 style={{ width: 14, height: 14 }} />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
