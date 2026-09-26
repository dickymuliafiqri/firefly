import { useState } from "react";
import { Badge } from "@/components/ui/Badge";
import { Drawer } from "@/components/ui/Drawer";
import { Field } from "@/components/ui/Controls";
import { PageHeader } from "@/components/ui/PageHeader";
import { QueryGate } from "@/components/ui/QueryGate";
import {
  useProvidersQuery,
  useProviderKeysQuery,
  usePatchProviderKeyMutation,
  useUpsertProviderKeysMutation,
  useDeleteProviderKeyMutation,
  useCreateProviderMutation,
  useUpdateProviderMutation,
  useDeleteProviderMutation,
} from "@/services/api";
import type { ProviderRecordDTO } from "@/services/schema";
import { useUiStore } from "@/state/store";

function ProviderForm({
  provider,
  onClose,
}: {
  provider: ProviderRecordDTO | "new" | null;
  onClose: () => void;
}) {
  const create = useCreateProviderMutation();
  const update = useUpdateProviderMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const editing = provider && provider !== "new" ? provider : null;
  const [name, setName] = useState(editing?.name ?? "");
  const [baseUrl, setBaseUrl] = useState(editing?.base_url ?? "");
  const [description, setDescription] = useState(editing?.description ?? "");
  const [active, setActive] = useState(editing?.is_active ?? true);
  const pending = create.isPending || update.isPending;

  function submit() {
    if (!name.trim() || !baseUrl.trim() || pending) return;
    if (editing) {
      update.mutate(
        {
          id: editing.id,
          payload: {
            base_url: baseUrl.trim(),
            description: description.trim(),
            is_active: active,
          },
        },
        {
          onSuccess: () => {
            pushToast({
              type: "success",
              title: "Provider updated",
              message: `${editing.name} saved.`,
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
    } else {
      create.mutate(
        {
          name: name.trim(),
          base_url: baseUrl.trim(),
          description: description.trim(),
          is_active: active,
        },
        {
          onSuccess: () => {
            pushToast({
              type: "success",
              title: "Provider created",
              message: `${name.trim()} saved.`,
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
  }

  return (
    <Drawer
      open={provider !== null}
      onClose={onClose}
      title={editing ? `Edit: ${editing.name}` : "Add provider"}
    >
      <Field label="Name" htmlFor="p-name">
        <input
          id="p-name"
          type="text"
          value={name}
          disabled={Boolean(editing)}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <Field label="Base URL" htmlFor="p-url">
        <input
          id="p-url"
          type="url"
          className="mono"
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
        />
      </Field>
      <Field label="Description" htmlFor="p-desc">
        <input
          id="p-desc"
          type="text"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      <Field label="Status" htmlFor="p-active">
        <select
          id="p-active"
          value={active ? "yes" : "no"}
          onChange={(e) => setActive(e.target.value === "yes")}
        >
          <option value="yes">Active</option>
          <option value="no">Inactive</option>
        </select>
      </Field>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button className="btn btn-ghost" onClick={onClose}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          disabled={!name.trim() || !baseUrl.trim() || pending}
          onClick={submit}
        >
          {pending ? "Saving…" : editing ? "Save" : "Create"}
        </button>
      </div>
    </Drawer>
  );
}

function KeysDrawer({
  provider,
  onClose,
}: {
  provider: ProviderRecordDTO | null;
  onClose: () => void;
}) {
  const keys = useProviderKeysQuery(provider?.id ?? null);
  const patchKey = usePatchProviderKeyMutation();
  const upsert = useUpsertProviderKeysMutation();
  const deleteKey = useDeleteProviderKeyMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [bulk, setBulk] = useState("");

  function handleUpsert() {
    if (!provider) return;
    const entries = bulk
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean)
      .map((api_key) => ({ api_key }));
    if (entries.length === 0) return;
    upsert.mutate(
      { providerId: provider.id, entries },
      {
        onSuccess: (d) => {
          setBulk("");
          pushToast({
            type: "success",
            title: "Keys saved",
            message: `${d.created} new, ${d.updated} updated.`,
          });
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
    <Drawer
      open={provider !== null}
      onClose={onClose}
      title={`Keys: ${provider?.name ?? ""}`}
    >
      <QueryGate isLoading={keys.isLoading} error={keys.error}>
        <div style={{ marginBottom: 20 }}>
          <Field
            label="Batch upsert keys (satu secret per baris)"
            htmlFor="p-bulk"
          >
            <textarea
              id="p-bulk"
              rows={4}
              spellCheck={false}
              placeholder="sk-..."
              value={bulk}
              onChange={(e) => setBulk(e.target.value)}
            />
          </Field>
          <div
            style={{
              display: "flex",
              justifyContent: "flex-end",
              marginTop: 8,
            }}
          >
            <button
              className="btn btn-primary"
              disabled={!provider || !bulk.trim() || upsert.isPending}
              onClick={handleUpsert}
            >
              {upsert.isPending ? "Saving…" : "Add keys"}
            </button>
          </div>
        </div>

        <div>
          <h3>Stored credentials (hint-only)</h3>
          <div className="kv-list">
            {(keys.data?.keys ?? []).map((key) => (
              <div key={key.id}>
                <span className="k mono" style={{ fontSize: 12 }}>
                  {key.api_key_hint}
                </span>
                <span
                  className="v"
                  style={{ display: "flex", gap: 6, alignItems: "center" }}
                >
                  <Badge tone={key.is_active ? "ok" : "neutral"}>
                    {key.is_active ? key.status.toUpperCase() : "INACTIVE"}
                  </Badge>
                  <button
                    className="btn btn-ghost"
                    disabled={patchKey.isPending}
                    onClick={() =>
                      patchKey.mutate(
                        { keyId: key.id, patch: { is_active: !key.is_active } },
                        {
                          onSuccess: () =>
                            pushToast({
                              type: "success",
                              title: key.is_active
                                ? "Key disabled"
                                : "Key enabled",
                              message: `Key #${key.id} updated.`,
                            }),
                          onError: (e) =>
                            pushToast({
                              type: "error",
                              title: "Failed",
                              message:
                                e instanceof Error ? e.message : "Unknown",
                            }),
                        },
                      )
                    }
                  >
                    {key.is_active ? "Disable" : "Enable"}
                  </button>
                  <button
                    className="btn btn-ghost"
                    disabled={deleteKey.isPending}
                    onClick={() =>
                      deleteKey.mutate(key.id, {
                        onSuccess: () =>
                          pushToast({
                            type: "success",
                            title: "Key deleted",
                            message: `Key #${key.id} deleted.`,
                          }),
                        onError: (e) =>
                          pushToast({
                            type: "error",
                            title: "Failed",
                            message: e instanceof Error ? e.message : "Unknown",
                          }),
                      })
                    }
                  >
                    Delete
                  </button>
                </span>
              </div>
            ))}
            {(keys.data?.keys ?? []).length === 0 ? (
              <span className="hint">No credentials yet.</span>
            ) : null}
          </div>
        </div>
      </QueryGate>
    </Drawer>
  );
}

export function ProvidersPage() {
  const providers = useProvidersQuery();
  const deleteProv = useDeleteProviderMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [selected, setSelected] = useState<ProviderRecordDTO | null>(null);
  const [editingProvider, setEditingProvider] = useState<
    ProviderRecordDTO | "new" | null
  >(null);

  const rows = providers.data?.providers ?? [];

  return (
    <div className="page-col">
      <PageHeader
        title="Providers"
        description="Stored credential pools and key lifecycle management."
        actions={
          <button
            className="btn btn-primary"
            disabled={providers.data?.read_only}
            onClick={() => setEditingProvider("new")}
          >
            Add provider
          </button>
        }
      />

      <QueryGate isLoading={providers.isLoading} error={providers.error}>
        <div className="card">
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Base URL</th>
                  <th className="num">Active keys</th>
                  <th>Status</th>
                  <th>Storage</th>
                  <th className="num">Actions</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((p) => (
                  <tr key={p.id}>
                    <td>{p.name}</td>
                    <td className="mono dim" style={{ fontSize: 12 }}>
                      {p.base_url}
                    </td>
                    <td className="num">{p.active_keys}</td>
                    <td>
                      <Badge tone={p.is_active ? "ok" : "neutral"}>
                        {p.is_active ? "ACTIVE" : "INACTIVE"}
                      </Badge>
                    </td>
                    <td>
                      <Badge tone={providers.data?.read_only ? "warn" : "info"}>
                        {providers.data?.read_only
                          ? "READ-ONLY"
                          : (providers.data?.storage ?? "file").toUpperCase()}
                      </Badge>
                    </td>
                    <td className="num">
                      <div
                        style={{
                          display: "flex",
                          gap: 6,
                          justifyContent: "flex-end",
                        }}
                      >
                        <button
                          className="btn btn-secondary"
                          onClick={() => setSelected(p)}
                        >
                          Keys
                        </button>
                        <button
                          className="btn btn-ghost"
                          disabled={providers.data?.read_only}
                          onClick={() => setEditingProvider(p)}
                        >
                          Edit
                        </button>
                        <button
                          className="btn btn-ghost"
                          disabled={
                            providers.data?.read_only || deleteProv.isPending
                          }
                          onClick={() =>
                            deleteProv.mutate(p.id, {
                              onSuccess: () =>
                                pushToast({
                                  type: "success",
                                  title: "Provider deleted",
                                  message: `${p.name} deleted.`,
                                }),
                              onError: (e) =>
                                pushToast({
                                  type: "error",
                                  title: "Failed",
                                  message:
                                    e instanceof Error ? e.message : "Unknown",
                                }),
                            })
                          }
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {rows.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="faint">
                      No stored providers yet.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
        {providers.data?.read_only ? (
          <p
            className="hint"
            style={{ marginTop: 10, fontSize: 12, color: "var(--faint)" }}
          >
            Catalog is running in file-config mode: credentials are declared in
            upstreams.json and the environment.
          </p>
        ) : null}
      </QueryGate>

      <KeysDrawer provider={selected} onClose={() => setSelected(null)} />
      {editingProvider ? (
        <ProviderForm
          provider={editingProvider}
          onClose={() => setEditingProvider(null)}
        />
      ) : null}
    </div>
  );
}
