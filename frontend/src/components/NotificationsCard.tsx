import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { SwitchRow } from '@/components/ui/Controls';
import { useTestNotificationMutation } from '@/services/api';
import type { NotificationChannelDTO } from '@/services/schema';
import { useSettingsView, useDraftStore } from '@/state/draftStore';
import { useUiStore } from '@/state/store';

/**
 * Webhook channel management. Credentials are write-only: the API hands back
 * "***", so an untouched field must be sent back as the mask rather than
 * blanked, otherwise saving settings from this card would wipe every bearer
 * and signing secret.
 */
export function NotificationsCard() {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const channels = settings.data?.notifications?.channels ?? [];

  const [drafting, setDrafting] = useState(false);
  const [editingUrl, setEditingUrl] = useState<string | null>(null);
  const [form, setForm] = useState({
    url: '',
    format: 'generic',
    bearer: '',
    secret: '',
    enabled: true,
    events: '',
    min_severity: '',
  });
  const [formError, setFormError] = useState<string | null>(null);

  const test = useTestNotificationMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  // The mutation owns the request; the card owns the reporting, so a failure
  // names its reason instead of a bare status code.
  const runTest = (ch: NotificationChannelDTO) => {
    test.mutate(ch, {
      onSuccess: (res) => {
        if (res.success) {
          pushToast({ type: 'success', title: 'Test delivered', message: res.message ?? 'ok' });
        } else {
          pushToast({ type: 'error', title: 'Test failed', message: res.error ?? 'unknown error' });
        }
      },
      onError: (err: unknown) =>
        pushToast({
          type: 'error',
          title: 'Test failed',
          message: err instanceof Error ? err.message : String(err),
        }),
    });
  };

  const commit = (next: NotificationChannelDTO[]) => {
    if (!settings.data) return;
    stage({ notifications: { channels: next } });
  };

  const openNew = () => {
    setEditingUrl(null);
    setForm({ url: '', format: 'generic', bearer: '', secret: '', enabled: true, events: '', min_severity: '' });
    setFormError(null);
    setDrafting(true);
  };

  const openEdit = (ch: NotificationChannelDTO) => {
    setEditingUrl(ch.url);
    setForm({
      url: ch.url,
      format: ch.format,
      bearer: ch.bearer ?? '',
      secret: ch.secret ?? '',
      enabled: ch.enabled,
      events: (ch.events ?? []).join(', '),
      min_severity: ch.min_severity ?? '',
    });
    setFormError(null);
    setDrafting(true);
  };

  const submit = () => {
    const url = form.url.trim();
    if (url === '') {
      setFormError('A webhook URL is required.');
      return;
    }
    const events = form.events
      .split(',')
      .map((e) => e.trim())
      .filter((e) => e !== '');
    const entry: NotificationChannelDTO = {
      url,
      format: form.format,
      enabled: form.enabled,
      ...(form.bearer.trim() !== '' ? { bearer: form.bearer.trim() } : {}),
      ...(form.secret.trim() !== '' ? { secret: form.secret.trim() } : {}),
      ...(events.length > 0 ? { events } : {}),
      ...(form.min_severity !== '' ? { min_severity: form.min_severity } : {}),
    };
    const next =
      editingUrl === null
        ? [...channels, entry]
        : channels.map((c) => (c.url === editingUrl ? entry : c));
    commit(next);
    setDrafting(false);
  };

  const remove = (url: string) => {
    if (!window.confirm(`Delete the notification channel for ${url}?`)) return;
    commit(channels.filter((c) => c.url !== url));
  };

  return (
    <div className="card">
      <div className="card-header">
        <h2>Notifications</h2>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <Badge tone={channels.length > 0 ? 'ok' : 'neutral'}>
            {channels.length} CHANNEL{channels.length === 1 ? '' : 'S'}
          </Badge>
          <Button leftIcon={<Plus size={14} />} onClick={openNew}>
            Add channel
          </Button>
        </div>
      </div>
      <div className="card-body tight table-wrap">
        <table>
          <thead>
            <tr>
              <th>URL</th>
              <th>Format</th>
              <th>Events</th>
              <th>Min severity</th>
              <th>Status</th>
              <th className="num">Actions</th>
            </tr>
          </thead>
          <tbody>
            {channels.map((ch) => (
              <tr key={ch.url}>
                <td className="mono" style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {ch.url}
                </td>
                <td>
                  <Badge tone="info">{ch.format}</Badge>
                </td>
                <td className="dim">{ch.events?.length ? ch.events.join(', ') : 'all'}</td>
                <td className="dim">{ch.min_severity || 'info'}</td>
                <td>
                  <Badge tone={ch.enabled ? 'ok' : 'neutral'}>{ch.enabled ? 'ENABLED' : 'DISABLED'}</Badge>
                </td>
                <td className="num">
                  <Button variant="ghost" onClick={() => runTest(ch)} disabled={test.isPending}>
                    Test
                  </Button>{' '}
                  <Button variant="ghost" onClick={() => openEdit(ch)}>
                    Edit
                  </Button>{' '}
                  <Button
                    variant="ghost"
                    aria-label={`Delete channel ${ch.url}`}
                    onClick={() => remove(ch.url)}
                  >
                    <Trash2 size={13} />
                  </Button>
                </td>
              </tr>
            ))}
            {channels.length === 0 ? (
              <tr>
                <td colSpan={6} className="faint">
                  No notification channels. Key cooldowns, breaker transitions, and tenant budget
                  warnings are only logged until one is configured.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      <Drawer
        open={drafting}
        onClose={() => setDrafting(false)}
        title={editingUrl ? 'Edit channel' : 'Add channel'}
      >
        <div className="stack">
          <label className="field">
            <span>Webhook URL</span>
            <input
              value={form.url}
              onChange={(e) => setForm({ ...form, url: e.target.value })}
              placeholder="https://discord.com/api/webhooks/…"
              disabled={editingUrl !== null}
            />
          </label>
          <label className="field">
            <span>Format</span>
            <select
              value={form.format}
              onChange={(e) => setForm({ ...form, format: e.target.value })}
            >
              <option value="generic">generic (raw JSON)</option>
              <option value="discord">discord (embed)</option>
              <option value="slack">slack (blocks)</option>
              <option value="telegram">telegram (sendMessage)</option>
            </select>
          </label>
          <label className="field">
            <span>Bearer token (optional)</span>
            <input
              value={form.bearer}
              onChange={(e) => setForm({ ...form, bearer: e.target.value })}
              placeholder="leave as *** to keep the stored value"
            />
            <em>Sent as Authorization: Bearer. Write-only — never returned by the API.</em>
          </label>
          <label className="field">
            <span>Signing secret (optional)</span>
            <input
              value={form.secret}
              onChange={(e) => setForm({ ...form, secret: e.target.value })}
              placeholder="leave as *** to keep the stored value"
            />
            <em>Enables the X-Firefly-Signature HMAC header. Write-only.</em>
          </label>
          <label className="field">
            <span>Event filter (optional)</span>
            <input
              value={form.events}
              onChange={(e) => setForm({ ...form, events: e.target.value })}
              placeholder="key.cooldown, breaker.open"
            />
            <em>Comma-separated event types. Blank receives every event.</em>
          </label>
          <label className="field">
            <span>Minimum severity</span>
            <select
              value={form.min_severity}
              onChange={(e) => setForm({ ...form, min_severity: e.target.value })}
            >
              <option value="">info (everything)</option>
              <option value="warning">warning</option>
              <option value="critical">critical</option>
            </select>
          </label>
          <SwitchRow
            title="Enabled"
            description="Disable to keep the configuration without receiving events."
            checked={form.enabled}
            onChange={(v) => setForm({ ...form, enabled: v })}
            ariaLabel="Enable channel"
          />
          {formError ? <p className="form-error">{formError}</p> : null}
          <div className="row-actions end">
            <Button variant="ghost" onClick={() => setDrafting(false)}>
              Cancel
                       </Button>
            <Button onClick={submit}>Save channel</Button>
          </div>
        </div>
      </Drawer>
    </div>
  );
}
