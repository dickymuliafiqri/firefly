import { useState, useEffect } from "react";
import {
  ArrowLeft,
  Server,
  KeyRound,
  Layers,
  Shield,
  Trash2,
} from "lucide-react";
import { PageHeader } from "@/components/ui/PageHeader";
import type { UpstreamDTO, CredentialKeyDTO } from "@/services/schema";
import { sanitizeModelName } from "@/services/schema";
import { useHashRest, navigate } from "@/lib/router";
import { useUiStore } from "@/state/store";
import { useDraftStore, useSettingsView } from "@/state/draftStore";

import {
  GeneralTab,
  type GeneralState,
  lockedOAuthBaseUrl,
  resolveBaseUrl,
} from "@/components/upstream/GeneralTab";
import { canonicalProtocol } from "@/services/schema";
import { KeysTab, type KeyEntry } from "@/components/upstream/KeysTab";
import { ModelsTab } from "@/components/upstream/ModelsTab";
import { ResilienceTab } from "@/components/upstream/ResilienceTab";

type Tab = "general" | "keys" | "models" | "resilience";

export function UpstreamEditorPage() {
  const rest = useHashRest(); // 'new' or 'edit/<name>'
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const pushToast = useUiStore((s) => s.pushToast);

  const isEdit = rest.startsWith("edit/");
  const editingName = isEdit
    ? decodeURIComponent(rest.replace("edit/", ""))
    : "";

  const [activeTab, setActiveTab] = useState<Tab>("general");

  // Tab 1 state
  const [general, setGeneral] = useState<GeneralState>({
    name: "",
    protocol: "openai",
    baseUrl: "",
    fallbackUrls: [],
    egressMode: "direct",
    proxyUrl: "",
    allowInsecure: false,
    timeoutMs: 30000,
    idleTimeoutMs: 60000,
    extraHeaders: [],
  });

  // Tab 2 state
  const [providerId, setProviderId] = useState<number | null>(null);
  const [keyStrategy, setKeyStrategy] = useState<
    "round_robin" | "least_inflight"
  >("round_robin");
  const [keys, setKeys] = useState<KeyEntry[]>([]);

  // Tab 3 state (Models & Probe)
  const [probeModel, setProbeModel] = useState("");
  const [discoveredModels, setDiscoveredModels] = useState<string[]>([]);
  const [modelsLatency, setModelsLatency] = useState<number | null>(null);

  // Tab 4 state
  const [keyErrorAction, setKeyErrorAction] = useState<
    "cooldown" | "deactivate" | "delete"
  >("cooldown");
  const [keyErrorThreshold, setKeyErrorThreshold] = useState(0);
  const [keyCooldownDurationMs, setKeyCooldownDurationMs] = useState(30000);
  const [keyErrorRules, setKeyErrorRules] = useState<
    UpstreamDTO["key_error_rules"]
  >([]);

  // Populate data when editing. Depends on the resolved upstream entry (not the
  // whole settings view) so staging other collections — e.g. model routes from
  // the Models tab — does not reset form fields the operator is still typing.
  const editingTarget =
    isEdit && settings.data
      ? settings.data.upstreams.find((u) => u.name === editingName)
      : undefined;
  useEffect(() => {
    if (!editingTarget || !isEdit || !editingName) return;
    const target = editingTarget;

    // A provider-managed endpoint is never taken from storage: an older release,
    // or a hand-edited file, could hold a host the adapter would send an OAuth
    // token to, so the pinned endpoint — and an empty fallback list — replaces it.
    const protocol = canonicalProtocol(target.protocol);
    const lockedBaseUrl = lockedOAuthBaseUrl(protocol);

    setGeneral({
      name: target.name,
      protocol,
      baseUrl: lockedBaseUrl ?? target.base_url ?? "",
      fallbackUrls: lockedBaseUrl ? [] : (target.base_urls ?? []),
      egressMode:
        (target.egress_mode as GeneralState["egressMode"]) ?? "direct",
      proxyUrl: target.proxy_url ?? "",
      allowInsecure: target.allow_insecure ?? false,
      timeoutMs: target.timeout_ms ?? 30000,
      idleTimeoutMs: target.idle_timeout_ms ?? 60000,
      extraHeaders: target.extra_headers
        ? Object.entries(target.extra_headers).map(([k, v]) => ({
            key: k,
            value: v,
          }))
        : [],
    });

    setProbeModel(target.probe_model ?? "");
    setProviderId(target.provider_id ?? null);
    setKeyStrategy(
      (target.key_strategy as "round_robin" | "least_inflight") ??
        "round_robin",
    );
    setKeyErrorAction(
      (target.key_error_action as "cooldown" | "deactivate" | "delete") ??
        "cooldown",
    );
    setKeyErrorThreshold(target.key_error_threshold ?? 0);
    setKeyCooldownDurationMs(target.key_cooldown_duration_ms ?? 30000);
    setKeyErrorRules(target.key_error_rules ?? []);

    const pool = target.credential_pool ?? [];
    if (pool.length > 0) {
      setKeys(
        pool.map((p, idx) => ({
          id: p.ref || `k-${idx}`,
          ref: p.ref,
          secret: p.secret || p.api_key || "",
          rps: p.rps || 0,
          maxConcurrent: p.max_concurrent || 0,
        })),
      );
    } else if (target.api_keys && target.api_keys.length > 0) {
      setKeys(
        target.api_keys.map((k, idx) => ({
          id: `k-${idx}`,
          ref: `${target.name}-key-${idx + 1}`,
          secret: k,
          rps: 0,
          maxConcurrent: 0,
        })),
      );
    } else if (target.api_key) {
      setKeys([
        {
          id: "k-0",
          ref: `${target.name}-key-1`,
          secret: target.api_key,
          rps: 0,
          maxConcurrent: 0,
        },
      ]);
    }
  }, [editingTarget, isEdit, editingName]);

  // Model actions
  const catalogModelNames = (settings.data?.models ?? []).map(
    (m) => m.public_name,
  );

  function handleCreateRoute(modelName: string) {
    if (!settings.data) return;
    // Discovered upstream ids routinely contain characters the gateway forbids
    // in public names (`vendor/model`, `name:tag`), so register under the
    // sanitized name while keeping the raw id as the provider-side mapping.
    const publicName = sanitizeModelName(modelName);
    if (!publicName) {
      pushToast({
        type: "error",
        title: "Invalid model name",
        message: `Cannot derive a valid public name from "${modelName}". Add a route from the Models page instead.`,
      });
      return;
    }
    if (catalogModelNames.includes(publicName)) {
      pushToast({
        type: "info",
        title: "Already registered",
        message: `Model ${publicName} already exists in the catalog.`,
      });
      return;
    }
    const nextModels = [
      ...settings.data.models,
      {
        public_name: publicName,
        upstream: general.name,
        upstream_model: modelName,
        enabled: true,
      },
    ];
    stage({ models: nextModels });
    pushToast({
      type: "info",
      title: "Route staged",
      message: `Model ${publicName} added to pending changes — commit to register it.`,
    });
  }

  function handleCreateAllRoutes(modelsToAdd: string[]) {
    if (!settings.data) return;
    const currentSet = new Set(catalogModelNames);
    const seen = new Set<string>();
    const newModels = [];
    for (const raw of modelsToAdd) {
      const publicName = sanitizeModelName(raw);
      // Skip empty derivations (nothing registerable) and anything that would
      // collide with the catalog or with an earlier entry of this same batch —
      // either would make the backend reject the whole save.
      if (!publicName || currentSet.has(publicName) || seen.has(publicName))
        continue;
      seen.add(publicName);
      newModels.push({
        public_name: publicName,
        upstream: general.name,
        upstream_model: raw,
        enabled: true,
      });
    }

    if (newModels.length === 0) return;
    stage({ models: [...settings.data.models, ...newModels] });
    pushToast({
      type: "info",
      title: "Routes staged",
      message: `${newModels.length} model routes added to pending changes.`,
    });
  }

  // Delete Upstream
  function handleDeleteUpstream() {
    if (!isEdit || !editingName || !settings.data) return;
    const referencing = (settings.data.models ?? []).filter(
      (m) => m.upstream === editingName,
    );
    if (referencing.length > 0) {
      pushToast({
        type: "error",
        title: "Cannot delete",
        message: `This upstream is still referenced by ${referencing.length} model(s): ${referencing.map((m) => m.public_name).join(", ")}. Please update or remove those model routes first.`,
      });
      return;
    }

    if (
      !window.confirm(
        `Mark upstream "${editingName}" for deletion? It will be removed when pending changes are committed.`,
      )
    )
      return;
    const nextUpstreams = (settings.data.upstreams ?? []).filter(
      (u) => u.name !== editingName,
    );
    stage({ upstreams: nextUpstreams });
    navigate("upstreams");
  }

  // Save Upstream
  function handleSave() {
    if (!general.name.trim()) {
      pushToast({
        type: "error",
        title: "Empty name",
        message: "Upstream name is required.",
      });
      return;
    }
    // The backend pins an OAuth endpoint as well, but resolving it here keeps the
    // form from ever offering another host — and from blocking the save of a
    // legacy upstream whose stored base_url is empty.
    const lockedBaseUrl = lockedOAuthBaseUrl(general.protocol);
    const resolvedBaseUrl = resolveBaseUrl(general.protocol, general.baseUrl);
    if (!resolvedBaseUrl) {
      pushToast({
        type: "error",
        title: "Empty Base URL",
        message: "Base URL is required.",
      });
      return;
    }
    if (!settings.data) return;

    const existingTarget = isEdit
      ? settings.data.upstreams.find((u) => u.name === editingName)
      : undefined;

    const pool: CredentialKeyDTO[] = keys.map((k, idx) => ({
      ref: k.ref || `${general.name}-cred-${idx + 1}`,
      api_key: k.secret,
      secret: k.secret,
      rps: k.rps > 0 ? k.rps : undefined,
      max_concurrent: k.maxConcurrent > 0 ? k.maxConcurrent : undefined,
    }));

    const extraHeadersObj: Record<string, string> = {};
    for (const h of general.extraHeaders) {
      if (h.key.trim() && h.value.trim()) {
        extraHeadersObj[h.key.trim()] = h.value.trim();
      }
    }

    const payload: UpstreamDTO = {
      ...(existingTarget || {}),
      name: general.name.trim(),
      protocol: canonicalProtocol(general.protocol),
      base_url: resolvedBaseUrl,
      base_urls:
        lockedBaseUrl === undefined && general.fallbackUrls.length > 0
          ? general.fallbackUrls
          : undefined,
      provider_id: providerId,
      key_strategy: keyStrategy,
      credential_pool: pool.length > 0 ? pool : undefined,
      egress_mode: general.egressMode,
      proxy_url:
        general.egressMode === "proxy"
          ? general.proxyUrl.trim() || undefined
          : undefined,
      allow_insecure: general.allowInsecure,
      timeout_ms: general.timeoutMs > 0 ? general.timeoutMs : undefined,
      idle_timeout_ms:
        general.idleTimeoutMs > 0 ? general.idleTimeoutMs : undefined,
      stream_idle_timeout_ms: existingTarget?.stream_idle_timeout_ms,
      max_idle_conns_per_host: existingTarget?.max_idle_conns_per_host,
      max_conns_per_host: existingTarget?.max_conns_per_host,
      probe_model: probeModel.trim() || undefined,
      extra_headers:
        Object.keys(extraHeadersObj).length > 0 ? extraHeadersObj : undefined,
      key_error_action: keyErrorAction,
      key_error_threshold: keyErrorThreshold,
      key_cooldown_duration_ms: keyCooldownDurationMs,
      key_error_rules: keyErrorRules,
      enabled: existingTarget ? existingTarget.enabled : true,
    };

    const currentList = settings.data.upstreams ?? [];
    let nextList: UpstreamDTO[];

    if (isEdit) {
      nextList = currentList.map((u) => (u.name === editingName ? payload : u));
    } else {
      if (currentList.some((u) => u.name === payload.name)) {
        pushToast({
          type: "error",
          title: "Duplicate name",
          message: `Upstream '${payload.name}' already exists.`,
        });
        return;
      }
      nextList = [...currentList, payload];
    }

    stage({ upstreams: nextList });
    pushToast({
      type: "info",
      title: "Upstream staged",
      message: `Upstream '${payload.name}' added to pending changes — commit to apply it.`,
    });
    navigate("upstreams");
  }

  return (
    <div className="page-col" style={{ paddingBottom: "90px" }}>
      <PageHeader
        title={isEdit ? `Edit Upstream: ${editingName}` : "Add Upstream"}
        description="Configure upstream host, credential pool, model auto-routing, and error policy."
        actions={
          <div style={{ display: "flex", gap: 8 }}>
            {isEdit && (
              <button
                type="button"
                className="btn btn-ghost"
                style={{ color: "var(--danger)" }}
                onClick={handleDeleteUpstream}
              >
                <Trash2 style={{ width: 14, height: 14 }} />
                Delete
              </button>
            )}
            <button
              className="btn btn-secondary"
              onClick={() => navigate("upstreams")}
            >
              <ArrowLeft style={{ width: 14, height: 14 }} />
              Back
            </button>
          </div>
        }
      />

      {/* Responsive Tab Bar: horizontal scrollable without overflow on mobile */}
      <div className="flex items-center gap-1.5 sm:gap-2 border-b border-[var(--line)] mb-5 overflow-x-auto pb-2 scrollbar-none -mx-4 px-4 sm:mx-0 sm:px-0">
        <button
          type="button"
          className={`btn shrink-0 whitespace-nowrap text-xs sm:text-sm py-2 px-3 sm:px-4 ${
            activeTab === "general" ? "btn-primary" : "btn-ghost"
          }`}
          onClick={() => setActiveTab("general")}
        >
          <Server className="w-4 h-4 shrink-0" />
          <span>
            General<span className="hidden sm:inline"> & Network</span>
          </span>
        </button>
        <button
          type="button"
          className={`btn shrink-0 whitespace-nowrap text-xs sm:text-sm py-2 px-3 sm:px-4 ${
            activeTab === "keys" ? "btn-primary" : "btn-ghost"
          }`}
          onClick={() => setActiveTab("keys")}
        >
          <KeyRound className="w-4 h-4 shrink-0" />
          <span>Keys</span>
          <span className="opacity-75">({keys.length})</span>
        </button>
        <button
          type="button"
          className={`btn shrink-0 whitespace-nowrap text-xs sm:text-sm py-2 px-3 sm:px-4 ${
            activeTab === "models" ? "btn-primary" : "btn-ghost"
          }`}
          onClick={() => setActiveTab("models")}
        >
          <Layers className="w-4 h-4 shrink-0" />
          <span>Models</span>
          <span className="opacity-75">({discoveredModels.length})</span>
        </button>
        <button
          type="button"
          className={`btn shrink-0 whitespace-nowrap text-xs sm:text-sm py-2 px-3 sm:px-4 ${
            activeTab === "resilience" ? "btn-primary" : "btn-ghost"
          }`}
          onClick={() => setActiveTab("resilience")}
        >
          <Shield className="w-4 h-4 shrink-0" />
          <span>Resilience</span>
        </button>
      </div>

      {activeTab === "general" && (
        <GeneralTab
          state={general}
          isEdit={isEdit}
          onChange={(patch) => setGeneral((prev) => ({ ...prev, ...patch }))}
        />
      )}

      {activeTab === "keys" && (
        <KeysTab
          providerId={providerId}
          keyStrategy={keyStrategy}
          keys={keys}
          upstreamName={general.name}
          protocol={general.protocol}
          baseUrl={general.baseUrl}
          egressMode={general.egressMode}
          proxyUrl={general.proxyUrl}
          probeModel={probeModel}
          onProviderChange={setProviderId}
          onStrategyChange={setKeyStrategy}
          onKeysChange={setKeys}
        />
      )}

      {activeTab === "models" && (
        <ModelsTab
          upstreamName={general.name}
          protocol={general.protocol}
          baseUrl={general.baseUrl}
          egressMode={general.egressMode}
          proxyUrl={general.proxyUrl}
          firstKey={keys[0]?.secret}
          probeModel={probeModel}
          discoveredModels={discoveredModels}
          latencyMs={modelsLatency}
          catalogNames={catalogModelNames}
          catalogModels={settings.data?.models ?? []}
          onProbeModelChange={setProbeModel}
          onDiscover={(mods, lat) => {
            setDiscoveredModels(mods);
            setModelsLatency(lat);
          }}
          onCreateRoute={handleCreateRoute}
          onCreateAll={handleCreateAllRoutes}
        />
      )}

      {activeTab === "resilience" && (
        <ResilienceTab
          keyErrorAction={keyErrorAction}
          keyErrorThreshold={keyErrorThreshold}
          keyCooldownDurationMs={keyCooldownDurationMs}
          keyErrorRules={keyErrorRules ?? []}
          onActionChange={setKeyErrorAction}
          onThresholdChange={setKeyErrorThreshold}
          onCooldownChange={setKeyCooldownDurationMs}
          onRulesChange={setKeyErrorRules}
        />
      )}

      {/* Bottom Save Bar: responsive padding & button sizes on mobile */}
      <div className="fixed bottom-0 left-0 right-0 bg-[var(--surface-overlay)] border-t border-[var(--line-strong)] px-3 sm:px-6 py-3 flex items-center justify-between gap-2 sm:gap-3 z-40">
        {isEdit ? (
          <button
            type="button"
            className="btn btn-ghost text-xs sm:text-sm px-2.5 sm:px-4 text-[var(--danger)] hover:bg-rose-500/10"
            onClick={handleDeleteUpstream}
          >
            <Trash2 className="w-3.5 h-3.5 sm:w-4 sm:h-4" />
            <span className="hidden sm:inline">Delete Upstream</span>
            <span className="sm:hidden">Delete</span>
          </button>
        ) : (
          <div />
        )}
        <div className="flex items-center gap-2 sm:gap-3">
          <button
            type="button"
            className="btn btn-secondary text-xs sm:text-sm px-3 sm:px-4"
            onClick={() => navigate("upstreams")}
          >
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-primary text-xs sm:text-sm px-3 sm:px-4 whitespace-nowrap"
            disabled={
              !general.name.trim() ||
              !resolveBaseUrl(general.protocol, general.baseUrl)
            }
            onClick={handleSave}
          >
            Save Upstream
          </button>
        </div>
      </div>
    </div>
  );
}
