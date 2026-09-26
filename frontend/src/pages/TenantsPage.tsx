import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/Badge";
import { Drawer } from "@/components/ui/Drawer";
import { PageHeader } from "@/components/ui/PageHeader";
import { QueryGate } from "@/components/ui/QueryGate";
import {
  useSettingsQuery,
  useSaveSettingsSmart,
  withSettings,
  useTopupTenantMutation,
} from "@/services/api";
import type { TenantDTO } from "@/services/schema";
import { useUiStore } from "@/state/store";

const STATUS_TONE: Record<string, "ok" | "warn" | "danger" | "neutral"> = {
  active: "ok",
  suspended: "warn",
  revoked: "danger",
  exhausted: "warn",
  expired: "warn",
};

function maskKey(key: string | undefined): string {
  if (!key) return "—";
  return "sk-gw-••••" + key.slice(-4);
}

function randomKey(): string {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  return (
    "sk-gw-" +
    Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
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

function TopupModal({
  tenant,
  onClose,
}: {
  tenant: TenantDTO;
  onClose: () => void;
}) {
  const topup = useTopupTenantMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [tokens, setTokens] = useState("");
  const [days, setDays] = useState("");
  const [reset, setReset] = useState(false);

  function submit() {
    topup.mutate(
      {
        tenant_name: tenant.name,
        ...(tokens.trim() ? { add_tokens: parseInt(tokens, 10) } : {}),
        ...(days.trim() ? { extend_days: parseInt(days, 10) } : {}),
        ...(reset ? { reset_used: true } : {}),
      },
      {
        onSuccess: (d) => {
          pushToast({
            type: "success",
            title: "Quota added",
            message: d.message || `${tenant.name} updated.`,
          });
          onClose();
        },
        onError: (e) =>
          pushToast({
            type: "error",
            title: "Failed",
            message: e instanceof Error ? e.message : "Unknown",
          }),
      },
    );
  }

  return (
    <Drawer open onClose={onClose} title={`Top up: ${tenant.name}`}>
      <Field label="Add tokens" htmlFor="tp-tokens">
        <input
          id="tp-tokens"
          type="number"
          value={tokens}
          placeholder="100000"
          onChange={(e) => setTokens(e.target.value)}
        />
      </Field>
      <Field label="Extend days" htmlFor="tp-days">
        <input
          id="tp-days"
          type="number"
          value={days}
          placeholder="30"
          onChange={(e) => setDays(e.target.value)}
        />
      </Field>
      <label
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          fontSize: 13,
          cursor: "pointer",
        }}
      >
        <input
          type="checkbox"
          checked={reset}
          onChange={(e) => setReset(e.target.checked)}
        />
        Reset used tokens
      </label>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          disabled={
            topup.isPending || (!tokens.trim() && !days.trim() && !reset)
          }
          onClick={submit}
        >
          {topup.isPending ? "Saving…" : "Top up"}
        </button>
      </div>
    </Drawer>
  );
}

interface TenantFormProps {
  editing: TenantDTO | null;
  onClose: () => void;
}

function TenantForm({ editing, onClose }: TenantFormProps) {
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);

  const [name, setName] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [status, setStatus] = useState("active");
  const [rps, setRps] = useState("10");
  const [maxConc, setMaxConc] = useState("8");
  const [models, setModels] = useState("");
  const [maxTokens, setMaxTokens] = useState("");
  const [expires, setExpires] = useState("");

  useEffect(() => {
    if (editing) {
      setName(editing.name);
      setApiKey(editing.api_key ?? "");
      setStatus(String(editing.status ?? "active"));
      setRps(String(editing.rate_limit?.rps ?? 10));
      setMaxConc(String(editing.rate_limit?.max_concurrent ?? 8));
      setModels((editing.allowed_models ?? []).join(", "));
      setMaxTokens(
        editing.max_tokens !== undefined ? String(editing.max_tokens) : "",
      );
      setExpires(
        editing.expires_at
          ? new Date(editing.expires_at * 1000).toISOString().slice(0, 16)
          : "",
      );
    } else {
      setName("");
      setApiKey(randomKey());
      setStatus("active");
      setRps("10");
      setMaxConc("8");
      setModels("");
      setMaxTokens("");
      setExpires("");
    }
  }, [editing]);

  function submit() {
    if (!settings.data || !name.trim()) return;
    const entry: TenantDTO = {
      name: name.trim(),
      api_key: apiKey.trim() || randomKey(),
      status: status as TenantDTO["status"],
      allowed_models: models
        .split(",")
        .map((m) => m.trim())
        .filter(Boolean),
      rate_limit: {
        rps: Math.max(1, parseInt(rps, 10) || 10),
        max_concurrent: Math.max(1, parseInt(maxConc, 10) || 8),
      },
      ...(maxTokens.trim() ? { max_tokens: parseInt(maxTokens, 10) } : {}),
      ...(expires
        ? { expires_at: Math.floor(new Date(expires).getTime() / 1000) }
        : {}),
    };
    const others = settings.data.tenants.filter((t) => t.name !== entry.name);
    const next = withSettings(settings.data, { tenants: [...others, entry] });
    save.mutate(next, {
      onSuccess: (d) =>
        pushToast({
          type: "success",
          title: editing ? "Tenant updated" : "Tenant created",
          message: d.local
            ? "Demo mode: changes are local to browser only."
            : "Catalog synchronized.",
        }),
      onError: (e) =>
        pushToast({
          type: "error",
          title: "Save failed",
          message: e instanceof Error ? e.message : "Unknown",
        }),
    });
    onClose();
  }

  return (
    <Drawer
      open
      onClose={onClose}
      title={editing ? `Edit tenant: ${editing.name}` : "Create tenant"}
    >
      <div className="form-grid">
        <Field label="Name" htmlFor="t-name">
          <input
            id="t-name"
            type="text"
            value={name}
            placeholder="work"
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Status" htmlFor="t-status">
          <select
            id="t-status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            <option value="active">active</option>
            <option value="suspended">suspended</option>
          </select>
        </Field>
      </div>
      <Field
        label="API key"
        htmlFor="t-key"
        hint="Randomly generated in browser. Copy now — dashboard only displays a masked hint after this."
      >
        <div style={{ display: "flex", gap: 8 }}>
          <input
            id="t-key"
            type="text"
            value={apiKey}
            className="mono"
            onChange={(e) => setApiKey(e.target.value)}
          />
          <button
            className="btn btn-secondary"
            style={{ flexShrink: 0 }}
            onClick={() => setApiKey(randomKey())}
          >
            Regenerate
          </button>
        </div>
      </Field>
      <div className="form-grid">
        <Field label="RPS limit" htmlFor="t-rps">
          <input
            id="t-rps"
            type="number"
            value={rps}
            className="mono"
            onChange={(e) => setRps(e.target.value)}
          />
        </Field>
        <Field label="Max concurrent" htmlFor="t-mc">
          <input
            id="t-mc"
            type="number"
            value={maxConc}
            className="mono"
            onChange={(e) => setMaxConc(e.target.value)}
          />
        </Field>
      </div>
      <Field
        label="Allowed models"
        htmlFor="t-models"
        hint="Comma-separated. Leave empty to allow all catalog models."
      >
        <input
          id="t-models"
          type="text"
          value={models}
          placeholder="gpt-4o, claude-sonnet-4-5"
          onChange={(e) => setModels(e.target.value)}
        />
      </Field>
      <div className="form-grid">
        <Field
          label="Max tokens (optional)"
          htmlFor="t-mt"
          hint="Total token quota; empty = unlimited."
        >
          <input
            id="t-mt"
            type="number"
            value={maxTokens}
            className="mono"
            placeholder="1000000"
            onChange={(e) => setMaxTokens(e.target.value)}
          />
        </Field>
        <Field label="Expires (optional)" htmlFor="t-exp">
          <input
            id="t-exp"
            type="datetime-local"
            value={expires}
            onChange={(e) => setExpires(e.target.value)}
          />
        </Field>
      </div>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          disabled={!name.trim() || save.isPending}
          onClick={submit}
        >
          {editing ? "Save changes" : "Create tenant"}
        </button>
      </div>
    </Drawer>
  );
}

export function TenantsPage() {
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<TenantDTO | null>(null);
  const [query, setQuery] = useState("");
  const [topupTarget, setTopupTarget] = useState<TenantDTO | null>(null);
  const [revealedKey, setRevealedKey] = useState<string | null>(null);

  const allTenants = settings.data?.tenants ?? [];
  const q = query.trim().toLowerCase();
  const tenants = q
    ? allTenants.filter((t) => t.name.toLowerCase().includes(q))
    : allTenants;

  function copyKey(t: TenantDTO) {
    if (!t.api_key || t.api_key.includes("•")) {
      pushToast({
        type: "info",
        title: "Key unavailable",
        message: "Key is only displayed once upon creation.",
      });
      return;
    }
    void navigator.clipboard
      .writeText(t.api_key)
      .then(() =>
        pushToast({
          type: "success",
          title: "Key copied",
          message: `API key for ${t.name} copied.`,
        }),
      )
      .catch(() =>
        pushToast({
          type: "error",
          title: "Failed to copy",
          message: "Clipboard is unavailable.",
        }),
      );
  }

  function toggleStatus(t: TenantDTO) {
    if (!settings.data) return;
    const nextStatus = String(t.status) === "active" ? "suspended" : "active";
    const next = withSettings(settings.data, {
      tenants: settings.data.tenants.map((x) =>
        x.name === t.name ? { ...x, status: nextStatus } : x,
      ),
    });
    save.mutate(next, {
      onSuccess: () =>
        pushToast({
          type: "success",
          title:
            nextStatus === "active" ? "Tenant activated" : "Tenant suspended",
          message: `${t.name} → ${nextStatus}.`,
        }),
      onError: (e) =>
        pushToast({
          type: "error",
          title: "Failed",
          message: e instanceof Error ? e.message : "Unknown",
        }),
    });
  }

  function deleteTenant(name: string) {
    if (!settings.data) return;
    const next = withSettings(settings.data, {
      tenants: settings.data.tenants.filter((t) => t.name !== name),
    });
    save.mutate(next, {
      onSuccess: (d) =>
        pushToast({
          type: "success",
          title: "Tenant deleted",
          message: d.local
            ? "Demo mode: changes are local to browser only."
            : "Catalog synchronized.",
        }),
      onError: (e) =>
        pushToast({
          type: "error",
          title: "Save failed",
          message: e instanceof Error ? e.message : "Unknown",
        }),
    });
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Tenants"
        description="Tenant authentication, rate limits, and model access."
        actions={
          <button
            className="btn btn-primary"
            onClick={() => {
              setEditing(null);
              setFormOpen(true);
            }}
          >
            Create Tenant
          </button>
        }
      />

      <div
        className="filter-bar"
        style={{
          display: "flex",
          justifyContent: "flex-end",
          marginBottom: 12,
        }}
      >
        <input
          type="search"
          placeholder="Search tenants…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ width: 220 }}
        />
      </div>

      <QueryGate isLoading={settings.isLoading} error={settings.error}>
        <div className="card">
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>API key</th>
                  <th className="num">RPS limit</th>
                  <th className="num">Max concurrent</th>
                  <th className="num">Models</th>
                  <th className="num">Quota</th>
                  <th>Status</th>
                  <th className="num">Actions</th>
                </tr>
              </thead>
              <tbody>
                {tenants.map((t) => {
                  const remaining =
                    t.max_tokens !== undefined
                      ? t.max_tokens - (t.used_tokens ?? 0)
                      : undefined;
                  return (
                    <tr key={t.name}>
                      <td>{t.name}</td>
                      <td className="mono" style={{ fontSize: 12 }}>
                        {revealedKey === t.name
                          ? t.api_key
                          : maskKey(t.api_key)}{" "}
                        <button
                          className="btn btn-ghost"
                          style={{ padding: "2px 6px" }}
                          onClick={() =>
                            setRevealedKey(
                              revealedKey === t.name ? null : t.name,
                            )
                          }
                        >
                          {revealedKey === t.name ? "Hide" : "Show"}
                        </button>
                        <button
                          className="btn btn-ghost"
                          style={{ padding: "2px 6px" }}
                          onClick={() => copyKey(t)}
                        >
                          Copy
                        </button>
                      </td>
                      <td className="num">{t.rate_limit?.rps ?? "—"}</td>
                      <td className="num">
                        {t.rate_limit?.max_concurrent ?? "—"}
                      </td>
                      <td className="num">{t.allowed_models?.length ?? 0}</td>
                      <td className="num">
                        {remaining !== undefined
                          ? remaining.toLocaleString()
                          : "—"}
                        {t.expires_at ? (
                          <span className="sub">
                            exp{" "}
                            {new Date(t.expires_at * 1000).toLocaleDateString()}
                          </span>
                        ) : null}
                      </td>
                      <td>
                        <Badge
                          tone={STATUS_TONE[String(t.status)] ?? "neutral"}
                        >
                          {String(t.status).toUpperCase()}
                        </Badge>
                      </td>
                      <td className="num">
                        <button
                          className="btn btn-ghost"
                          onClick={() => {
                            setEditing(t);
                            setFormOpen(true);
                          }}
                        >
                          Edit
                        </button>{" "}
                        <button
                          className="btn btn-ghost"
                          onClick={() => setTopupTarget(t)}
                        >
                          Top up
                        </button>{" "}
                        <button
                          className="btn btn-ghost"
                          onClick={() => toggleStatus(t)}
                        >
                          {String(t.status) === "active"
                            ? "Suspend"
                            : "Activate"}
                        </button>{" "}
                        <button
                          className="btn btn-ghost"
                          onClick={() => deleteTenant(t.name)}
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  );
                })}
                {tenants.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="faint">
                      No tenants registered yet.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
      </QueryGate>

      {formOpen ? (
        <TenantForm editing={editing} onClose={() => setFormOpen(false)} />
      ) : null}
      {topupTarget ? (
        <TopupModal tenant={topupTarget} onClose={() => setTopupTarget(null)} />
      ) : null}
    </div>
  );
}
