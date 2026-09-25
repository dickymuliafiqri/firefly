import { Plus, Trash2 } from "lucide-react";
import { Field, SwitchRow } from "@/components/ui/Controls";

export interface GeneralState {
  name: string;
  protocol: string;
  baseUrl: string;
  fallbackUrls: string[];
  egressMode: "direct" | "warp" | "proxy";
  proxyUrl: string;
  allowInsecure: boolean;
  timeoutMs: number;
  idleTimeoutMs: number;
  probeModel: string;
  extraHeaders: Array<{ key: string; value: string }>;
}

interface GeneralTabProps {
  state: GeneralState;
  isEdit: boolean;
  onChange: (patch: Partial<GeneralState>) => void;
}

export function GeneralTab({ state, isEdit, onChange }: GeneralTabProps) {
  const headers = state.extraHeaders;

  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <h2>Upstream Host</h2>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <Field label="Upstream name" htmlFor="u-name">
              <input
                id="u-name"
                type="text"
                value={state.name}
                placeholder="openai-prod"
                disabled={isEdit}
                onChange={(e) => onChange({ name: e.target.value })}
                required
              />
            </Field>
            <Field label="Protocol" htmlFor="u-proto">
              <select
                id="u-proto"
                value={state.protocol}
                onChange={(e) => onChange({ protocol: e.target.value })}
              >
                <option value="openai">openai</option>
                <option value="anthropic">anthropic</option>
                <option value="antigravity">antigravity</option>
                <option value="antigravity-go">antigravity-go</option>
                <option value="cline">cline</option>
                <option value="codebuddy">codebuddy</option>
                <option value="codebuddy-cn">codebuddy-cn</option>
                <option value="codebuddy-intl">codebuddy-intl</option>
                <option value="grok-cli">grok-cli</option>
                <option value="opencode">opencode</option>
                <option value="qoder">qoder</option>
                {/* Fallback if current protocol is non-standard */}
                {![
                  "openai",
                  "anthropic",
                  "antigravity",
                  "antigravity-go",
                  "cline",
                  "codebuddy",
                  "codebuddy-cn",
                  "codebuddy-intl",
                  "grok-cli",
                  "opencode",
                  "qoder",
                ].includes(state.protocol) && (
                  <option value={state.protocol}>{state.protocol}</option>
                )}
              </select>
            </Field>
          </div>

          <div style={{ marginTop: 14 }}>
            <Field label="Base URL" htmlFor="u-base-url">
              <input
                id="u-base-url"
                type="url"
                className="mono"
                value={state.baseUrl}
                placeholder="https://api.openai.com/v1"
                onChange={(e) => onChange({ baseUrl: e.target.value })}
                required
              />
            </Field>
          </div>

          <div style={{ marginTop: 14 }}>
            <Field label="Fallback base URLs" htmlFor="u-fallback">
              <input
                id="u-fallback"
                type="text"
                className="mono"
                value={state.fallbackUrls.join(", ")}
                placeholder="https://backup.example.com/v1 (dipisah koma)"
                onChange={(e) =>
                  onChange({
                    fallbackUrls: e.target.value
                      .split(",")
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                }
              />
            </Field>
          </div>

          <div className="form-grid" style={{ marginTop: 14 }}>
            <Field label="Egress mode" htmlFor="u-egress">
              <select
                id="u-egress"
                value={state.egressMode}
                onChange={(e) =>
                  onChange({
                    egressMode: e.target.value as GeneralState["egressMode"],
                  })
                }
              >
                <option value="direct">direct</option>
                <option value="warp">warp</option>
                <option value="proxy">proxy</option>
              </select>
            </Field>
            {state.egressMode === "proxy" && (
              <Field label="Proxy URL" htmlFor="u-proxy-url">
                <input
                  id="u-proxy-url"
                  type="text"
                  className="mono"
                  value={state.proxyUrl}
                  placeholder="socks5://127.0.0.1:1080"
                  onChange={(e) => onChange({ proxyUrl: e.target.value })}
                />
              </Field>
            )}
          </div>

          <div className="form-grid" style={{ marginTop: 14 }}>
            <Field label="Probe model" htmlFor="u-probe">
              <input
                id="u-probe"
                type="text"
                className="mono"
                value={state.probeModel}
                placeholder="gpt-4o-mini"
                onChange={(e) => onChange({ probeModel: e.target.value })}
              />
            </Field>
            <Field label="Timeout (ms)" htmlFor="u-timeout">
              <input
                id="u-timeout"
                type="number"
                value={state.timeoutMs}
                onChange={(e) =>
                  onChange({ timeoutMs: Number(e.target.value) })
                }
              />
            </Field>
          </div>

          <div className="form-grid" style={{ marginTop: 14 }}>
            <Field label="Idle timeout (ms)" htmlFor="u-idle">
              <input
                id="u-idle"
                type="number"
                value={state.idleTimeoutMs}
                onChange={(e) =>
                  onChange({ idleTimeoutMs: Number(e.target.value) })
                }
              />
            </Field>
          </div>

          <div style={{ marginTop: 14 }}>
            <SwitchRow
              title="Allow HTTP (insecure)"
              description="Centang jika menggunakan koneksi http:// lokal atau intranet."
              checked={state.allowInsecure}
              onChange={(v) => onChange({ allowInsecure: v })}
              ariaLabel="Allow insecure HTTP"
            />
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <h2>Extra Headers</h2>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() =>
              onChange({ extraHeaders: [...headers, { key: "", value: "" }] })
            }
          >
            <Plus style={{ width: 14, height: 14 }} />
            Add
          </button>
        </div>
        <div className="card-body">
          {headers.length === 0 ? (
            <span className="faint">Belum ada custom header.</span>
          ) : (
            <div className="stack" style={{ gap: 8 }}>
              {headers.map((h, i) => (
                <div key={i} className="form-row">
                  <input
                    type="text"
                    className="mono"
                    style={{ flex: 1 }}
                    placeholder="Header name"
                    value={h.key}
                    onChange={(e) => {
                      const val = e.target.value;
                      onChange({
                        extraHeaders: headers.map((item, idx) =>
                          idx === i ? { ...item, key: val } : item,
                        ),
                      });
                    }}
                  />
                  <input
                    type="text"
                    className="mono"
                    style={{ flex: 1 }}
                    placeholder="Header value"
                    value={h.value}
                    onChange={(e) => {
                      const val = e.target.value;
                      onChange({
                        extraHeaders: headers.map((item, idx) =>
                          idx === i ? { ...item, value: val } : item,
                        ),
                      });
                    }}
                  />
                  <button
                    type="button"
                    className="btn btn-ghost"
                    onClick={() =>
                      onChange({
                        extraHeaders: headers.filter((_, idx) => idx !== i),
                      })
                    }
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
