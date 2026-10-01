import { useEffect, useState } from 'react';
import { AlertTriangle, Loader2, Save, Trash2 } from 'lucide-react';
import { useSettingsQuery } from '@/services/api';
import type { SettingsDTO } from '@/services/schema';
import { useDraftStore } from '@/state/draftStore';
import { useUiStore } from '@/state/store';

/**
 * Global floating bar for the staged-configuration flow: appears on every
 * admin page as soon as a draft exists, offers Commit/Discard, and owns the
 * blocking overlay + conflict modal around the actual save. The UI is only
 * ever blocked while the commit request itself is in flight.
 */

interface CollectionSummary {
  label: string;
  before: number;
  after: number;
}

function summarizeChanges(base: SettingsDTO | null, draft: SettingsDTO): CollectionSummary[] {
  const collections = [
    { label: 'upstreams', key: 'upstreams' as const },
    { label: 'models', key: 'models' as const },
    { label: 'combos', key: 'combos' as const },
    { label: 'tenants', key: 'tenants' as const },
  ];
  const out: CollectionSummary[] = [];
  for (const { label, key } of collections) {
    const before = base?.[key]?.length ?? 0;
    const after = draft[key]?.length ?? 0;
    if (before !== after) out.push({ label, before, after });
  }
  return out;
}

export function PendingChangesBar() {
  const draft = useDraftStore((s) => s.draft);
  const base = useDraftStore((s) => s.base);
  const status = useDraftStore((s) => s.status);
  const error = useDraftStore((s) => s.error);
  const commit = useDraftStore((s) => s.commit);
  const discard = useDraftStore((s) => s.discard);
  const resolveConflict = useDraftStore((s) => s.resolveConflict);
  const syncServer = useDraftStore((s) => s.syncServer);
  const pushToast = useUiStore((s) => s.pushToast);
  const [conflictOpen, setConflictOpen] = useState(false);

  // Keeps the drift-check baseline fresh: the settings query polls app-wide,
  // so `base` tracks the live server config between commits.
  const settings = useSettingsQuery();
  useEffect(() => {
    if (settings.data) syncServer(settings.data);
  }, [settings.data, syncServer]);

  // A conflict always needs an explicit decision — surface the modal.
  useEffect(() => {
    setConflictOpen(status === 'conflict');
  }, [status]);

  // Warn before closing/reloading the tab with uncommitted work.
  const hasDraft = draft != null;
  useEffect(() => {
    if (!hasDraft) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [hasDraft]);

  if (!draft) return null;

  const blocked = status === 'saving';
  const summary = summarizeChanges(base, draft);

  async function handleCommit() {
    const result = await commit();
    if (result === 'saved') {
      pushToast({
        type: 'success',
        title: 'Configuration committed',
        message: 'Gateway catalog synchronized.',
      });
    } else if (result === 'local') {
      pushToast({
        type: 'info',
        title: 'Demo mode',
        message: 'Gateway unreachable — changes applied to the local browser cache only.',
      });
    }
  }

  function handleDiscard() {
    if (!window.confirm('Discard all pending configuration changes? This cannot be undone.')) return;
    discard();
    pushToast({ type: 'info', title: 'Changes discarded', message: 'The staged configuration draft was removed.' });
  }

  async function handleOverwrite() {
    await resolveConflict('overwrite');
    if (useDraftStore.getState().draft == null) {
      pushToast({
        type: 'success',
        title: 'Configuration committed',
        message: 'Server configuration was overwritten with the staged draft.',
      });
    }
  }

  async function handleKeepServer() {
    await resolveConflict('discard');
    pushToast({ type: 'info', title: 'Server version kept', message: 'The staged draft was discarded.' });
  }

  return (
    <>
      {blocked ? (
        <div
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(2, 6, 23, 0.55)',
            backdropFilter: 'blur(2px)',
            zIndex: 90,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
          role="status"
          aria-busy="true"
          aria-label="Committing configuration"
        >
          <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '18px 24px' }}>
            <Loader2 className="animate-spin" style={{ width: 20, height: 20, color: 'var(--biolum, var(--ok))' }} />
            <div>
              <div style={{ fontSize: 14, fontWeight: 600 }}>Committing configuration…</div>
              <div style={{ fontSize: 12, color: 'var(--muted)' }}>Saving the staged catalog to the gateway.</div>
            </div>
          </div>
        </div>
      ) : null}

      <div
        className="card"
        role="region"
        aria-label="Pending configuration changes"
        style={{
          position: 'fixed',
          top: 'calc(var(--topbar-h, 56px) + 12px)',
          left: '50%',
          transform: 'translateX(-50%)',
          zIndex: 45,
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '10px 14px',
          maxWidth: 'calc(100vw - 32px)',
          boxShadow: '0 8px 24px rgba(0, 0, 0, 0.35)',
        }}
      >
        {status === 'error' ? (
          <AlertTriangle style={{ width: 16, height: 16, color: 'var(--danger)', flexShrink: 0 }} />
        ) : (
          <span
            aria-hidden="true"
            style={{
              width: 8,
              height: 8,
              borderRadius: '50%',
              background: 'var(--warn, #f59e0b)',
              flexShrink: 0,
            }}
          />
        )}
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap' }}>
            {status === 'error'
              ? 'Commit failed'
              : status === 'conflict'
                ? 'Configuration conflict'
                : status === 'saving'
                  ? 'Committing…'
                  : 'Pending changes'}
          </div>
          <div
            style={{
              fontSize: 11,
              color: status === 'error' ? 'var(--danger)' : 'var(--muted)',
              maxWidth: 380,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
            title={status === 'error' ? (error ?? '') : undefined}
          >
            {status === 'error'
              ? (error ?? 'Unknown error — retry or discard.')
              : summary.length > 0
                ? summary.map((s) => `${s.label} ${s.before}→${s.after}`).join(' · ')
                : 'Configuration edited — not committed yet.'}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, marginLeft: 'auto', flexShrink: 0 }}>
          {status === 'conflict' ? (
            <button type="button" className="btn btn-primary" onClick={() => setConflictOpen(true)}>
              Review
            </button>
          ) : (
            <button
              type="button"
              className="btn btn-primary"
              disabled={blocked}
              onClick={() => void handleCommit()}
            >
              <Save style={{ width: 14, height: 14 }} />
              Commit
            </button>
          )}
          <button type="button" className="btn btn-ghost" disabled={blocked} onClick={handleDiscard}>
            <Trash2 style={{ width: 14, height: 14 }} />
            Discard
          </button>
        </div>
      </div>

      {status === 'conflict' && conflictOpen ? (
        <div
          className="modal-backdrop"
          onClick={() => setConflictOpen(false)}
          role="presentation"
        >
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
            aria-label="Configuration conflict"
          >
            <div className="modal-header">
              <h2>Server configuration changed</h2>
            </div>
            <div className="modal-body">
              <p style={{ margin: '0 0 10px', fontSize: 13 }}>
                The gateway configuration was updated since you started editing — for example by the
                key harvester, another operator, or another tab. Committing your draft now would
                overwrite those changes.
              </p>
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, color: 'var(--muted)' }}>
                <li>
                  <strong>Overwrite server</strong> — replaces the server configuration with your
                  staged draft, including everything it does not have.
                </li>
                <li>
                  <strong>Discard my changes</strong> — keeps the current server configuration and
                  drops the staged draft.
                </li>
                <li>
                  <strong>Keep editing</strong> — close this dialog and decide later; nothing is
                  saved until you commit.
                </li>
              </ul>
            </div>
            <div className="modal-footer">
              <button type="button" className="btn btn-secondary" onClick={() => setConflictOpen(false)}>
                Keep editing
              </button>
              <button
                type="button"
                className="btn btn-ghost"
                style={{ color: 'var(--danger)' }}
                onClick={() => void handleKeepServer()}
              >
                Discard my changes
              </button>
              <button type="button" className="btn btn-primary" onClick={() => void handleOverwrite()}>
                Overwrite server
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </>
  );
}
