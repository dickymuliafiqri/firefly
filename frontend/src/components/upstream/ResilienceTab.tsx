import { Plus, Trash2 } from 'lucide-react';
import { Field, SwitchRow } from '@/components/ui/Controls';
import type { KeyErrorRuleDTO } from '@/services/schema';

interface ResilienceTabProps {
  alwaysAvailable: boolean;
  keyErrorAction: 'cooldown' | 'deactivate' | 'delete';
  keyErrorThreshold: number;
  keyCooldownDurationMs: number;
  keyErrorRules: KeyErrorRuleDTO[];
  onAlwaysAvailableChange: (v: boolean) => void;
  onActionChange: (action: 'cooldown' | 'deactivate' | 'delete') => void;
  onThresholdChange: (threshold: number) => void;
  onCooldownChange: (ms: number) => void;
  onRulesChange: (rules: KeyErrorRuleDTO[]) => void;
}

export function ResilienceTab({
  alwaysAvailable,
  keyErrorAction,
  keyErrorThreshold,
  keyCooldownDurationMs,
  keyErrorRules,
  onAlwaysAvailableChange,
  onActionChange,
  onThresholdChange,
  onCooldownChange,
  onRulesChange,
}: ResilienceTabProps) {
  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <h2>Availability</h2>
        </div>
        <div className="card-body">
          <SwitchRow
            title="Always available"
            ariaLabel="Always available"
            checked={alwaysAvailable}
            onChange={onAlwaysAvailableChange}
            description="Keep this upstream open despite key errors: a 429 never cools the key, a 401 or 403 never revokes it, no consecutive-error action runs, and the breaker never sees it. Use it when the provider meters your network instead of the key, like the keyless OpenCode Free tier whose 'public' key follows your egress IP. Failures are still logged, traffic still rotates to another key, and RPS and concurrency limits still apply."
          />
          {alwaysAvailable ? (
            <p className="hint" style={{ marginTop: 12 }}>
              The Layer 1 policy below is inactive while this is on.
            </p>
          ) : null}
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <h2>Layer 1: Key Error Policy</h2>
        </div>
        <div className="card-body">
          <p className="hint" style={{ marginBottom: 14 }}>
            Action when upstream returns a credential error (401/429/quota). The circuit breaker is never
            tripped by Layer 1 errors.
          </p>
          <div className="form-grid">
            <Field label="Key error action" htmlFor="u-act">
              <select
                id="u-act"
                value={keyErrorAction}
                onChange={(e) => onActionChange(e.target.value as 'cooldown' | 'deactivate' | 'delete')}
              >
                <option value="cooldown">cooldown (temporary pause)</option>
                <option value="deactivate">deactivate (permanent deactivation in memory & DB)</option>
                <option value="delete">delete (permanent removal from memory & DB)</option>
              </select>
            </Field>
            <Field label="Error threshold" htmlFor="u-thresh" hint="Consecutive error count before action (0 = disabled).">
              <input
                id="u-thresh"
                type="number"
                min="0"
                value={keyErrorThreshold}
                onChange={(e) => {
                  const val = Number(e.target.value);
                  onThresholdChange(Number.isNaN(val) || val < 0 ? 0 : val);
                }}
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
            <span className="faint">Follows default rules above.</span>
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
                    placeholder="e.g. 429"
                    value={rule.status_code}
                    onChange={(e) => {
                      const val = Number(e.target.value);
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, status_code: val } : r)));
                    }}
                  />
                  <input
                    type="number"
                    min="1"
                    placeholder="e.g. 3"
                    value={rule.threshold}
                    onChange={(e) => {
                      const val = Number(e.target.value);
                      const safeVal = Number.isNaN(val) || val < 1 ? 1 : val;
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, threshold: safeVal } : r)));
                    }}
                  />
                  <select
                    value={rule.action}
                    onChange={(e) => {
                      const val = e.target.value;
                      onRulesChange(keyErrorRules.map((r, i) => (i === idx ? { ...r, action: val } : r)));
                    }}
                  >
                    <option value="cooldown">cooldown (temporary pause)</option>
                    <option value="deactivate">deactivate (disable)</option>
                    <option value="delete">delete (remove slot)</option>
                  </select>
                  <button
                    type="button"
                    className="btn btn-ghost p-2 text-danger hover:bg-danger/10"
                    title="Delete rule"
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
