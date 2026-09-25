import { useState, useEffect, useRef, useCallback } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import type { ConnectionDTO } from '@/services/schema';
import { useOAuthAuthorizeMutation, useOAuthPollMutation } from '@/services/api';
import { ExternalLink, Loader2, Check, AlertCircle } from 'lucide-react';

export interface OAuthConnectDialogProps {
  isOpen: boolean;
  onClose: () => void;
  provider: string; // 'antigravity' | 'cline' | 'codebuddy_cn' | 'codebuddy_intl'
  onSuccess: (connection: ConnectionDTO) => void;
}

export function OAuthConnectDialog({
  isOpen,
  onClose,
  provider,
  onSuccess,
}: OAuthConnectDialogProps) {
  const authorizeMutation = useOAuthAuthorizeMutation();
  const pollMutation = useOAuthPollMutation();

  const [authUrl, setAuthUrl] = useState<string | null>(null);
  const [sessionState, setSessionState] = useState<string | null>(null);
  const [status, setStatus] = useState<'idle' | 'starting' | 'waiting' | 'success' | 'error'>('idle');
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [connectedAccount, setConnectedAccount] = useState<ConnectionDTO | null>(null);

  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isCancelledRef = useRef(false);

  const getProviderTitle = (p: string) => {
    switch (p) {
      case 'antigravity':
        return 'Google Cloud / Antigravity';
      case 'cline':
        return 'Cline Account';
      case 'codebuddy_cn':
        return 'Tencent Cloud CodeBuddy (CN)';
      case 'codebuddy_intl':
        return 'CodeBuddy International';
      default:
        return p;
    }
  };

  const startAuthFlow = useCallback(async () => {
    setStatus('starting');
    setErrorMessage(null);
    setAuthUrl(null);
    setSessionState(null);
    isCancelledRef.current = false;

    try {
      const res = await authorizeMutation.mutateAsync({ provider });
      setAuthUrl(res.auth_url);
      setSessionState(res.state);
      setStatus('waiting');

      // Automatically open auth URL in new tab
      if (res.auth_url) {
        window.open(res.auth_url, '_blank', 'noopener,noreferrer');
      }
    } catch (err: unknown) {
      setStatus('error');
      setErrorMessage(err instanceof Error ? err.message : 'Failed to initiate authorization');
    }
  }, [provider, authorizeMutation]);

  // Polling loop
  useEffect(() => {
    if (status !== 'waiting' || !sessionState) {
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
        pollTimerRef.current = null;
      }
      return;
    }

    const poll = async () => {
      if (isCancelledRef.current) return;
      try {
        const res = await pollMutation.mutateAsync({ state: sessionState });
        if (!res.pending && res.connection) {
          setStatus('success');
          setConnectedAccount(res.connection);
          onSuccess(res.connection);
          return;
        }
      } catch (err) {
        // Continue polling unless permanent error
      }

      if (!isCancelledRef.current) {
        pollTimerRef.current = setTimeout(poll, 2000);
      }
    };

    pollTimerRef.current = setTimeout(poll, 2000);

    return () => {
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
      }
    };
  }, [status, sessionState, pollMutation, onSuccess]);

  // Trigger start when dialog opens
  useEffect(() => {
    if (isOpen) {
      startAuthFlow();
    } else {
      isCancelledRef.current = true;
      if (pollTimerRef.current) {
        clearTimeout(pollTimerRef.current);
      }
      setStatus('idle');
      setConnectedAccount(null);
      setAuthUrl(null);
    }
  }, [isOpen]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleClose = () => {
    isCancelledRef.current = true;
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current);
    }
    onClose();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={handleClose}
      title={`Connect ${getProviderTitle(provider)}`}
      description="Authorize gateway access to bind this account to your upstream pool."
      size="sm"
    >
      <div className="flex flex-col gap-4 font-mono text-xs text-neutral-300 py-2">
        {status === 'starting' && (
          <div className="flex flex-col items-center justify-center py-6 gap-2">
            <Loader2 className="w-5 h-5 text-neutral-400 animate-spin" />
            <span className="text-neutral-400">Initiating OAuth authorization session...</span>
          </div>
        )}

        {status === 'waiting' && (
          <div className="flex flex-col gap-3 py-2">
            <div className="p-3 rounded-lg border border-white/[0.06] bg-white/[0.02] flex flex-col gap-2">
              <span className="text-neutral-400 text-[11px]">
                A browser tab was opened. If it was blocked, click below:
              </span>
              {authUrl && (
                <a
                  href={authUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1.5 text-xs text-emerald-400 hover:text-emerald-300 hover:underline font-medium break-all"
                >
                  <ExternalLink className="w-3.5 h-3.5 shrink-0" />
                  Continue login on provider
                </a>
              )}
            </div>

            <div className="flex items-center gap-2 text-neutral-400 text-[11px] pt-1">
              <Loader2 className="w-3.5 h-3.5 animate-spin text-neutral-500 shrink-0" />
              <span>Waiting for authorization completion in browser...</span>
            </div>
          </div>
        )}

        {status === 'success' && connectedAccount && (
          <div className="p-3 rounded-lg border border-emerald-500/20 bg-emerald-500/5 flex items-start gap-2.5">
            <Check className="w-4 h-4 text-emerald-400 mt-0.5 shrink-0" />
            <div className="flex flex-col">
              <span className="text-emerald-300 font-medium">Account Connected Successfully</span>
              <span className="text-neutral-300 text-[11px] mt-0.5">
                {connectedAccount.email || connectedAccount.id}
              </span>
              {connectedAccount.provider_specific_data?.project_id && (
                <span className="text-neutral-400 text-[10px]">
                  Project: {connectedAccount.provider_specific_data.project_id}
                </span>
              )}
            </div>
          </div>
        )}

        {status === 'error' && (
          <div className="p-3 rounded-lg border border-rose-500/20 bg-rose-500/5 flex items-start gap-2.5">
            <AlertCircle className="w-4 h-4 text-rose-400 mt-0.5 shrink-0" />
            <div className="flex flex-col">
              <span className="text-rose-300 font-medium">Authorization Failed</span>
              <span className="text-neutral-400 text-[11px] mt-0.5">{errorMessage || 'An error occurred'}</span>
            </div>
          </div>
        )}

        <div className="flex items-center justify-end gap-2 pt-3 border-t border-white/[0.04]">
          {status === 'error' && (
            <Button variant="minimal" size="sm" onClick={startAuthFlow}>
              Retry
            </Button>
          )}
          <Button variant="minimal" size="sm" onClick={handleClose}>
            {status === 'success' ? 'Done' : 'Cancel'}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
