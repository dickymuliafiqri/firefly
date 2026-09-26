import { useMemo, useState, useEffect, type FormEvent } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Field, Segmented, SwitchRow } from '@/components/ui/Controls';
import { useAuthStore } from '@/state/auth';
import { useUiStore } from '@/state/store';
import { navigate } from '@/lib/router';
import {
  useSettingsQuery,
  useWarpStatusQuery,
  useTunnelStatusQuery,
  useToggleTunnelMutation,


  useSaveSettingsMutation,
  useRotateWarpMutation,
  logoutApi,
  verifyAuthApi,
  updatePasswordApi,
  useTestTursoMutation,
  ApiError,
} from '@/services/api';
import { handleSessionInvalid } from '@/lib/session';

interface DiffRow {
  kind: 'same' | 'add' | 'del';
  text: string;
}

function buildDiff(a: string, b: string): DiffRow[] {
  const linesA = a.split('\n');
  const linesB = b.split('\n');
  const max = Math.max(linesA.length, linesB.length);
  const out: DiffRow[] = [];

  for (let i = 0; i < max; i++) {
    const la = linesA[i];
    const lb = linesB[i];
    if (la === lb) {
      if (la !== undefined) out.push({ kind: 'same', text: la });
    } else {
      if (la !== undefined) out.push({ kind: 'del', text: la });
      if (lb !== undefined) out.push({ kind: 'add', text: lb });
    }
  }
  return out;
}

function AccessCard() {
  const token = useAuthStore((s) => s.token);
  const clearToken = useAuthStore((s) => s.clearToken);
  const pushToast = useUiStore((s) => s.pushToast);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);

  useEffect(() => {
    if (!token) {
      setAuthenticated(false);
      return;
    }
    let alive = true;
    verifyAuthApi(token)
      .then((d) => {
        if (!alive) return;
        setAuthenticated(d.authenticated);
        if (!d.authenticated) handleSessionInvalid();
      })
      .catch((err) => {
        if (!alive) return;
        // Only 401 means session rejected. Network drops are not SESSION INVALID.
        if (err instanceof ApiError && err.status === 401) {
          setAuthenticated(false);
          handleSessionInvalid();
          return;
        }
        setAuthenticated(null);
      });
    return () => {
      alive = false;
    };
  }, [token]);

  const statusTone = authenticated === true ? 'ok' : authenticated === false && token ? 'warn' : 'neutral';
  const statusText =
    token === '' ? 'NO SESSION' : authenticated === null ? 'CHECKING' : authenticated ? 'AUTHENTICATED' : 'SESSION INVALID';

  return (
    <div className="card">
      <div className="card-header">
        <h2>Access</h2>
        <Badge tone={statusTone}>{statusText}</Badge>
      </div>
      <div className="card-body">
        <div className="form-row form-row-end">
          <span className="hint">
            {authenticated === true
              ? 'Dashboard session active in this browser.'
              : authenticated === null && token
                ? 'Checking dashboard session…'
                : 'No active session. Log in to manage the gateway.'}
          </span>
          {authenticated ? (
            <button
              className="btn btn-secondary"
              onClick={async () => {
                try {
                  await logoutApi(token);
                } finally {
                  clearToken();
                  setAuthenticated(false);
                  pushToast({ type: 'success', title: 'Logout', message: 'Dashboard session revoked.' });
                  navigate('login');
                }
              }}
            >
              Logout
            </button>
          ) : (
            <button className="btn btn-primary" onClick={() => navigate('login')}>
              Sign in
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function PasswordCard() {
  const token = useAuthStore((s) => s.token);
  const pushToast = useUiStore((s) => s.pushToast);
  const [currentPass, setCurrentPass] = useState('');
  const [newPass, setNewPass] = useState('');
  const [saving, setSaving] = useState(false);

  async function handleUpdate(e: FormEvent) {
    e.preventDefault();
    if (!currentPass || !newPass || saving) return;
    setSaving(true);
    try {
      await updatePasswordApi(currentPass, newPass, token);
      setCurrentPass('');
      setNewPass('');
      pushToast({ type: 'success', title: 'Password updated', message: 'Master password successfully changed.' });
    } catch (err) {
      // 401 dead session is handled by handleSessionInvalid (redirects to login).
      // 401 "incorrect current password" remains displayed here.
      if (err instanceof ApiError && err.status === 401 && !/incorrect current password/i.test(err.message)) {
        return;
      }
      pushToast({
        type: 'error',
        title: 'Failed to change password',
        message: err instanceof Error ? err.message : 'Unknown error',
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Password</h2>
        <span className="hint">Change dashboard master password</span>
      </div>
      <div className="card-body">
        <form onSubmit={handleUpdate} className="stack" style={{ marginTop: 0 }}>
          <div className="form-grid">
            <Field label="Current password" htmlFor="curr-pass">
              <input
                id="curr-pass"
                type="password"
                value={currentPass}
                placeholder="••••••••"
                onChange={(e) => setCurrentPass(e.target.value)}
                required
              />
            </Field>
            <Field label="New password" htmlFor="new-pass">
              <input
                id="new-pass"
                type="password"
                value={newPass}
                placeholder="••••••••"
                onChange={(e) => setNewPass(e.target.value)}
                required
              />
            </Field>
          </div>
          <div className="form-row form-row-end">
            <span className="hint">Changes are saved directly to the backend.</span>
            <button type="submit" className="btn btn-primary" disabled={!currentPass || !newPass || saving || !token}>
              {saving ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function TokenSaverCard() {
  const settings = useSettingsQuery();
  const save = useSaveSettingsMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const ts = settings.data?.token_saver;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Token Saver</h2>
        <Badge tone={ts?.enabled ? 'ok' : 'neutral'}>{ts?.enabled ? 'ENABLED' : 'DISABLED'}</Badge>
      </div>
      <div className="card-body">
        <SwitchRow
          title="Token Saver — master switch"
          description="Token optimization suite: tool output compression, concise responses, minimal code, and context pruning."
          checked={ts?.enabled ?? false}
          onChange={(v) =>
            settings.data &&
            save.mutate(
              { ...settings.data, token_saver: { ...ts!, enabled: v } },
              {
                onSuccess: () =>
                  pushToast({ type: 'success', title: 'Token Saver', message: `Master switch ${v ? 'enabled' : 'disabled'}.` }),
                onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
              },
            )
          }
          ariaLabel="Enable Token Saver"
        />
        <SwitchRow
          title="RTK — tool output compression"
          description="ANSI stripping, log dedup, git diff compact, head/tail truncation."
          checked={ts?.compress_tool_output ?? false}
          onChange={(v) =>
            settings.data &&
            save.mutate(
              { ...settings.data, token_saver: { ...ts!, compress_tool_output: v } },
              {
                onSuccess: () => pushToast({ type: 'success', title: 'Token Saver', message: 'RTK synchronized.' }),
                onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
              },
            )
          }
          ariaLabel="Enable RTK"
        />
        <SwitchRow
          title="Caveman — brevity prompt injection"
          description="Forces terse assistant responses to save output tokens."
          checked={ts?.terse_output ?? false}
          onChange={(v) =>
            settings.data &&
            save.mutate(
              { ...settings.data, token_saver: { ...ts!, terse_output: v } },
              {
                onSuccess: () => pushToast({ type: 'success', title: 'Token Saver', message: 'Caveman synchronized.' }),
                onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
              },
            )
          }
          ariaLabel="Enable Caveman"
        />
        <SwitchRow
          title="Ponytail — minimal code bias"
          description="Biases responses toward minimal, patch-style code output."
          checked={ts?.minimal_code ?? false}
          onChange={(v) =>
            settings.data &&
            save.mutate(
              { ...settings.data, token_saver: { ...ts!, minimal_code: v } },
              {
                onSuccess: () => pushToast({ type: 'success', title: 'Token Saver', message: 'Ponytail synchronized.' }),
                onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
              },
            )
          }
          ariaLabel="Enable Ponytail"
        />
        <SwitchRow
          title="Headroom — middle context pruning"
          description="Prunes middle conversation turns beyond the token budget."
          checked={ts?.compress_context ?? false}
          onChange={(v) =>
            settings.data &&
            save.mutate(
              { ...settings.data, token_saver: { ...ts!, compress_context: v } },
              {
                onSuccess: () => pushToast({ type: 'success', title: 'Token Saver', message: 'Headroom synchronized.' }),
                onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
              },
            )
          }
          ariaLabel="Enable Headroom"
        />
      </div>
    </div>
  );
}

function WarpCard() {
  const warp = useWarpStatusQuery();
  const rotate = useRotateWarpMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const d = warp.data;
  const live = Boolean(d?.enabled);

  // Three states, not two: a warm-up that could not reach Cloudflare is neither a
  // live tunnel nor a healthy idle engine, so it is reported as UNAVAILABLE with
  // the recorded reason instead of the neutral DISABLED badge.
  const badgeTone = live ? 'ok' : d?.error ? 'warn' : 'neutral';
  const badgeText = live ? 'ROTATING' : d?.error ? 'UNAVAILABLE' : 'DISABLED';

  const copyIp = (ip: string) => {
    navigator.clipboard.writeText(ip);
    pushToast({ type: 'success', title: 'IP Copied', message: ip });
  };

  return (
    <div className="card">
      <div className="card-header">
        <h2>Warp Engine</h2>
        <Badge tone={badgeTone} title={d?.error || undefined}>{badgeText}</Badge>
      </div>
      <div className="card-body">
        <div className="kv-list">
          <div>
            <span className="k">Public IP (egress)</span>
            <span className="v">
              {d?.public_ip ? (
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ fontFamily: 'var(--font-mono, monospace)', fontWeight: 500 }}>{d.public_ip}</span>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    style={{ padding: '2px 8px', fontSize: '0.75rem', height: 'auto' }}
                    onClick={() => copyIp(d.public_ip!)}
                  >
                    Copy
                  </button>
                </div>
              ) : (
                '—'
              )}
            </span>
          </div>
          <div><span className="k">Internal IP (tunnel)</span><span className="v">{d?.internal_ip || '—'}</span></div>
          <div><span className="k">Colo</span><span className="v">{d?.colo || '—'}</span></div>
          <div><span className="k">Latency</span><span className="v">{d?.latency_ms ? `${d.latency_ms.toFixed(1)}ms` : '—'}</span></div>
          <div><span className="k">Active connections</span><span className="v">{d?.active_connections ?? '—'}</span></div>
          <div><span className="k">Auto-rotate interval</span><span className="v">{d?.auto_rotate_interval_seconds ? `${Math.round(d.auto_rotate_interval_seconds / 60)}m` : 'disabled'}</span></div>
          <div>
            <span className="k">Next rotation</span>
            <span className="v">{d?.next_rotation_at ? new Date(d.next_rotation_at).toLocaleTimeString() : '—'}</span>
          </div>
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 14 }}>
          <button
            className="btn btn-secondary"
            disabled={rotate.isPending}
            onClick={() =>
              rotate.mutate(undefined, {
                onSuccess: () =>
                  pushToast({
                    type: 'success',
                    title: 'WARP rotate',
                    message: live
                      ? 'New egress session created; old session is draining.'
                      : 'WARP tunnel established on the first available egress.',
                  }),
                onError: (e) => pushToast({ type: 'error', title: 'Rotate failed', message: e instanceof Error ? e.message : 'Unknown' }),
              })
            }
          >
            {live ? 'Rotate now' : 'Start tunnel'}
          </button>
        </div>

        {d?.error && (
          <div
            style={{
              marginTop: 12,
              padding: '10px 14px',
              borderRadius: 'var(--radius, 6px)',
              background: 'rgba(239, 68, 68, 0.1)',
              border: '1px solid rgba(239, 68, 68, 0.3)',
              color: 'var(--color-error, #ef4444)',
              fontSize: '0.85rem',
              display: 'flex',
              flexDirection: 'column',
              gap: 4,
            }}
          >
            <strong>WARP error:</strong>
            <span style={{ wordBreak: 'break-word', fontFamily: 'var(--font-mono, monospace)', fontSize: '0.8rem' }}>{d.error}</span>
          </div>
        )}
      </div>
    </div>
  );
}

function TunnelCard() {
  const tunnel = useTunnelStatusQuery();
  const toggle = useToggleTunnelMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const d = tunnel.data;

  const [mode, setMode] = useState<'quick' | 'named'>('quick');
  const [token, setToken] = useState('');
  const [showConfig, setShowConfig] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  useEffect(() => {
    if (d?.mode === 'named') {
      setMode('named');
    }
  }, [d?.mode]);

  const isOnline = d?.running && !!d?.public_url;
  const isStarting = d?.running && !d?.public_url;
  const isDownloading = !!d?.downloading;
  const statusTone = isOnline ? 'ok' : (isDownloading || isStarting) ? 'warn' : 'neutral';
  const statusLabel = isOnline
    ? 'ONLINE'
    : isDownloading
    ? 'DOWNLOADING'
    : isStarting
    ? 'STARTING'
    : d?.running
    ? 'RUNNING'
    : 'STOPPED';

  const copyUrl = () => {
    if (!d?.public_url) return;
    navigator.clipboard.writeText(d.public_url);
    pushToast({ type: 'success', title: 'URL Copied', message: d.public_url });
  };

  const handleToggle = (enabled: boolean) => {
    setErrorMessage(null);
    toggle.mutate(
      { enabled, mode, token: mode === 'named' ? token : undefined },
      {
        onSuccess: (res) => {
          setErrorMessage(null);
          if (res.running) {
            pushToast({
              type: 'success',
              title: 'Tunnel Enabled',
              message: res.mode === 'quick' ? 'Starting quick tunnel (trycloudflare.com)...' : 'Starting named tunnel...',
            });
          } else {
            pushToast({
              type: 'info',
              title: 'Tunnel Disabled',
              message: 'Tunnel ingress successfully disabled.',
            });
          }
        },
        onError: (err) => {
          const msg = err instanceof Error ? err.message : 'Unknown error';
          setErrorMessage(msg);
          pushToast({
            type: 'error',
            title: 'Failed to Change Tunnel Status',
            message: msg,
          });
        },
      }
    );
  };

  return (
    <div className="card">
      <div className="card-header">
        <div>
          <h2>Cloudflare Tunnel</h2>
          <span className="hint">Native Ingress Engine (remote public access)</span>
        </div>
        <Badge tone={statusTone}>{statusLabel}</Badge>
      </div>
      <div className="card-body">
        <SwitchRow
          title="Enable Cloudflare Tunnel"
          description="Enable instant public access via Cloudflare Tunnel for remote AI coding agents without port forwarding."
          checked={d?.running ?? false}
          onChange={handleToggle}
          ariaLabel="Toggle Cloudflare Tunnel"
        />

        <div className="kv-list" style={{ marginTop: 14 }}>
          <div>
            <span className="k">Operation mode</span>
            <span className="v">{d?.mode ? d.mode.toUpperCase() : 'DISABLED'}</span>
          </div>
          <div>
            <span className="k">Process status</span>
            <span className="v">{d?.downloading ? 'Downloading binary…' : d?.running ? (isStarting ? 'Starting…' : 'Running') : 'Stopped'}</span>
          </div>
          <div>
            <span className="k">Target local URL</span>
            <span className="v">{d?.local_url || '—'}</span>
          </div>
          <div>
            <span className="k">Public tunnel URL</span>
            <span className="v">
              {d?.public_url ? (
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <a
                    href={d.public_url}
                    target="_blank"
                    rel="noreferrer"
                    style={{ color: 'var(--color-primary, #3b82f6)', textDecoration: 'underline', wordBreak: 'break-all' }}
                  >
                    {d.public_url}
                  </a>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    style={{ padding: '2px 8px', fontSize: '0.75rem', height: 'auto' }}
                    onClick={copyUrl}
                  >
                    Copy
                  </button>
                </div>
              ) : isStarting ? (
                <span style={{ color: 'var(--color-text-muted, #888)' }}>Generating public URL…</span>
              ) : (
                '—'
              )}
            </span>
          </div>
        </div>

        {d?.downloading && (
          <div
            style={{
              marginTop: 12,
              padding: '10px 14px',
              borderRadius: 'var(--radius, 6px)',
              background: 'rgba(59, 130, 246, 0.08)',
              border: '1px solid rgba(59, 130, 246, 0.25)',
              color: 'var(--color-primary, #3b82f6)',
              fontSize: '0.85rem',
            }}
          >
            Automatically downloading official <code>cloudflared</code> binary to the Firefly directory... Please wait a moment.
          </div>
        )}

        {/* Configuration drawer / collapse */}
        <div style={{ marginTop: 14, borderTop: '1px solid var(--border-subtle, rgba(255,255,255,0.06))', paddingTop: 10 }}>
          <button
            type="button"
            className="btn btn-ghost"
            style={{ fontSize: '0.8rem', padding: '3px 8px', height: 'auto', marginBottom: 6 }}
            onClick={() => setShowConfig(!showConfig)}
          >
            {showConfig ? 'Hide Mode & Token Settings' : 'Configure Mode & Token…'}
          </button>

          {showConfig && (
            <div className="form-grid" style={{ marginTop: 8 }}>
              <Field label="Tunnel Mode" htmlFor="tunnel-mode">
                <select
                  id="tunnel-mode"
                  value={mode}
                  disabled={d?.running}
                  onChange={(e) => setMode(e.target.value as 'quick' | 'named')}
                  style={{ width: '100%' }}
                >
                  <option value="quick">Quick Tunnel (Zero config trycloudflare.com)</option>
                  <option value="named">Named Tunnel (Production Cloudflare Token)</option>
                </select>
              </Field>

              {mode === 'named' && (
                <Field label="Cloudflare Tunnel Token" htmlFor="tunnel-token">
                  <input
                    id="tunnel-token"
                    type="password"
                    value={token}
                    placeholder="eyJh..."
                    disabled={d?.running}
                    onChange={(e) => setToken(e.target.value)}
                  />
                </Field>
              )}
            </div>
          )}
        </div>

        {errorMessage && (
          <div
            style={{
              marginTop: 12,
              padding: '10px 14px',
              borderRadius: 'var(--radius, 6px)',
              background: 'rgba(239, 68, 68, 0.1)',
              border: '1px solid rgba(239, 68, 68, 0.3)',
              color: 'var(--color-error, #ef4444)',
              fontSize: '0.85rem',
              display: 'flex',
              flexDirection: 'column',
              gap: 4,
            }}
          >
            <strong>Error:</strong>
            <span style={{ wordBreak: 'break-word', fontFamily: 'var(--font-mono, monospace)', fontSize: '0.8rem' }}>{errorMessage}</span>
            {errorMessage.toLowerCase().includes('cloudflared') && (
              <span style={{ color: 'var(--color-text-muted, #888)', fontSize: '0.8rem', marginTop: 4 }}>
                Ensure the <code>cloudflared</code> binary is installed on the host server and registered in the system PATH (e.g. download from Cloudflare, or via <code>winget install Cloudflare.cloudflared</code> / <code>brew install cloudflared</code>).
              </span>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function GatewayCard() {
  const settings = useSettingsQuery();
  const save = useSaveSettingsMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [panel, setPanel] = useState('gw-form');
  const [draft, setDraft] = useState<string | null>(null);
  const [diffOpen, setDiffOpen] = useState(false);

  const original = useMemo(() => JSON.stringify(settings.data, null, 2), [settings.data]);
  const text = draft ?? original;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Gateway configuration</h2>
        <Segmented
          items={[
            { id: 'gw-form', label: 'Overview' },
            { id: 'gw-raw', label: 'Raw JSON' },
          ]}
          value={panel}
          onChange={setPanel}
          ariaLabel="Gateway editor"
        />
      </div>

      {panel === 'gw-form' ? (
        <div className="card-body">
          <div className="kv-list">
            <div><span className="k">Upstreams</span><span className="v">{settings.data?.upstreams.length ?? '—'}</span></div>
            <div><span className="k">Models</span><span className="v">{settings.data?.models.length ?? '—'}</span></div>
            <div><span className="k">Combos</span><span className="v">{settings.data?.combos?.length ?? '—'}</span></div>
            <div><span className="k">Tenants</span><span className="v">{settings.data?.tenants.length ?? '—'}</span></div>
            <div>
              <span className="k">Authoritative lists</span>
              <span className="v">
                {[
                  settings.data?.manage_upstreams && 'upstreams',
                  settings.data?.manage_models && 'models',
                  settings.data?.manage_combos && 'combos',
                  settings.data?.manage_tenants && 'tenants',
                ]
                  .filter(Boolean)
                  .join(', ') || '—'}
              </span>
            </div>
            <div>
              <span className="k">Storage engine</span>
              <span className="v">{settings.data?.storage_engine || 'file'}</span>
            </div>
          </div>
        </div>
      ) : (
        <div className="card-body">
          <Field label="Declarative configuration" htmlFor="gw-raw-json">
            <textarea
              id="gw-raw-json"
              rows={14}
              spellCheck={false}
              value={text}
              onChange={(e) => setDraft(e.target.value)}
            />
          </Field>
          <div style={{ display: 'flex', gap: 8, marginTop: 14, justifyContent: 'flex-end' }}>
            <button
              className="btn btn-ghost"
              disabled={draft === null}
              onClick={() => setDraft(null)}
            >
              Reset
            </button>
            <button
              className="btn btn-ghost"
              disabled={draft === null}
              onClick={() => setDiffOpen(true)}
            >
              Diff
            </button>
            <button
              className="btn btn-primary"
              disabled={draft === null || save.isPending}
              onClick={() => {
                try {
                  const parsed = JSON.parse(text) as Parameters<typeof save.mutate>[0];
                  save.mutate(parsed, {
                    onSuccess: () => {
                      setDraft(null);
                      pushToast({ type: 'success', title: 'Settings saved', message: 'Configuration synchronized.' });
                    },
                    onError: (e) =>
                      pushToast({
                        type: 'error',
                        title: e instanceof Error && 'status' in e && (e as { status?: number }).status === 409 ? 'Conflict (409)' : 'Save failed',
                        message: e instanceof Error ? e.message : 'Unknown',
                      }),
                  });
                } catch {
                  pushToast({ type: 'error', title: 'Invalid JSON', message: 'Fix syntax errors before saving.' });
                }
              }}
            >
              Save
            </button>
          </div>
        </div>
      )}

      {diffOpen ? <DiffModal original={original} draft={text} onClose={() => setDiffOpen(false)} /> : null}
    </div>
  );
}

function DiffModal({ original, draft, onClose }: { original: string; draft: string; onClose: () => void }) {
  const rows = useMemo(() => buildDiff(original, draft), [original, draft]);
  const changed = rows.filter((r) => r.kind !== 'same').length;

  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Configuration diff"
      >
        <div className="modal-header">
          <h2>Configuration diff</h2>
          <Badge tone={changed === 0 ? 'ok' : 'warn'}>
            {changed === 0 ? 'IDENTICAL' : `${changed} CHANGED LINES`}
          </Badge>
        </div>
        <div className="modal-body">
          <pre className="diff-view">
            {rows.map((r, i) => (
              <div key={i} className={`diff-line ${r.kind}`}>
                <span className="diff-mark">{r.kind === 'add' ? '+' : r.kind === 'del' ? '-' : ' '}</span>
                <span>{r.text || ' '}</span>
              </div>
            ))}
          </pre>
        </div>
        <div className="modal-footer">
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}

function TursoCard() {
  const settings = useSettingsQuery();
  const save = useSaveSettingsMutation();
  const test = useTestTursoMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const turso = settings.data?.turso;
  const [url, setUrl] = useState(turso?.database_url ?? '');
  const [token, setToken] = useState(turso?.auth_token ?? '');

  useEffect(() => {
    if (turso) {
      setUrl(turso.database_url ?? '');
      setToken(turso.auth_token ?? '');
    }
  }, [turso]);

  async function handleTest() {
    try {
      const res = await test.mutateAsync({ database_url: url, auth_token: token });
      pushToast({
        type: res.ok ? 'success' : 'error',
        title: res.ok ? 'Connection successful' : 'Connection failed',
        message: res.message || (res.ok ? 'Turso database is accessible.' : 'Failed to connect to Turso.'),
      });
    } catch (err) {
      pushToast({
        type: 'error',
        title: 'Test failed',
        message: err instanceof Error ? err.message : 'Unknown error',
      });
    }
  }

  function handleSave() {
    if (!settings.data) return;
    save.mutate(
      { ...settings.data, turso: { ...turso, database_url: url, auth_token: token } },
      {
        onSuccess: () => pushToast({ type: 'success', title: 'Turso saved', message: 'Database configuration updated.' }),
        onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
      },
    );
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Turso Database</h2>
        <Badge tone={turso?.database_url ? 'ok' : 'neutral'}>
          {turso?.database_url ? 'CONFIGURED' : 'NOT SET'}
        </Badge>
      </div>
      <div className="card-body">
        <div className="form-grid">
          <Field label="Database URL" htmlFor="turso-url">
            <input
              id="turso-url"
              value={url}
              placeholder="libsql://..."
              onChange={(e) => setUrl(e.target.value)}
            />
          </Field>
          <Field label="Auth token" htmlFor="turso-token">
            <input
              id="turso-token"
              type="password"
              value={token}
              placeholder="••••••••"
              onChange={(e) => setToken(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-row form-row-end" style={{ marginTop: 14 }}>
          <span className="hint">Test connection before saving.</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <button
              type="button"
              className="btn btn-secondary"
              disabled={!url.trim() || test.isPending}
              onClick={() => void handleTest()}
            >
              {test.isPending ? 'Testing…' : 'Test'}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={!url.trim() || save.isPending}
              onClick={handleSave}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function AutoTLSCard() {
  const settings = useSettingsQuery();
  const save = useSaveSettingsMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const tls = settings.data?.auto_tls;
  const [domain, setDomain] = useState(tls?.domain ?? '');
  const [email, setEmail] = useState(tls?.email ?? '');
  const [enabled, setEnabled] = useState(tls?.enabled ?? false);

  useEffect(() => {
    if (tls) {
      setDomain(tls.domain ?? '');
      setEmail(tls.email ?? '');
      setEnabled(tls.enabled);
    }
  }, [tls]);

  function handleSave() {
    if (!settings.data) return;
    save.mutate(
      { ...settings.data, auto_tls: { enabled, domain, email } },
      {
        onSuccess: () => pushToast({ type: 'success', title: 'AutoTLS saved', message: 'Certificate configuration updated.' }),
        onError: (e) => pushToast({ type: 'error', title: 'Save failed', message: e instanceof Error ? e.message : 'Unknown' }),
      },
    );
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>AutoTLS</h2>
        <Badge tone={enabled ? 'ok' : 'neutral'}>{enabled ? 'ENABLED' : 'DISABLED'}</Badge>
      </div>
      <div className="card-body">
        <div className="form-grid">
          <Field label="Public domain" htmlFor="tls-domain">
            <input
              id="tls-domain"
              value={domain}
              placeholder="gateway.example.com"
              onChange={(e) => setDomain(e.target.value)}
            />
          </Field>
          <Field label="ACME email" htmlFor="tls-email">
            <input
              id="tls-email"
              type="email"
              value={email}
              placeholder="admin@example.com"
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-row form-row-end" style={{ marginTop: 14 }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            <span>Enable AutoTLS</span>
          </label>
          <button
            type="button"
            className="btn btn-primary"
            disabled={save.isPending}
            onClick={handleSave}
          >
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}

export function SettingsPage() {
  return (
    <div className="page-col">
      <div className="page-header">
        <div>
          <h1>Settings</h1>
          <p>Global gateway configuration, one domain per card.</p>
        </div>
      </div>

      <div className="stack">
        <AccessCard />
        <PasswordCard />
        <TokenSaverCard />
        <WarpCard />
        <TunnelCard />

        <TursoCard />
        <AutoTLSCard />
        <GatewayCard />
      </div>
    </div>
  );
}
