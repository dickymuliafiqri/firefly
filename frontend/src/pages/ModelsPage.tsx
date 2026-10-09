import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUp, ArrowDown, Trash2, Plus, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/Badge";
import { Segmented } from "@/components/ui/Controls";
import { Drawer } from "@/components/ui/Drawer";
import { PageHeader } from "@/components/ui/PageHeader";
import { QueryGate } from "@/components/ui/QueryGate";
import {
  resolvePricing,
  useUpstreamModelsMutation,
  type UpstreamModelsRequest,
} from "@/services/api";
import type { ModelDTO, ComboDTO, PricingEntryDTO } from "@/services/schema";
import {
  isValidModelName,
  sanitizeModelName,
  findInvalidCatalogName,
  MODEL_NAME_HINT,
} from "@/services/schema";
import { useUiStore } from "@/state/store";
import { navigate } from "@/lib/router";
import { useDraftStore, useSettingsView } from "@/state/draftStore";
import { useModelCacheStore } from "@/services/modelCache";
import { resolveBaseUrl } from "@/components/upstream/GeneralTab";

interface ModelFormProps {
  editing: ModelDTO | null;
  onClose: () => void;
}

function ModelForm({ editing, onClose }: ModelFormProps) {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const pushToast = useUiStore((s) => s.pushToast);
  const modelsMutation = useUpstreamModelsMutation();
  const cache = useModelCacheStore((s) => s.cache);
  const setCachedModels = useModelCacheStore((s) => s.setModels);

  const [name, setName] = useState("");
  const [upstream, setUpstream] = useState("");
  const [upstreamModel, setUpstreamModel] = useState("");
  const [isManualModel, setIsManualModel] = useState(false);
  const [fallbacks, setFallbacks] = useState("");
  const [maxContext, setMaxContext] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [caps, setCaps] = useState({
    stream: true,
    tools: false,
    vision: false,
    json_mode: false,
    embeddings: false,
    audio: false,
  });

  // Cached discovered models for the selected upstream (populated by the
  // Upstream Editor's Models tab or the Fetch list button below).
  const availableCachedModels = upstream ? (cache[upstream] ?? []) : [];

  useEffect(() => {
    if (editing) {
      setName(editing.public_name);
      setUpstream(editing.upstream);
      setUpstreamModel(editing.upstream_model);
      setFallbacks((editing.fallback_upstreams ?? []).join(", "));
      setMaxContext(editing.max_context ? String(editing.max_context) : "");
      setSystemPrompt(editing.system_prompt ?? "");
      setCaps({
        stream: editing.capabilities?.stream ?? true,
        tools: editing.capabilities?.tools ?? false,
        vision: editing.capabilities?.vision ?? false,
        json_mode: editing.capabilities?.json_mode ?? false,
        embeddings: editing.capabilities?.embeddings ?? false,
        audio: editing.capabilities?.audio ?? false,
      });
      setIsManualModel(false);
    } else {
      setName("");
      setUpstream(settings.data?.upstreams[0]?.name ?? "");
      setUpstreamModel("");
      setFallbacks("");
      setMaxContext("");
      setSystemPrompt("");
      setCaps({
        stream: true,
        tools: false,
        vision: false,
        json_mode: false,
        embeddings: false,
        audio: false,
      });
      setIsManualModel(false);
    }
    // Only initialize when edit target changes. settings.data is refetched every
    // 5 seconds; adding it to dependencies would clear user input.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing]);

  // Populate default upstream only if still empty (e.g. drawer opened before
  // settings finished loading) — never overwrites user choice.
  useEffect(() => {
    if (!editing && !upstream && (settings.data?.upstreams.length ?? 0) > 0) {
      setUpstream(settings.data!.upstreams[0].name);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing, upstream, settings.data?.upstreams]);

  // Discovers the selected upstream's model list directly from this drawer and
  // persists it into the shared model cache for every view to reuse.
  async function handleFetchUpstreamModels() {
    if (!upstream || !settings.data) return;
    const target = settings.data.upstreams.find((u) => u.name === upstream);
    if (!target) return;
    const targetProtocol = target.protocol ?? "openai";
    const effectiveBaseUrl = resolveBaseUrl(
      targetProtocol,
      target.base_url ?? "",
    );
    if (!effectiveBaseUrl) {
      pushToast({
        type: "error",
        title: "Empty Base URL",
        message: "This upstream has no Base URL configured.",
      });
      return;
    }
    const firstKey =
      target.credential_pool?.[0]?.secret ||
      target.credential_pool?.[0]?.api_key ||
      target.api_keys?.[0] ||
      target.api_key;
    try {
      const payload: UpstreamModelsRequest = {
        name: upstream,
        protocol: targetProtocol,
        base_url: effectiveBaseUrl,
        api_key: firstKey || undefined,
        egress_mode: target.egress_mode,
        proxy_url: target.proxy_url || undefined,
      };
      const res = await modelsMutation.mutateAsync(payload);
      const fetched = res.models || [];
      if (fetched.length > 0) {
        setCachedModels(upstream, fetched);
        if (!upstreamModel) {
          setUpstreamModel(fetched[0]);
          // The upstream model id commonly carries characters the gateway
          // rejects in public names (`vendor/model`, `name:tag`), so the
          // auto-fill is sanitized to a valid default the user can still edit.
          if (!name.trim()) {
            const auto = sanitizeModelName(fetched[0]);
            if (auto) setName(auto);
          }
        }
        pushToast({
          type: "success",
          title: "Models fetched",
          message: `${fetched.length} model(s) cached for ${upstream}.`,
        });
      } else {
        pushToast({
          type: "info",
          title: "No models",
          message: "The upstream returned an empty model list.",
        });
      }
    } catch (err) {
      pushToast({
        type: "error",
        title: "Fetch failed",
        message: err instanceof Error ? err.message : "Unknown error",
      });
    }
  }

  function submit() {
    const publicName = name.trim();
    if (
      !settings.data ||
      !publicName ||
      !upstream.trim() ||
      !upstreamModel.trim()
    )
      return;
    // Mirror the backend's public_name rule here with a named message: sending
    // an invalid value would fail the whole save with a cryptic
    // `models[i].public_name: invalid or empty name` 400 aimed at an index.
    if (!isValidModelName(publicName)) {
      pushToast({
        type: "error",
        title: "Invalid model name",
        message: `"${publicName}" is not allowed. ${MODEL_NAME_HINT}`,
      });
      return;
    }
    const entry: ModelDTO = {
      public_name: publicName,
      upstream: upstream.trim(),
      upstream_model: upstreamModel.trim(),
      enabled: editing?.enabled ?? true,
      capabilities: caps,
      ...(maxContext.trim() ? { max_context: Number(maxContext) } : {}),
      ...(systemPrompt.trim() ? { system_prompt: systemPrompt.trim() } : {}),
      ...(fallbacks.trim()
        ? {
            fallback_upstreams: fallbacks
              .split(",")
              .map((f) => f.trim())
              .filter(Boolean),
          }
        : {}),
    };
    const others = settings.data.models.filter(
      (m) => m.public_name !== entry.public_name,
    );
    const nextModels = [...others, entry];
    // Pre-existing catalog entries were all validated when they were saved, but
    // name the offender explicitly instead of surfacing `models[i]` anyway.
    const bad = nextModels.find((m) => !isValidModelName(m.public_name));
    if (bad) {
      pushToast({
        type: "error",
        title: "Catalog has an invalid model name",
        message: `Model "${bad.public_name}" breaks the gateway name rule (${MODEL_NAME_HINT}). Edit or delete it, then save again.`,
      });
      return;
    }
    stage({ models: nextModels });
    onClose();
  }

  return (
    <Drawer
      open
      onClose={onClose}
      title={editing ? `Edit model: ${editing.public_name}` : "Add model"}
    >
      <div className="form-grid">
        <Field label="Public name" htmlFor="m-name" hint={MODEL_NAME_HINT}>
          <input
            id="m-name"
            value={name}
            placeholder="gpt-4o"
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Upstream" htmlFor="m-upstream">
          <select
            id="m-upstream"
            value={upstream}
            onChange={(e) => {
              setUpstream(e.target.value);
              setIsManualModel(false);
            }}
          >
            {(settings.data?.upstreams ?? []).map((u) => (
              <option key={u.name}>{u.name}</option>
            ))}
          </select>
        </Field>
      </div>
      <div className="field">
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: 8,
          }}
        >
          <label htmlFor="m-um">Upstream model</label>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            {availableCachedModels.length > 0 && (
              <button
                type="button"
                className="btn btn-ghost"
                style={{
                  fontSize: 11,
                  padding: "2px 6px",
                  textDecoration: "underline",
                }}
                onClick={() => setIsManualModel((prev) => !prev)}
              >
                {isManualModel ? "Select from list" : "Custom input"}
              </button>
            )}
            <button
              type="button"
              className="btn btn-ghost"
              style={{ fontSize: 11, padding: "2px 6px" }}
              disabled={modelsMutation.isPending || !upstream}
              onClick={() => void handleFetchUpstreamModels()}
              title="Fetch model list from this upstream and cache it"
            >
              <RefreshCw
                style={{
                  width: 11,
                  height: 11,
                  animation: modelsMutation.isPending
                    ? "spin 1s linear infinite"
                    : "none",
                }}
              />
              {modelsMutation.isPending ? "Fetching…" : "Fetch list"}
            </button>
          </div>
        </div>
        {!isManualModel && availableCachedModels.length > 0 ? (
          <select
            id="m-um"
            value={upstreamModel}
            className="mono"
            onChange={(e) => {
              const val = e.target.value;
              if (val === "__manual__") {
                setIsManualModel(true);
                return;
              }
              setUpstreamModel(val);
              // Upstream ids (`vendor/model`, `name:tag`) are invalid as public
              // names, so the auto-fill below is sanitized to a valid default.
              if (!name.trim()) {
                const auto = sanitizeModelName(val);
                if (auto) setName(auto);
              }
            }}
          >
            <option value="">
              -- Select upstream model ({availableCachedModels.length}{" "}
              available) --
            </option>
            {availableCachedModels.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
            <option value="__manual__">Custom / Enter manually…</option>
          </select>
        ) : (
          <input
            id="m-um"
            value={upstreamModel}
            className="mono"
            placeholder={
              availableCachedModels.length > 0
                ? "e.g. gpt-4o-2024-11-20"
                : "gpt-4o-2024-11-20"
            }
            onChange={(e) => setUpstreamModel(e.target.value)}
          />
        )}
        <span className="hint">Private model name on provider side.</span>
      </div>
      <Field
        label="Fallback upstreams (optional)"
        htmlFor="m-fb"
        hint="Comma-separated; used when primary upstream breaker is open."
      >
        <input
          id="m-fb"
          value={fallbacks}
          placeholder="openai-main, antigravity-prod"
          onChange={(e) => setFallbacks(e.target.value)}
        />
      </Field>
      <Field label="Max context tokens (optional)" htmlFor="m-ctx">
        <input
          id="m-ctx"
          type="number"
          value={maxContext}
          placeholder="128000"
          onChange={(e) => setMaxContext(e.target.value)}
        />
      </Field>
      <Field
        label="System prompt (optional)"
        htmlFor="m-sysprompt"
        hint="Appended to the system block of every chat completion routed to this model. Capped at ~32,000 tokens (128,000 chars) and forwarded verbatim. The global Token Saver guard is injected afterwards, so it keeps the last word."
      >
        <textarea
          id="m-sysprompt"
          rows={6}
          spellCheck={false}
          maxLength={128000}
          placeholder="e.g. You are a senior Go engineer. Prefer the standard library and keep answers terse."
          value={systemPrompt}
          onChange={(e) => setSystemPrompt(e.target.value)}
        />
      </Field>
      <div style={{ marginTop: -6, textAlign: "right" }}>
        <span className="hint">
          {systemPrompt.length.toLocaleString()} / 128,000 chars · ~
          {Math.ceil(systemPrompt.length / 4).toLocaleString()} / 32,000 tokens
        </span>
      </div>
      <div>
        <label
          style={{
            fontSize: 13,
            fontWeight: 500,
            color: "var(--muted)",
            display: "block",
            marginBottom: 6,
          }}
        >
          Capabilities
        </label>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(3, 1fr)",
            gap: 8,
            fontSize: 13,
          }}
        >
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.stream}
              onChange={(e) =>
                setCaps((c) => ({ ...c, stream: e.target.checked }))
              }
            />
            Stream
          </label>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.tools}
              onChange={(e) =>
                setCaps((c) => ({ ...c, tools: e.target.checked }))
              }
            />
            Tools
          </label>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.vision}
              onChange={(e) =>
                setCaps((c) => ({ ...c, vision: e.target.checked }))
              }
            />
            Vision
          </label>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.json_mode}
              onChange={(e) =>
                setCaps((c) => ({ ...c, json_mode: e.target.checked }))
              }
            />
            JSON Mode
          </label>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.embeddings}
              onChange={(e) =>
                setCaps((c) => ({ ...c, embeddings: e.target.checked }))
              }
            />
            Embeddings
          </label>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={caps.audio}
              onChange={(e) =>
                setCaps((c) => ({ ...c, audio: e.target.checked }))
              }
            />
            Audio
          </label>
        </div>
      </div>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          disabled={!name.trim() || !upstream.trim() || !upstreamModel.trim()}
          onClick={submit}
        >
          {editing ? "Save changes" : "Add model"}
        </button>
      </div>
    </Drawer>
  );
}

function Field(props: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="field">
      <label htmlFor={props.htmlFor}>{props.label}</label>
      {props.children}
      {props.hint ? <span className="hint">{props.hint}</span> : null}
    </div>
  );
}

function ComboForm({
  editing,
  onClose,
}: {
  editing: ComboDTO | null;
  onClose: () => void;
}) {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const pushToast = useUiStore((s) => s.pushToast);

  const [name, setName] = useState(editing?.name ?? "");
  const [strategy, setStrategy] = useState(editing?.strategy ?? "round_robin");
  const [selectedModels, setSelectedModels] = useState<string[]>(
    (editing?.models ?? []).slice(),
  );
  const [enabled, setEnabled] = useState(editing?.enabled !== false);

  const allCatalogModels = settings.data?.models ?? [];
  const availableToPick = allCatalogModels.filter(
    (m) => !selectedModels.includes(m.public_name),
  );

  function toggleModel(publicName: string) {
    setSelectedModels((prev) =>
      prev.includes(publicName)
        ? prev.filter((m) => m !== publicName)
        : [...prev, publicName],
    );
  }

  function moveModel(idx: number, dir: "up" | "down") {
    setSelectedModels((prev) => {
      const next = prev.slice();
      const target = dir === "up" ? idx - 1 : idx + 1;
      if (target < 0 || target >= next.length) return prev;
      [next[idx], next[target]] = [next[target], next[idx]];
      return next;
    });
  }

  function submit() {
    const comboName = name.trim();
    if (!settings.data || !comboName || selectedModels.length === 0) return;
    // Combos share the backend's public-name rule (`combos[i].name`), and an
    // invalid combo name fails the save the same way an invalid model name
    // does — reject it here with the actual name quoted.
    if (!isValidModelName(comboName)) {
      pushToast({
        type: "error",
        title: "Invalid combo name",
        message: `"${comboName}" is not allowed. ${MODEL_NAME_HINT}`,
      });
      return;
    }
    const entry: ComboDTO = {
      name: comboName,
      strategy,
      models: selectedModels,
      enabled,
    };
    const others = (settings.data.combos ?? []).filter(
      (c) => c.name !== entry.name,
    );
    const nextModels = settings.data.models ?? [];
    const nextCombos = [...others, entry];
    // The payload also carries the untouched models and combos, so name the
    // real offender when something already in the catalog is invalid.
    const bad = findInvalidCatalogName(nextModels, nextCombos);
    if (bad) {
      pushToast({
        type: "error",
        title: "Catalog has an invalid name",
        message: `${bad.kind === "model" ? "Model" : "Combo"} "${bad.name}" breaks the gateway name rule (${MODEL_NAME_HINT}). Edit or delete it, then save again.`,
      });
      return;
    }
    stage({ combos: nextCombos });
    onClose();
  }

  return (
    <Drawer
      open
      onClose={onClose}
      title={editing ? `Edit combo: ${editing.name}` : "Add combo"}
    >
      <Field label="Combo name" htmlFor="c-name" hint={MODEL_NAME_HINT}>
        <input
          id="c-name"
          value={name}
          placeholder="smart-combo"
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <Field label="Strategy" htmlFor="c-strat">
        <select
          id="c-strat"
          value={strategy}
          onChange={(e) => setStrategy(e.target.value)}
        >
          <option value="round_robin">round_robin</option>
          <option value="least_inflight">least_inflight</option>
          <option value="failover">failover</option>
        </select>
      </Field>
      <div className="field">
        <label>
          Member models ({selectedModels.length} selected)
          {strategy === "failover" ? " — first model is primary" : ""}
        </label>

        {/* Selected ordered chips */}
        {selectedModels.length > 0 ? (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 6,
              padding: 8,
              border: "1px solid var(--line)",
              borderRadius: 8,
              marginBottom: 8,
            }}
          >
            {selectedModels.map((m, idx) => {
              const target = allCatalogModels.find(
                (cm) => cm.public_name === m,
              );
              return (
                <div
                  key={m}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    gap: 8,
                    padding: "6px 8px",
                    border: "1px solid var(--line)",
                    borderRadius: 6,
                    background: "var(--surface-raised)",
                  }}
                >
                  <div
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 8,
                      minWidth: 0,
                    }}
                  >
                    <span
                      className="num"
                      style={{ width: 18, textAlign: "center", fontSize: 11 }}
                    >
                      {idx + 1}.
                    </span>
                    <span className="mono truncate">{m}</span>
                    <span className="faint" style={{ fontSize: 11 }}>
                      → {target?.upstream || "unknown"}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 2,
                      flexShrink: 0,
                    }}
                  >
                    <button
                      type="button"
                      className="btn btn-ghost p-1"
                      title="Move up"
                      disabled={idx === 0}
                      onClick={() => moveModel(idx, "up")}
                    >
                      <ArrowUp style={{ width: 13, height: 13 }} />
                    </button>
                    <button
                      type="button"
                      className="btn btn-ghost p-1"
                      title="Move down"
                      disabled={idx === selectedModels.length - 1}
                      onClick={() => moveModel(idx, "down")}
                    >
                      <ArrowDown style={{ width: 13, height: 13 }} />
                    </button>
                    <button
                      type="button"
                      className="btn btn-ghost p-1"
                      title="Remove from combo"
                      style={{ color: "var(--danger)" }}
                      onClick={() => toggleModel(m)}
                    >
                      <Trash2 style={{ width: 13, height: 13 }} />
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        ) : (
          <div
            style={{
              padding: 12,
              textAlign: "center",
              border: "1px dashed var(--line)",
              borderRadius: 8,
              marginBottom: 8,
              fontSize: 12,
            }}
            className="faint"
          >
            No member models selected. Pick from the catalog below.
          </div>
        )}
        {/* Available catalog models as toggle chips */}
        {availableToPick.length > 0 ? (
          <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
            {availableToPick.map((m) => (
              <button
                key={m.public_name}
                type="button"
                className="btn btn-secondary"
                style={{ fontSize: 11, padding: "3px 8px" }}
                title={`Add ${m.public_name} (${m.upstream})`}
                onClick={() => toggleModel(m.public_name)}
              >
                <Plus style={{ width: 11, height: 11 }} />
                {m.public_name}
                <span className="faint" style={{ fontSize: 10 }}>
                  ({m.upstream})
                </span>
              </button>
            ))}
          </div>
        ) : (
          <span className="faint" style={{ fontSize: 11 }}>
            {allCatalogModels.length === 0
              ? "No catalog models exist yet — create models first."
              : "All catalog models are already in this combo."}
          </span>
        )}
        <span className="hint">
          Click catalog models to add them; order defines priority.
        </span>
      </div>
      <Field label="Enabled" htmlFor="c-en">
        <select
          id="c-en"
          value={enabled ? "yes" : "no"}
          onChange={(e) => setEnabled(e.target.value === "yes")}
        >
          <option value="yes">Yes</option>
          <option value="no">No</option>
        </select>
      </Field>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          disabled={!name.trim() || selectedModels.length === 0}
          onClick={submit}
        >
          {editing ? "Save changes" : "Add combo"}
        </button>
      </div>
    </Drawer>
  );
}

/** Prices are integer micro-USD per 1M tokens, numerically identical to a
 *  provider's USD-per-1M figure, so the table shows the provider's own number. */
function perM(micros: number | undefined): string {
  if (micros === undefined || micros === null) return "—";
  return `$${(micros / 1_000_000).toFixed(3)}`;
}

export function ModelsPage() {
  const [panel, setPanel] = useState("direct");
  const [query, setQuery] = useState("");
  const [pricingFilter, setPricingFilter] = useState("all");
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<ModelDTO | null>(null);
  const [comboFormOpen, setComboFormOpen] = useState(false);
  const [editingCombo, setEditingCombo] = useState<ComboDTO | null>(null);

  const allModels = settings.data?.models ?? [];
  const allCombos = settings.data?.combos ?? [];

  const pricingResolve = useQuery({
    queryKey: ["pricing", "resolve", "models"],
    queryFn: () =>
      resolvePricing(
        allModels.map((m) => ({
          public_name: m.public_name,
          upstream: m.upstream,
          upstream_model: m.upstream_model,
        })),
      ),
    enabled: allModels.length > 0,
  });

  // "Is this model priced?" cannot be answered client-side: the
  // protocol-to-provider mapping and the wildcard rules live on the backend.
  // One batch resolve covers the whole table, so the filter is exact rather
  // than a guess based on the model name.
  const pricedNames = useMemo(
    () =>
      new Set(
        (pricingResolve.data?.entries ?? [])
          .filter((e) => e.entry !== null)
          .map((e) => e.public_name),
      ),
    [pricingResolve.data],
  );

  const priceByModel = useMemo(() => {
    const map = new Map<string, PricingEntryDTO>();
    for (const e of pricingResolve.data?.entries ?? []) {
      if (e.entry !== null) map.set(e.public_name, e.entry);
    }
    return map;
  }, [pricingResolve.data]);

  const q = query.trim().toLowerCase();
  const models = allModels.filter((m) => {
    if (q && !m.public_name.toLowerCase().includes(q) && !m.upstream.toLowerCase().includes(q)) {
      return false;
    }
    if (pricingFilter === "registered") return pricedNames.has(m.public_name);
    if (pricingFilter === "unregistered") return !pricedNames.has(m.public_name);
    return true;
  });
  const combos = q
    ? allCombos.filter(
        (c) =>
          c.name.toLowerCase().includes(q) ||
          c.models.some((m) => m.toLowerCase().includes(q)),
      )
    : allCombos;

  function mutateModels(fn: (list: ModelDTO[]) => ModelDTO[]) {
    if (!settings.data) return;
    stage({ models: fn(settings.data.models) });
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Models"
        description="Direct model routing catalog and virtual combo fallback chains."
        actions={
          panel === "direct" ? (
            <button
              className="btn btn-primary"
              onClick={() => {
                setEditing(null);
                setFormOpen(true);
              }}
            >
              Add Model
            </button>
          ) : (
            <button
              className="btn btn-primary"
              onClick={() => {
                setEditingCombo(null);
                setComboFormOpen(true);
              }}
            >
              Add Combo
            </button>
          )
        }
      />

      <div
        className="filter-bar"
        style={{ display: "flex", gap: 12, alignItems: "center" }}
      >
        <Segmented
          items={[
            { id: "direct", label: "Direct Models" },
            { id: "combos", label: "Virtual Combos" },
          ]}
          value={panel}
          onChange={setPanel}
          ariaLabel="Model panel"
        />
        {panel === "direct" ? (
          <Segmented
            items={[
              { id: "all", label: "All pricing" },
              { id: "registered", label: "Registered" },
              { id: "unregistered", label: "Not registered" },
            ]}
            value={pricingFilter}
            onChange={setPricingFilter}
            ariaLabel="Filter by pricing"
          />
        ) : null}
        <input
          type="search"
          placeholder="Search models…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ marginLeft: "auto", width: 220 }}
        />
      </div>

      <QueryGate isLoading={settings.isLoading} error={settings.error}>
        {panel === "direct" ? (
          <div className="card">
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Public name</th>
                    <th>Upstream</th>
                    <th>Upstream model</th>
                    <th className="num">Fallbacks</th>
                    <th className="num">In / 1M</th>
                    <th className="num">Out / 1M</th>
                    <th>Status</th>
                    <th className="num">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {models.map((m) => {
                    const price = priceByModel.get(m.public_name);
                    return (
                    <tr key={m.public_name}>
                      <td>{m.public_name}</td>
                      <td className="dim">{m.upstream}</td>
                      <td className="mono dim">{m.upstream_model}</td>
                      <td className="num">
                        {m.fallback_upstreams?.length ?? 0}
                      </td>
                      <td className="num">
                        {price ? (
                          perM(price.input_micros_per_m)
                        ) : (
                          <button
                            className="btn btn-ghost"
                            onClick={() => navigate("pricing", { model: m.public_name })}
                          >
                            Set price
                          </button>
                        )}
                      </td>
                      <td className="num">
                        {price ? perM(price.output_micros_per_m) : "—"}
                      </td>
                      <td>
                        <Badge tone={m.enabled === false ? "neutral" : "ok"}>
                          {m.enabled === false ? "DISABLED" : "ACTIVE"}
                        </Badge>
                      </td>
                      <td className="num">
                        <button
                          className="btn btn-ghost"
                          onClick={() => {
                            setEditing(m);
                            setFormOpen(true);
                          }}
                        >
                          Edit
                        </button>{" "}
                        <button
                          className="btn btn-ghost"
                          onClick={() =>
                            mutateModels((list) =>
                              list.map((x) =>
                                x.public_name === m.public_name
                                  ? { ...x, enabled: x.enabled === false }
                                  : x,
                              ),
                            )
                          }
                        >
                          {m.enabled === false ? "Enable" : "Disable"}
                        </button>{" "}
                        <button
                          className="btn btn-ghost"
                          onClick={() =>
                            mutateModels((list) =>
                              list.filter(
                                (x) => x.public_name !== m.public_name,
                              ),
                            )
                          }
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                    );
                  })}
                  {models.length === 0 ? (
                    <tr>
                      <td colSpan={8} className="faint">
                        Model catalog is empty.
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </div>
        ) : (
          <div className="stack">
            {combos.map((combo) => (
              <div className="card" key={combo.name}>
                <div className="card-header">
                  <h2>
                    {combo.name}{" "}
                    <Badge tone="info" className="ml-2">
                      {(combo.strategy ?? "round_robin").toUpperCase()}
                    </Badge>
                    {combo.enabled === false ? (
                      <Badge tone="neutral" className="ml-2">
                        DISABLED
                      </Badge>
                    ) : null}
                  </h2>
                  <div style={{ display: "flex", gap: 6 }}>
                    <button
                      className="btn btn-ghost"
                      onClick={() => {
                        setEditingCombo(combo);
                        setComboFormOpen(true);
                      }}
                    >
                      Edit
                    </button>
                    <button
                      className="btn btn-ghost"
                      onClick={() => {
                        if (!settings.data) return;
                        const nextCombos = (settings.data.combos ?? []).filter(
                          (c) => c.name !== combo.name,
                        );
                        stage({ combos: nextCombos });
                      }}
                    >
                      Delete
                    </button>
                  </div>
                </div>
                <div className="card-body">
                  <ul className="chain">
                    {combo.models.map((name, i) => (
                      <li key={name}>
                        <span className="step">
                          {String(i + 1).padStart(2, "0")}
                        </span>
                        {name}
                        {models.find((m) => m.public_name === name) ? (
                          <>
                            {" "}
                            <span className="arrow">&rarr;</span>{" "}
                            {
                              models.find((m) => m.public_name === name)!
                                .upstream
                            }
                          </>
                        ) : null}
                        <span className="weight">
                          {combo.strategy === "failover"
                            ? `priority ${i + 1}`
                            : "equal"}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              </div>
            ))}
            {combos.length === 0 ? (
              <div className="card">
                <div className="card-body">
                  <span className="faint" style={{ fontSize: 13 }}>
                    No virtual combos yet.
                  </span>
                </div>
              </div>
            ) : null}
          </div>
        )}
      </QueryGate>

      {formOpen ? (
        <ModelForm editing={editing} onClose={() => setFormOpen(false)} />
      ) : null}
      {comboFormOpen ? (
        <ComboForm
          editing={editingCombo}
          onClose={() => setComboFormOpen(false)}
        />
      ) : null}
    </div>
  );
}
