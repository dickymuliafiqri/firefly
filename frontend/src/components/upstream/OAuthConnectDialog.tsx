import { useState, useEffect, useRef, useCallback } from 'react';
import { ExternalLink, Loader2, Check, AlertCircle, X } from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import type { ConnectionDTO } from '@/services/schema';
import { canonicalProtocol } from '@/services/schema';
import { initiateOAuthAuthorize, pollOAuthStatus } from '@/services/api';

export interface OAuthConnectDialogProps {
  open: boolean;
  onClose: () => void;
  provider: string; // 'antigravity' | 'cline' | 'codebuddy-cn' | 'codebuddy-intl'
  onSuccess: (connection: ConnectionDTO) => void;
}

function getProviderTitle(p: string): string {
  switch (canonicalProtocol(p)) {
    case 'antigravity':
      return 'Google Cloud Code';
    case 'cline':
      return 'Cline';
    case 'codebuddy-cn':
      return 'CodeBuddy (CN)';
    case 'codebuddy-intl':
      return 'CodeBuddy (Intl)';
    default:
      return p;
  }
}

function getProviderHint(p: string): string {
  switch (canonicalProtocol(p)) {
    case 'antigravity':
      return 'Authorize with your Google account to access Cloud Code inference.';
    case 'cline':
      return 'Connect your Cline account for proxy-based inference routing.';
    case 'codebuddy-cn':
      return 'Authorize via Tencent Cloud CodeBuddy (China region).';
    case 'codebuddy-intl':
      return 'Authorize via CodeBuddy International.';
    default:
      return 'Authorize gateway access to bind this account to your upstream pool.';
  }
}

export const canonicalOAuthProvider = canonicalProtocol;

type Phase = 'idle' | 'starting' | 'waiting' | 'success' | 'error';

export function OAuthConnectDialog({
  open,
  onClose,
  provider,
  onSuccess,
}: OAuthConnectDialogProps) {
  const [phase, setPhase] = useState<Phase>('idle');
  const [authUrl, setAuthUrl] = useState<string | null>(null);
  const [sessionState, setSessionState] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [connection, setConnection] = useState<ConnectionDTO | null>(null);

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelledRef = useRef(false);

  const clearPoll = useCallback(() => {
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
  }, []);

  const startAuthFlow = useCallback(async () => {
    setPhase('starting');
    setErrorMessage(null);
    setAuthUrl(null);
    setSessionState(null);
    setConnection(null);
    cancelledRef.current = false;

    try {
      const res = await initiateOAuthAuthorize({ provider: canonicalOAuthProvider(provider) });
      setAuthUrl(res.auth_url);
      setSessionState(res.state);
      setPhase('waiting');

      if (res.auth_url) {
        window.open(res.auth_url, '_blank', 'noopener,noreferrer,width=520,height=680');
      }
    } catch (err: unknown) {
      setPhase('error');
      setErrorMessage(err instanceof Error ? err.message : 'Failed to initiate authorization');
    }
  }, [provider]);

  // Polling loop
  useEffect(() => {
    if (phase !== 'waiting' || !sessionState) {
      clearPoll();
      return;
    }

    const poll = async () => {
      if (cancelledRef.current) return;
      try {
        const res = await pollOAuthStatus({ state: sessionState });
        if (!res.pending && res.connection) {
          setPhase('success');
          setConnection(res.connection);
          onSuccess(res.connection);
          return;
        }
        if (res.status === 'error') {
          setPhase('error');
          setErrorMessage('Authorization was denied or expired.');
          return;
        }
      } catch {
        // Continue polling on transient errors
      }

      if (!cancelledRef.current) {
        pollTimerRef.current = setTimeout(poll, 2000);
      }
    };

    pollTimerRef.current = setTimeout(poll, 2000);
    return clearPoll;
  }, [phase, sessionState, clearPoll, onSuccess]);

  // Listen for postMessage from OAuth callback HTML page
  useEffect(() => {
    if (!open) return;

    const handler = (e: MessageEvent) => {
      if (e.data?.type === 'oauth_complete') {
        clearPoll();
        if (sessionState) {
          void (async () => {
            try {
              const res = await pollOAuthStatus({ state: sessionState });
              if (!res.pending && res.connection) {
                setPhase('success');
                setConnection(res.connection);
                onSuccess(res.connection);
              }
            } catch {
              // Fall through
            }
          })();
        }
      }
    };

    window.addEventListener('message', handler);
    return () => window.removeEventListener('message', handler);
  }, [open, sessionState, clearPoll, onSuccess]);

  // Auto-start when dialog opens; cleanup when closes
  useEffect(() => {
    if (open) {
      startAuthFlow();
    } else {
      cancelledRef.current = true;
      clearPoll();
      setPhase('idle');
      setConnection(null);
      setAuthUrl(null);
      setSessionState(null);
      setErrorMessage(null);
    }
  }, [open, startAuthFlow, clearPoll]);

  const handleClose = () => {
    cancelledRef.current = true;
    clearPoll();
    onClose();
  };

  if (!open) return null;

  return (
    <div className="modal-backdrop" onClick={handleClose} role="presentation">
      <div
        className="modal"
        style={{ maxWidth: 480 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={`Connect ${getProviderTitle(provider)}`}
      >
        {/* Header */}
        <div className="modal-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <h2>Connect {getProviderTitle(provider)}</h2>
            {phase === 'waiting' && <Badge tone="info">Authorizing</Badge>}
            {phase === 'success' && <Badge tone="ok">Connected</Badge>}
            {phase === 'error' && <Badge tone="danger">Failed</Badge>}
          </div>
          <button
            type="button"
            className="icon-btn"
            onClick={handleClose}
            aria-label="Close dialog"
          >
            <X style={{ width: 16, height: 16 }} />
          </button>
        </div>

        {/* Body */}
        <div
          className="modal-body"
          style={{ fontFamily: '"JetBrains Mono", monospace', fontSize: 12 }}
        >
          <p
            style={{
              color: 'var(--muted)',
              marginBottom: 16,
              fontFamily: 'Inter, system-ui, sans-serif',
              fontSize: 13,
            }}
          >
            {getProviderHint(provider)}
          </p>

          {/* Starting phase */}
          {phase === 'starting' && (
            <div
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: 10,
                padding: '28px 0',
              }}
            >
              <Loader2
                style={{
                  width: 22,
                  height: 22,
                  color: 'var(--faint)',
                  animation: 'spin 1s linear infinite',
                }}
              />
              <span style={{ color: 'var(--faint)', fontSize: 12 }}>
                Initiating authorization session...
              </span>
            </div>
          )}

          {/* Waiting phase */}
          {phase === 'waiting' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <div
                style={{
                  padding: '12px 14px',
                  borderRadius: 6,
                  border: '1px solid var(--line)',
                  background: 'var(--surface-raised)',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 8,
                }}
              >
                <span style={{ color: 'var(--faint)', fontSize: 11 }}>
                  A browser tab was opened. If your popup blocker dismissed it, click
                  below:
                </span>
                {authUrl && (
                  <a
                    href={authUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 6,
                      fontSize: 12,
                      color: 'var(--biolum)',
                      fontWeight: 500,
                      wordBreak: 'break-all',
                    }}
                  >
                    <ExternalLink
                      style={{ width: 13, height: 13, flexShrink: 0 }}
                    />
                    Continue login on provider
                  </a>
                )}
              </div>

              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  color: 'var(--muted)',
                  fontSize: 11,
                  paddingTop: 4,
                }}
              >
                <span
                  style={{
                    width: 8,
                    height: 8,
                    borderRadius: '50%',
                    background: 'var(--biolum)',
                    display: 'inline-block',
                    animation: 'oauth-pulse 2s ease-in-out infinite',
                    flexShrink: 0,
                  }}
                />
                <span>Waiting for authorization to complete in browser...</span>
              </div>
            </div>
          )}
          {/* Success phase */}
          {phase === 'success' && connection && (
            <div
              style={{
                padding: '12px 14px',
                borderRadius: 6,
                border: '1px solid rgba(16, 185, 129, 0.25)',
                background: 'rgba(16, 185, 129, 0.06)',
                display: 'flex',
                alignItems: 'flex-start',
                gap: 10,
              }}
            >
              <Check
                style={{
                  width: 16,
                  height: 16,
                  color: 'var(--ok)',
                  marginTop: 1,
                  flexShrink: 0,
                }}
              />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <span style={{ color: '#6ee7b7', fontWeight: 600, fontSize: 12 }}>
                  Account connected
                </span>
                <span style={{ color: 'var(--ink)', fontSize: 11 }}>
                  {connection.email || connection.id}
                </span>
                {connection.provider_specific_data?.project_id && (
                  <span style={{ color: 'var(--faint)', fontSize: 10 }}>
                    Project: {connection.provider_specific_data.project_id}
                  </span>
                )}
                <span
                  style={{
                    color: 'var(--faint)',
                    fontSize: 10,
                    fontFamily: '"JetBrains Mono", monospace',
                  }}
                >
                  Key ref: oauth:{connection.id}
                </span>
              </div>
            </div>
          )}

          {/* Error phase */}
          {phase === 'error' && (
            <div
              style={{
                padding: '12px 14px',
                borderRadius: 6,
                border: '1px solid rgba(244, 63, 94, 0.25)',
                background: 'rgba(244, 63, 94, 0.06)',
                display: 'flex',
                alignItems: 'flex-start',
                gap: 10,
              }}
            >
              <AlertCircle
                style={{
                  width: 16,
                  height: 16,
                  color: 'var(--danger)',
                  marginTop: 1,
                  flexShrink: 0,
                }}
              />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <span style={{ color: '#fda4af', fontWeight: 600, fontSize: 12 }}>
                  Authorization failed
                </span>
                <span style={{ color: 'var(--muted)', fontSize: 11 }}>
                  {errorMessage ||
                    'An unexpected error occurred during the OAuth flow.'}
                </span>
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="modal-footer" style={{ gap: 8 }}>
          {phase === 'error' && (
            <button type="button" className="btn btn-primary" onClick={startAuthFlow}>
              Retry
            </button>
          )}
          <button type="button" className="btn btn-secondary" onClick={handleClose}>
            {phase === 'success' ? 'Done' : 'Cancel'}
          </button>
        </div>
      </div>

      <style>{`
        @keyframes oauth-pulse {
          0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(190, 242, 100, 0.4); }
          50% { opacity: 0.6; box-shadow: 0 0 0 6px rgba(190, 242, 100, 0); }
        }
        @keyframes spin {
          from { transform: rotate(0deg); }
          to { transform: rotate(360deg); }
        }
      `}</style>
    </div>
  );
}

/** Protocols that authenticate via OAuth connection instead of manual API keys. */
export const OAUTH_PROTOCOLS = new Set([
  'antigravity',
  'antigravity-go',
  'cline',
  'codebuddy',
  'codebuddy-cn',
  'codebuddy-intl',
  'codebuddy_cn',
  'codebuddy_intl',
]);

export function isOAuthProtocol(protocol: string): boolean {
  return OAUTH_PROTOCOLS.has(protocol) || OAUTH_PROTOCOLS.has(canonicalProtocol(protocol));
}