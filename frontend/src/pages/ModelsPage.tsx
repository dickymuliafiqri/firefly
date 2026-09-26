import { useEffect, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Segmented } from '@/components/ui/Controls';
import { Drawer } from '@/components/ui/Drawer';
import { PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useSettingsQuery, useSaveSettingsSmart, withSettings } from '@/services/api';
import type { ModelDTO, ComboDTO } from '@/services/schema';
import { useUiStore } from '@/state/store';

interface ModelFormProps {
  editing: ModelDTO | null;
  onClose: () => void;
}

function ModelForm({ editing, onClose }: ModelFormProps) {
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);

  const [name, setName] = useState('');
  const [upstream, setUpstream] = useState('');
  const [upstreamModel, setUpstreamModel] = useState('');
  const [fallbacks, setFallbacks] = useState('');
  const [maxContext, setMaxContext] = useState('');
  const [caps, setCaps] = useState({
    stream: true,
    tools: false,
    vision: false,
    json_mode: false,
    embeddings: false,
    audio: false,
  });

  useEffect(() => {
    if (editing) {
      setName(editing.public_name);
      setUpstream(editing.upstream);
      setUpstreamModel(editing.upstream_model);
      setFallbacks((editing.fallback_upstreams ?? []).join(', '));
      setMaxContext(editing.max_context ? String(editing.max_context) : '');
      setCaps({
        stream: editing.capabilities?.stream ?? true,
        tools: editing.capabilities?.tools ?? false,
        vision: editing.capabilities?.vision ?? false,
        json_mode: editing.capabilities?.json_mode ?? false,
        embeddings: editing.capabilities?.embeddings ?? false,
        audio: editing.capabilities?.audio ?? false,
      });
    } else {
      setName('');
      setUpstream(settings.data?.upstreams[0]?.name ?? '');
      setUpstreamModel('');
      setFallbacks('');
      setMaxContext('');
      setCaps({ stream: true, tools: false, vision: false, json_mode: false, embeddings: false, audio: false });
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

  function submit() {
    if (!settings.data || !name.trim() || !upstream.trim() || !upstreamModel.trim()) return;
    const entry: ModelDTO = {
      public_name: name.trim(),
      upstream: upstream.trim(),
      upstream_model: upstreamModel.trim(),
      enabled: editing?.enabled ?? true,
      capabilities: caps,
      ...(maxContext.trim() ? { max_context: Number(maxContext) } : {}),
      ...(fallbacks.trim()
        ? { fallback_upstreams: fallbacks.split(',').map((f) => f.trim()).filter(Boolean) }
        : {}),
    };
    const others = settings.data.models.filter((m) => m.public_name !== entry.public_name);
    const next = withSettings(settings.data, { models: [...others, entry] });
    save.mutate(next, {
      onSuccess: (d) =>
        pushToast({
          type: 'success',
          title: editing ? 'Model updated' : 'Model added',
          message: d.local ? 'Demo mode: changes are local to browser only.' : 'Catalog synchronized.',
        }),
      onError: (e) =>
        pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
    });
    onClose();
  }

  return (
    <Drawer open onClose={onClose} title={editing ? `Edit model: ${editing.public_name}` : 'Add model'}>
      <div className="form-grid">
        <Field label="Public name" htmlFor="m-name">
          <input id="m-name" value={name} placeholder="gpt-4o" onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Upstream" htmlFor="m-upstream">
          <select id="m-upstream" value={upstream} onChange={(e) => setUpstream(e.target.value)}>
            {(settings.data?.upstreams ?? []).map((u) => (
              <option key={u.name}>{u.name}</option>
            ))}
          </select>
        </Field>
      </div>
      <Field label="Upstream model" htmlFor="m-um" hint="Private model name on provider side.">
        <input id="m-um" value={upstreamModel} className="mono" placeholder="gpt-4o-2024-11-20" onChange={(e) => setUpstreamModel(e.target.value)} />
      </Field>
      <Field label="Fallback upstreams (optional)" htmlFor="m-fb" hint="Comma-separated; used when primary upstream breaker is open.">
        <input id="m-fb" value={fallbacks} placeholder="openai-main, antigravity-prod" onChange={(e) => setFallbacks(e.target.value)} />
      </Field>
      <Field label="Max context tokens (optional)" htmlFor="m-ctx">
        <input id="m-ctx" type="number" value={maxContext} placeholder="128000" onChange={(e) => setMaxContext(e.target.value)} />
      </Field>
      <div>
        <label style={{ fontSize: 13, fontWeight: 500, color: 'var(--muted)', display: 'block', marginBottom: 6 }}>
          Capabilities
        </label>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 8, fontSize: 13 }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.stream} onChange={(e) => setCaps((c) => ({ ...c, stream: e.target.checked }))} />
            Stream
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.tools} onChange={(e) => setCaps((c) => ({ ...c, tools: e.target.checked }))} />
            Tools
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.vision} onChange={(e) => setCaps((c) => ({ ...c, vision: e.target.checked }))} />
            Vision
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.json_mode} onChange={(e) => setCaps((c) => ({ ...c, json_mode: e.target.checked }))} />
            JSON Mode
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.embeddings} onChange={(e) => setCaps((c) => ({ ...c, embeddings: e.target.checked }))} />
            Embeddings
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
            <input type="checkbox" checked={caps.audio} onChange={(e) => setCaps((c) => ({ ...c, audio: e.target.checked }))} />
            Audio
          </label>
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={!name.trim() || !upstream.trim() || !upstreamModel.trim() || save.isPending} onClick={submit}>
          {editing ? 'Save changes' : 'Add model'}
        </button>
      </div>
    </Drawer>
  );
}

function Field(props: { label: string; htmlFor: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <label htmlFor={props.htmlFor}>{props.label}</label>
      {props.children}
      {props.hint ? <span className="hint">{props.hint}</span> : null}
    </div>
  );
}

function ComboForm({ editing, onClose }: { editing: ComboDTO | null; onClose: () => void }) {
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);

  const [name, setName] = useState(editing?.name ?? '');
  const [strategy, setStrategy] = useState(editing?.strategy ?? 'round_robin');
  const [members, setMembers] = useState((editing?.models ?? []).join(', '));
  const [enabled, setEnabled] = useState(editing?.enabled !== false);

  function submit() {
    if (!settings.data || !name.trim() || !members.trim()) return;
    const entry: ComboDTO = {
      name: name.trim(),
      strategy,
      models: members.split(',').map((m) => m.trim()).filter(Boolean),
      enabled,
    };
    const others = (settings.data.combos ?? []).filter((c) => c.name !== entry.name);
    const next = withSettings(settings.data, { combos: [...others, entry] });
    save.mutate(next, {
      onSuccess: (d) => {
        pushToast({
          type: 'success',
          title: editing ? 'Combo updated' : 'Combo created',
          message: d.local ? 'Demo mode: changes are local to browser only.' : 'Catalog synchronized.',
        });
        onClose();
      },
      onError: (e) =>
        pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
    });
  }

  return (
    <Drawer open onClose={onClose} title={editing ? `Edit combo: ${editing.name}` : 'Add combo'}>
      <Field label="Combo name" htmlFor="c-name">
        <input id="c-name" value={name} placeholder="smart-combo" onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Strategy" htmlFor="c-strat">
        <select id="c-strat" value={strategy} onChange={(e) => setStrategy(e.target.value)}>
          <option value="round_robin">round_robin</option>
          <option value="least_inflight">least_inflight</option>
          <option value="failover">failover</option>
        </select>
      </Field>
      <Field label="Member models" htmlFor="c-members" hint="Public model names, comma-separated, in priority order.">
        <input id="c-members" value={members} placeholder="gpt-4o, claude-sonnet-4-5" onChange={(e) => setMembers(e.target.value)} />
      </Field>
      <Field label="Enabled" htmlFor="c-en">
        <select id="c-en" value={enabled ? 'yes' : 'no'} onChange={(e) => setEnabled(e.target.value === 'yes')}>
          <option value="yes">Yes</option>
          <option value="no">No</option>
        </select>
      </Field>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={!name.trim() || !members.trim() || save.isPending} onClick={submit}>
          {editing ? 'Save changes' : 'Add combo'}
        </button>
      </div>
    </Drawer>
  );
}

export function ModelsPage() {
  const [panel, setPanel] = useState('direct');
  const [query, setQuery] = useState('');
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<ModelDTO | null>(null);
  const [comboFormOpen, setComboFormOpen] = useState(false);
  const [editingCombo, setEditingCombo] = useState<ComboDTO | null>(null);

  const allModels = settings.data?.models ?? [];
  const allCombos = settings.data?.combos ?? [];

  const q = query.trim().toLowerCase();
  const models = q
    ? allModels.filter((m) => m.public_name.toLowerCase().includes(q) || m.upstream.toLowerCase().includes(q))
    : allModels;
  const combos = q
    ? allCombos.filter((c) => c.name.toLowerCase().includes(q) || c.models.some((m) => m.toLowerCase().includes(q)))
    : allCombos;

  function mutateModels(fn: (list: ModelDTO[]) => ModelDTO[], okMsg: string) {
    if (!settings.data) return;
    const next = withSettings(settings.data, { models: fn(settings.data.models) });
    save.mutate(next, {
      onSuccess: (d) =>
        pushToast({
          type: 'success',
          title: okMsg,
          message: d.local ? 'Demo mode: changes are local to browser only.' : 'Catalog synchronized.',
        }),
      onError: (e) =>
        pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
    });
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Models"
        description="Direct model routing catalog and virtual combo fallback chains."
        actions={
          panel === 'direct' ? (
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

      <div className="filter-bar" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
        <Segmented
          items={[
            { id: 'direct', label: 'Direct Models' },
            { id: 'combos', label: 'Virtual Combos' },
          ]}
          value={panel}
          onChange={setPanel}
          ariaLabel="Model panel"
        />
        <input
          type="search"
          placeholder="Search models…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ marginLeft: 'auto', width: 220 }}
        />
      </div>

      <QueryGate isLoading={settings.isLoading} error={settings.error}>
        {panel === 'direct' ? (
          <div className="card">
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Public name</th>
                    <th>Upstream</th>
                    <th>Upstream model</th>
                    <th className="num">Fallbacks</th>
                    <th>Status</th>
                    <th className="num">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {models.map((m) => (
                    <tr key={m.public_name}>
                      <td>{m.public_name}</td>
                      <td className="dim">{m.upstream}</td>
                      <td className="mono dim">{m.upstream_model}</td>
                      <td className="num">{m.fallback_upstreams?.length ?? 0}</td>
                      <td>
                        <Badge tone={m.enabled === false ? 'neutral' : 'ok'}>
                          {m.enabled === false ? 'DISABLED' : 'ACTIVE'}
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
                        </button>{' '}
                        <button
                          className="btn btn-ghost"
                          onClick={() =>
                            mutateModels(
                              (list) => list.map((x) => (x.public_name === m.public_name ? { ...x, enabled: x.enabled === false } : x)),
                              m.enabled === false ? 'Model enabled' : 'Model disabled',
                            )
                          }
                        >
                          {m.enabled === false ? 'Enable' : 'Disable'}
                        </button>{' '}
                        <button className="btn btn-ghost" onClick={() => mutateModels((list) => list.filter((x) => x.public_name !== m.public_name), 'Model deleted')}>
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                  {models.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="faint">Model catalog is empty.</td>
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
                    {combo.name}{' '}
                    <Badge tone="info" className="ml-2">{(combo.strategy ?? 'round_robin').toUpperCase()}</Badge>
                    {combo.enabled === false ? <Badge tone="neutral" className="ml-2">DISABLED</Badge> : null}
                  </h2>
                  <div style={{ display: 'flex', gap: 6 }}>
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
                        const nextCombos = (settings.data.combos ?? []).filter((c) => c.name !== combo.name);
                        save.mutate(withSettings(settings.data, { combos: nextCombos }), {
                          onSuccess: () => pushToast({ type: 'success', title: 'Combo deleted', message: `${combo.name} deleted.` }),
                          onError: (e) => pushToast({ type: 'error', title: 'Failed', message: e instanceof Error ? e.message : 'Unknown' }),
                        });
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
                        <span className="step">{String(i + 1).padStart(2, '0')}</span>
                        {name}
                        {models.find((m) => m.public_name === name) ? (
                          <>
                            {' '}
                            <span className="arrow">&rarr;</span> {models.find((m) => m.public_name === name)!.upstream}
                          </>
                        ) : null}
                        <span className="weight">
                          {combo.strategy === 'failover' ? `priority ${i + 1}` : 'equal'}
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
                  <span className="faint" style={{ fontSize: 13 }}>No virtual combos yet.</span>
                </div>
              </div>
            ) : null}
          </div>
        )}
      </QueryGate>

      {formOpen ? <ModelForm editing={editing} onClose={() => setFormOpen(false)} /> : null}
      {comboFormOpen ? <ComboForm editing={editingCombo} onClose={() => setComboFormOpen(false)} /> : null}
    </div>
  );
}
