/**
 * ConnectPage — Services → Connect.
 *
 * Hands an operator everything a third-party AI agent needs to talk to this
 * gateway: the base URL, a tenant-key dropdown and the model id (each with its
 * own copy button), the inbound endpoint table, and copy-ready presets for
 * Cline, Roo Code, Kilo Code, Cursor, Continue, OpenCode, Aider, the OpenAI
 * SDKs and raw HTTP. All preset text comes from `@/lib/connect` so the snippet
 * builder stays headless-testable (`npm run check:connect`).
 */
import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Controls";
import { PageHeader } from "@/components/ui/PageHeader";
import { QueryGate } from "@/components/ui/QueryGate";
import { useSettingsQuery } from "@/services/api";
import type { ClientContext, ClientPreset } from "@/lib/connect";
import {
  CLIENT_PRESETS,
  GATEWAY_ENDPOINTS,
  PLACEHOLDER_KEY,
  PLACEHOLDER_MODEL,
  clientCode,
  clientFields,
  connectionBundle,
  defaultOrigin,
  endpointUrl,
  gatewayOrigin,
  isUsableKey,
  maskKey,
  openAiBaseUrl,
  readBaseUrl,
  readTenantKey,
  writeBaseUrl,
  writeTenantKey,
} from "@/lib/connect";
import { useUiStore } from "@/state/store";

interface ProbeState {
  state: "idle" | "loading" | "ok" | "fail";
  message: string;
}

/** Copy-to-clipboard button that reports through the shared toast host. */
function CopyButton({
  label,
  value,
  disabled,
}: {
  label: string;
  value: string;
  disabled?: boolean;
}) {
  const pushToast = useUiStore((s) => s.pushToast);
  return (
    <button
      type="button"
      className="btn btn-ghost connect-copy"
      aria-label={`Copy ${label}`}
      disabled={disabled}
      onClick={() => {
        void navigator.clipboard.writeText(value).then(
          () =>
            pushToast({
              type: "success",
              title: `${label} copied`,
              message: "Paste it into your AI agent's configuration.",
            }),
          () =>
            pushToast({
              type: "error",
              title: "Failed to copy",
              message: "Clipboard is unavailable in this browser.",
            }),
        );
      }}
    >
      Copy
    </button>
  );
}

/** One client: copyable fields and/or a copyable config block. */
function ClientCard({ preset, ctx }: { preset: ClientPreset; ctx: ClientContext }) {
  const fields = clientFields(preset, ctx);
  const code = clientCode(preset, ctx);
  const bundle = [
    ...fields.map((field) => `${field.label}: ${field.value}`),
    code,
  ]
    .filter(Boolean)
    .join("\n\n");

  return (
    <div className="card connect-client" id={`connect-${preset.id}`}>
      <div className="card-header">
        <div className="connect-client-title">
          <h2>{preset.name}</h2>
          <span className="connect-client-kind">{preset.kind}</span>
        </div>
        <div className="connect-client-actions">
          <a
            className="btn btn-ghost"
            href={preset.docs}
            target="_blank"
            rel="noreferrer noopener"
          >
            Docs
          </a>
          <CopyButton label={`${preset.name} config`} value={bundle} />
        </div>
      </div>
      <div className="card-body">
        <p className="connect-client-summary">{preset.summary}</p>

        {fields.length > 0 ? (
          <div className="connect-copy-list">
            {fields.map((field) => (
              <div className="connect-copy-row" key={field.label}>
                <span className="connect-copy-label">{field.label}</span>
                <code className="connect-copy-value" title={field.value}>
                  {field.value}
                </code>
                <CopyButton label={field.label} value={field.value} />
              </div>
            ))}
          </div>
        ) : null}

        {code ? (
          <div className="connect-snippet">
            <div className="connect-snippet-head">
              <span className="lang">{preset.code?.label ?? "config"}</span>
              <CopyButton label={`${preset.name} snippet`} value={code} />
            </div>
            <pre>
              <code>{code}</code>
            </pre>
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function ConnectPage() {
  const settings = useSettingsQuery();
  const pushToast = useUiStore((s) => s.pushToast);
  const [originDraft, setOriginDraft] = useState(() => readBaseUrl() || defaultOrigin());
  const [tenantName, setTenantName] = useState("");
  const [pastedKey, setPastedKey] = useState("");
  const [revealKey, setRevealKey] = useState(false);
  const [modelDraft, setModelDraft] = useState("");
  const [filter, setFilter] = useState("");
  const [probe, setProbe] = useState<ProbeState>({ state: "idle", message: "" });

  const tenants = settings.data?.tenants ?? [];
  const tenant = tenants.find((t) => t.name === tenantName) ?? tenants[0];
  const selectedTenant = tenant?.name ?? "";

  // A key pasted for tenant A must never leak into tenant B's snippets.
  useEffect(() => {
    setPastedKey(selectedTenant ? readTenantKey(selectedTenant) : "");
    setRevealKey(false);
    setProbe({ state: "idle", message: "" });
  }, [selectedTenant]);

  const catalogKey = isUsableKey(tenant?.api_key) ? (tenant?.api_key ?? "").trim() : "";
  const sessionKey = isUsableKey(pastedKey) ? pastedKey.trim() : "";
  const key = sessionKey || catalogKey;
  const snippetKey = key || PLACEHOLDER_KEY;
  const keySource = sessionKey ? "pasted" : catalogKey ? "catalog" : "missing";

  const models = settings.data?.models ?? [];
  const combos = settings.data?.combos ?? [];
  const allowed = tenant?.allowed_models?.length ? new Set(tenant.allowed_models) : null;
  const modelOptions = [
    ...models.map((m) => m.public_name).filter((name) => !allowed || allowed.has(name)),
    ...combos.map((c) => c.name).filter((name) => !allowed || allowed.has(name)),
  ];
  const model = modelOptions.includes(modelDraft)
    ? modelDraft
    : (modelOptions[0] ?? PLACEHOLDER_MODEL);

  const origin = gatewayOrigin(originDraft);
  const baseUrl = openAiBaseUrl(originDraft);
  const ctx: ClientContext = { origin, baseUrl, key: snippetKey, model };
  const sameOrigin = origin === defaultOrigin();

  const query = filter.trim().toLowerCase();
  const presets = query
    ? CLIENT_PRESETS.filter((p) =>
        `${p.name} ${p.kind} ${p.summary}`.toLowerCase().includes(query),
      )
    : CLIENT_PRESETS;

  const keyBadge =
    keySource === "missing"
      ? { tone: "warn" as const, text: "Key not in catalog" }
      : keySource === "pasted"
        ? { tone: "info" as const, text: "Pasted key" }
        : { tone: "ok" as const, text: "From catalog" };

  function commitOrigin(value: string) {
    setOriginDraft(value);
    writeBaseUrl(value);
    setProbe({ state: "idle", message: "" });
  }

  async function testConnection() {
    setProbe({ state: "loading", message: "Calling GET /v1/models…" });
    try {
      const res = await fetch(endpointUrl(originDraft, "/v1/models"), {
        headers: { Authorization: `Bearer ${key}`, Accept: "application/json" },
        cache: "no-store",
      });
      const body = (await res.json().catch(() => null)) as {
        data?: unknown[];
        error?: { message?: string };
      } | null;
      if (!res.ok) {
        throw new Error(body?.error?.message ?? `HTTP ${res.status} ${res.statusText}`);
      }
      const count = body && Array.isArray(body.data) ? body.data.length : 0;
      setProbe({
        state: "ok",
        message: `Gateway reachable — ${count} model(s) visible to ${selectedTenant || "this key"}.`,
      });
    } catch (err) {
      setProbe({ state: "fail", message: err instanceof Error ? err.message : "Request failed" });
    }
  }

  function copyBundle() {
    void navigator.clipboard.writeText(connectionBundle(ctx)).then(
      () =>
        pushToast({
          type: "success",
          title: "Connection bundle copied",
          message: `${presets.length} client preset(s) with base URL and key.`,
        }),
      () =>
        pushToast({
          type: "error",
          title: "Failed to copy",
          message: "Clipboard is unavailable in this browser.",
        }),
    );
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Connect"
        description="Base URL, tenant key and copy-ready presets for wiring AI agents to this gateway."
        actions={
          <Button variant="secondary" onClick={copyBundle}>
            Copy everything
          </Button>
        }
      />

      <QueryGate isLoading={settings.isLoading} error={settings.error}>
        <div className="stack">
          {/* ---- 1 · Gateway base URL ---- */}
          <div className="card">
            <div className="card-header">
              <h2>1 · Gateway base URL</h2>
              <Badge tone={sameOrigin ? "ok" : "info"}>
                {sameOrigin ? "this dashboard's origin" : "custom origin"}
              </Badge>
            </div>
            <div className="card-body">
              <Field
                label="Base URL"
                htmlFor="connect-origin"
                hint="Defaults to this dashboard's origin — Firefly serves the dashboard and the /v1 data plane on one listener. Override it when agents reach Firefly through a tunnel, a reverse proxy or another host."
              >
                <input
                  id="connect-origin"
                  type="text"
                  value={originDraft}
                  spellCheck={false}
                  autoComplete="off"
                  placeholder={defaultOrigin()}
                  onChange={(e) => commitOrigin(e.target.value)}
                />
              </Field>

              <div className="connect-copy-list">
                <div className="connect-copy-row">
                  <span className="connect-copy-label">Gateway origin</span>
                  <code className="connect-copy-value" title={origin}>
                    {origin}
                  </code>
                  <CopyButton label="Gateway origin" value={origin} />
                </div>
                <div className="connect-copy-row">
                  <span className="connect-copy-label">OpenAI base URL</span>
                  <code className="connect-copy-value" title={baseUrl}>
                    {baseUrl}
                  </code>
                  <CopyButton label="OpenAI base URL" value={baseUrl} />
                </div>
              </div>

              <p className="connect-note">
                Every preset below takes the <strong>OpenAI base URL</strong> (origin +{" "}
                <code>/v1</code>) — the same value clients send to api.openai.com.
              </p>

              <div className="section-title">Endpoints</div>
              <div className="table-wrap">
                <table className="connect-endpoints">
                  <thead>
                    <tr>
                      <th>Method</th>
                      <th>Path</th>
                      <th>Purpose</th>
                      <th aria-label="Copy" />
                    </tr>
                  </thead>
                  <tbody>
                    {GATEWAY_ENDPOINTS.map((endpoint) => {
                      const url = endpointUrl(originDraft, endpoint.path);
                      return (
                        <tr key={`${endpoint.method} ${endpoint.path}`}>
                          <td>
                            <Badge tone="neutral">{endpoint.method}</Badge>
                          </td>
                          <td>
                            <code className="connect-copy-value" title={url}>
                              {endpoint.path}
                            </code>
                          </td>
                          <td className="connect-endpoint-note">{endpoint.note}</td>
                          <td className="num">
                            <CopyButton label={endpoint.path} value={url} />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          </div>

          {/* ---- 2 · Tenant key & model ---- */}
          <div className="card">
            <div className="card-header">
              <h2>2 · Tenant key &amp; model</h2>
              <Badge tone={keyBadge.tone}>{keyBadge.text}</Badge>
            </div>
            <div className="card-body">
              <div className="form-grid">
                <Field
                  label="Tenant key"
                  htmlFor="connect-tenant"
                  hint="Clients send it as Authorization: Bearer <key>."
                >
                  <select
                    id="connect-tenant"
                    value={selectedTenant}
                    onChange={(e) => setTenantName(e.target.value)}
                  >
                    {tenants.length === 0 ? (
                      <option value="">No tenants registered yet</option>
                    ) : null}
                    {tenants.map((t) => (
                      <option key={t.name} value={t.name}>
                        {t.name} — {String(t.status ?? "active")}
                      </option>
                    ))}
                  </select>
                </Field>

                <Field
                  label="Model"
                  htmlFor="connect-model"
                  hint={
                    allowed
                      ? "Filtered to this tenant's allowed_models."
                      : "Public model or combo name from the Models page."
                  }
                >
                  <select
                    id="connect-model"
                    value={model}
                    onChange={(e) => setModelDraft(e.target.value)}
                  >
                    {modelOptions.length === 0 ? (
                      <option value={PLACEHOLDER_MODEL}>{PLACEHOLDER_MODEL}</option>
                    ) : null}
                    {modelOptions.map((name) => (
                      <option key={name} value={name}>
                        {name}
                      </option>
                    ))}
                  </select>
                </Field>
              </div>

              <div className="connect-copy-list">
                <div className="connect-copy-row">
                  <span className="connect-copy-label">API key</span>
                  <code className="connect-copy-value" title={keySource === "missing" ? "" : key}>
                    {keySource === "missing" ? "—" : revealKey ? key : maskKey(key)}
                  </code>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    disabled={keySource === "missing"}
                    onClick={() => setRevealKey((v) => !v)}
                  >
                    {revealKey ? "Hide" : "Show"}
                  </button>
                  <CopyButton label="API key" value={key} disabled={keySource === "missing"} />
                </div>
                <div className="connect-copy-row">
                  <span className="connect-copy-label">Model ID</span>
                  <code className="connect-copy-value" title={model}>
                    {model}
                  </code>
                  <CopyButton label="Model ID" value={model} />
                </div>
              </div>

              <Field
                label="Paste tenant key (optional)"
                htmlFor="connect-paste"
                hint="The catalog keeps a masked key once it has been shown at creation, so paste the secret here to fill every preset below. It is remembered in this browser tab only — never written to disk."
              >
                <input
                  id="connect-paste"
                  type={revealKey ? "text" : "password"}
                  value={pastedKey}
                  spellCheck={false}
                  autoComplete="off"
                  placeholder="sk-gw-…"
                  onChange={(e) => {
                    setPastedKey(e.target.value);
                    if (selectedTenant) writeTenantKey(selectedTenant, e.target.value);
                  }}
                />
              </Field>

              {tenants.length === 0 ? (
                <p className="connect-note">
                  No tenant keys yet — create one on the Tenants page, then come back here.
                </p>
              ) : null}

              <div className="form-row form-row-end connect-probe">
                <span className="connect-probe-result" aria-live="polite">
                  {probe.state === "idle" ? "" : probe.message}
                </span>
                <div className="form-row">
                  {!sameOrigin ? (
                    <span className="connect-note">
                      Probing another origin is blocked by the browser (the data plane sends no CORS
                      headers) — use this dashboard's host to test.
                    </span>
                  ) : null}
                  <Button
                    variant="secondary"
                    disabled={!key || !sameOrigin || probe.state === "loading"}
                    onClick={() => void testConnection()}
                  >
                    {probe.state === "loading" ? "Testing…" : "Test connection"}
                  </Button>
                </div>
              </div>
            </div>
          </div>

          {/* ---- 3 · Client presets ---- */}
          <div>
            <div className="section-title">3 · Connect your AI agent</div>
            <div className="filter-bar">
              <input
                type="search"
                aria-label="Filter clients"
                placeholder="Filter clients (Cline, OpenCode, SDK…)"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
              <span className="spacer" />
              <span className="faint">
                {presets.length} of {CLIENT_PRESETS.length} clients
              </span>
            </div>

            <div className="connect-clients">
              {presets.map((preset) => (
                <ClientCard key={preset.id} preset={preset} ctx={ctx} />
              ))}
              {presets.length === 0 ? (
                <p className="connect-note">No client matches “{filter.trim()}”.</p>
              ) : null}
            </div>

            <p className="connect-note">
              Firefly exposes the OpenAI wire surface only: Anthropic-native clients
              (<code>/v1/messages</code>) and Responses-native clients (<code>/v1/responses</code>) have no
              inbound route. Every preset above speaks OpenAI-compatible HTTP, which is what these agents
              expect when you select their “OpenAI Compatible” provider.
            </p>
          </div>
        </div>
      </QueryGate>
    </div>
  );
}
