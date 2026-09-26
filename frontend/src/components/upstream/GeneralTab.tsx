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

/**
 * Provider-managed endpoints of the protocols that authenticate with an OAuth
 * token instead of an operator-supplied API key. The adapter forwards that token
 * to this host, so the value is not editable: the backend pins it while building
 * the catalog (see domain.OAuthManagedBaseURL), and the form renders it
 * read-only. `antigravity-go` is the legacy alias of `antigravity`.
 */
export const OAUTH_LOCKED_BASE_URLS: Record<string, string> = {
  antigravity: "https://daily-cloudcode-pa.googleapis.com",
  "antigravity-go": "https://daily-cloudcode-pa.googleapis.com",
  cline: "https://api.cline.bot/api/v1",
  codebuddy: "https://copilot.tencent.com/v2",
  "codebuddy-cn": "https://copilot.tencent.com/v2",
  codebuddy_cn: "https://copilot.tencent.com/v2",
  "codebuddy-intl": "https://www.codebuddy.ai/v2",
  codebuddy_intl: "https://www.codebuddy.ai/v2",
};

/**
 * Returns the provider-managed endpoint of an OAuth-authenticated protocol, or
 * undefined for protocols whose host the operator chooses (openai, anthropic,
 * grok-cli, opencode, qoder).
 */
export function lockedOAuthBaseUrl(protocol: string): string | undefined {
  return OAUTH_LOCKED_BASE_URLS[protocol.trim().toLowerCase()];
}

/**
 * Resolves the Base URL a save, health-check, or model-discovery call must send:
 * the provider-managed endpoint when the protocol has one (never the form
 * value), otherwise what the operator typed. Callers must prefer this over
 * reading the form value so a locked protocol can never be retargeted.
 */
export function resolveBaseUrl(protocol: string, baseUrl: string): string {
  return lockedOAuthBaseUrl(protocol) ?? baseUrl.trim();
}

interface GeneralTabProps {
  state: GeneralState;
  isEdit: boolean;
  onChange: (patch: Partial<GeneralState>) => void;
}

export function GeneralTab({ state, isEdit, onChange }: GeneralTabProps) {
  const headers = state.extraHeaders;
  // OAuth-managed protocols carry a provider endpoint nobody may edit, and an
  // existing upstream of one is frozen at that protocol: its credentials are
  // only meaningful for the provider that issued them.
  const lockedBaseUrl = lockedOAuthBaseUrl(state.protocol);
  const protocolLocked = isEdit && lockedBaseUrl !== undefined;

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
            <Field
              label="Protocol"
              htmlFor="u-proto"
              hint={
                protocolLocked
                  ? "Locked: this upstream authenticates with a provider OAuth token."
                  : undefined
              }
            >
              <select
                id="u-proto"
                value={state.protocol}
                disabled={protocolLocked}
                onChange={(e) => {
                  const next = e.target.value;
                  const patch: Partial<GeneralState> = { protocol: next };
                  const nextLocked = lockedOAuthBaseUrl(next);
                  // OAuth protocols read their endpoint from the provider:
                  // prefill the managed host on entry and drop every fallback
                  // host, then clear it again when switching away so a provider
                  // endpoint never leaks into a host the operator now chooses.
                  if (nextLocked) {
                    patch.baseUrl = nextLocked;
                    patch.fallbackUrls = [];
                  } else if (lockedBaseUrl && state.baseUrl.trim() === lockedBaseUrl) {
                    patch.baseUrl = "";
                  }
                  onChange(patch);
                }}
              >
                <option value="openai">openai</option>
                <option value="anthropic">anthropic</option>
                <option value="antigravity">antigravity</option>
                <option value="cline">cline</option>
                <option value="codebuddy-cn">codebuddy-cn</option>
                <option value="codebuddy-intl">codebuddy-intl</option>
                <option value="grok-cli">grok-cli</option>
                <option value="opencode">opencode</option>
                <option value="qoder">qoder</option>
                {/* Fallback if current protocol is non-standard or legacy alias */}
                {![
                  "openai",
                  "anthropic",
                  "antigravity",
                  "cline",
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
            <Field
              label={lockedBaseUrl ? "Base URL (managed)" : "Base URL"}
              htmlFor="u-base-url"
              hint={
                lockedBaseUrl
                  ? `Fixed by the ${state.protocol} OAuth provider: the adapter sends its token to ${lockedBaseUrl} only.`
                  : undefined
              }
            >
              <input
                id="u-base-url"
                type="url"
                className="mono"
                value={lockedBaseUrl ?? state.baseUrl}
                placeholder={lockedBaseUrl ?? "https://api.openai.com/v1"}
                disabled={lockedBaseUrl !== undefined}
                onChange={(e) => onChange({ baseUrl: e.target.value })}
                required={lockedBaseUrl === undefined}
              />
            </Field>
          </div>

          <div style={{ marginTop: 14 }}>
            <Field
              label="Fallback base URLs"
              htmlFor="u-fallback"
              hint={
                lockedBaseUrl
                  ? "Not available: an OAuth-managed endpoint has no operator-defined fallback host."
                  : undefined
              }
            >
              <input
                id="u-fallback"
                type="text"
                className="mono"
                value={lockedBaseUrl ? "" : state.fallbackUrls.join(", ")}
                placeholder="https://backup.example.com/v1 (comma-separated)"
                disabled={lockedBaseUrl !== undefined}
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
              description="Enable if using local or intranet http:// connections."
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
            <span className="faint">No custom headers yet.</span>
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
