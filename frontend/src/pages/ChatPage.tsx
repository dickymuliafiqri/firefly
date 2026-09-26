import { useEffect, useRef, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import { PageHeader } from '@/components/ui/PageHeader';
import { streamChat, type ChatChunk } from '@/services/api';
import { useSettingsQuery } from '@/services/api';
import { useUiStore } from '@/state/store';

interface Turn {
  role: 'user' | 'assistant';
  content: string;
  model?: string;
}

const MAX_WATERFALL_BARS = 40;
const API_KEY_STORAGE = 'firefly-chat-api-key';

export function ChatPage() {
  const settings = useSettingsQuery();
  const pushToast = useUiStore((s) => s.pushToast);

  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [lastEvents, setLastEvents] = useState<string[]>([]);
  const [waterfall, setWaterfall] = useState<ChatChunk[]>([]);
  const [temp, setTemp] = useState('0.7');
  const [maxTok, setMaxTok] = useState('1024');
  const [apiKey, setApiKey] = useState(() => localStorage.getItem(API_KEY_STORAGE) ?? '');
  const abortRef = useRef<AbortController | null>(null);

  const modelOptions = (settings.data?.models ?? [])
    .filter((m) => m.enabled !== false)
    .map((m) => m.public_name);
  const [model, setModel] = useState('');
  const activeModel = model || modelOptions[0] || 'gpt-4o';

  // Auto-fill API key from the first tenant with a plaintext key, only if
  // user has not manually filled this field yet.
  useEffect(() => {
    if (apiKey || !settings.data) return;
    const first = settings.data.tenants.find((t) => t.api_key && !t.api_key.includes('•'));
    if (first?.api_key) setApiKey(first.api_key);
  }, [apiKey, settings.data]);

  useEffect(() => {
    localStorage.setItem(API_KEY_STORAGE, apiKey);
  }, [apiKey]);

  function pushEvent(raw: string) {
    setLastEvents((prev) => [...prev.slice(-2), raw]);
  }

  function pushChunk(c: ChatChunk) {
    setTurns((prev) => {
      const last = prev[prev.length - 1];
      if (!last || last.role !== 'assistant') return prev;
      return [...prev.slice(0, -1), { ...last, content: last.content + c.text }];
    });
    setWaterfall((prev) => [...prev.slice(-(MAX_WATERFALL_BARS - 1)), c]);
  }

  function send() {
    if (!input.trim() || streaming) return;

    if (!apiKey.trim()) {
      pushToast({
        type: 'error',
        title: 'Tenant API key required',
        message: 'Enter the Tenant API Key (sk-gw-…) so the gateway can authenticate the request.',
      });
      return;
    }

    const userTurn: Turn = { role: 'user', content: input.trim() };
    const nextTurns = [...turns, userTurn, { role: 'assistant' as const, content: '', model: activeModel }];
    setTurns(nextTurns);
    setInput('');
    setLastEvents([]);
    setWaterfall([]);
    setStreaming(true);

    const controller = new AbortController();
    abortRef.current = controller;

    const temperature = Number(temp);
    const maxTokens = parseInt(maxTok, 10);

    streamChat(
      activeModel,
      nextTurns.filter((t) => t.content).map((t) => ({ role: t.role, content: t.content })),
      pushChunk,
      pushEvent,
      controller.signal,
      {
        apiKey: apiKey.trim(),
        ...(Number.isFinite(temperature) ? { temperature } : {}),
        ...(Number.isFinite(maxTokens) && maxTokens > 0 ? { maxTokens } : {}),
      },
    )
      .catch((e) => {
        if ((e as Error).name !== 'AbortError') {
          pushToast({
            type: 'error',
            title: 'Chat failed',
            message: e instanceof Error ? e.message : 'Unknown error',
          });
        }
        // Remove empty assistant bubble if there was no output
        setTurns((prev) => prev.filter((t, i) => !(i === prev.length - 1 && t.role === 'assistant' && !t.content)));
      })
      .finally(() => {
        setStreaming(false);
        abortRef.current = null;
      });
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Chat"
        description="SSE chat tester with stream inspector and token waterfall."
      />

      <div className="chat-layout">
        <div className="card">
          <div className="card-header">
            <h2>Request</h2>
          </div>
          <div className="card-body">
            <div className="stack" style={{ marginTop: 0 }}>
              <Field
                label="Tenant API key"
                htmlFor="chat-key"
                hint="Tenant sk-gw-… key (Tenants tab). Saved in this browser only."
              >
                <input
                  id="chat-key"
                  type="password"
                  className="mono"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="sk-gw-…"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                />
              </Field>
              <Field label="Model" htmlFor="chat-model">
                <select id="chat-model" value={activeModel} onChange={(e) => setModel(e.target.value)}>
                  {modelOptions.length === 0 ? <option>gpt-4o</option> : null}
                  {modelOptions.map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
              </Field>
              <div className="form-grid">
                <Field label="Temperature" htmlFor="chat-temp">
                  <input id="chat-temp" value={temp} className="mono" onChange={(e) => setTemp(e.target.value)} />
                </Field>
                <Field label="Max tokens" htmlFor="chat-maxtok">
                  <input id="chat-maxtok" value={maxTok} className="mono" onChange={(e) => setMaxTok(e.target.value)} />
                </Field>
              </div>
              <Field label="Message" htmlFor="chat-msg">
                <textarea
                  id="chat-msg"
                  rows={5}
                  spellCheck={false}
                  placeholder="Tulis pesan…"
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send();
                  }}
                />
              </Field>
              {streaming ? (
                <button className="btn btn-secondary" style={{ justifyContent: 'center' }} onClick={() => abortRef.current?.abort()}>
                  Stop
                </button>
              ) : (
                <button className="btn btn-primary" style={{ justifyContent: 'center' }} disabled={!input.trim()} onClick={send}>
                  Send
                </button>
              )}
            </div>
          </div>
        </div>

        <div className="stack" style={{ marginTop: 0 }}>
          <div className="card chat-pane">
            <div className="card-header">
              <h2>Conversation</h2>
              <Badge tone={streaming ? 'ok' : 'neutral'}>{streaming ? 'STREAMING' : 'IDLE'}</Badge>
            </div>
            <div className="chat-log">
              {turns.length === 0 ? (
                <span className="faint" style={{ fontSize: 13 }}>
                  No conversation yet. Send a message to test the gateway.
                </span>
              ) : (
                turns.map((t, i) => (
                  <div key={i} className={`bubble ${t.role}`}>
                    <span className="who">{t.role === 'user' ? 'You' : t.model ?? 'assistant'}</span>
                    {t.content || (streaming && i === turns.length - 1 ? '…' : '')}
                  </div>
                ))
              )}
            </div>
          </div>

          <div className="card stream-inspector">
            <div className="card-header">
              <h2>Stream inspector</h2>
              <span className="mono faint">last 3 events</span>
            </div>
            <div className="card-body">
              {lastEvents.length === 0 ? (
                <span className="faint" style={{ fontSize: 12 }}>Waiting for SSE events…</span>
              ) : (
                <pre>
                  {lastEvents.map((e, i) => (
                    <span key={i}>
                      data: {e.length > 200 ? e.slice(0, 200) + '…' : e}
                      {'\n'}
                    </span>
                  ))}
                </pre>
              )}
            </div>
          </div>

          <div className="card">
            <div className="card-header">
              <h2>Token waterfall</h2>
              <span className="mono faint">inter-arrival</span>
            </div>
            <div className="card-body">
              {waterfall.length === 0 ? (
                <span className="faint" style={{ fontSize: 12 }}>No chunks yet.</span>
              ) : (
                <div className="waterfall" aria-hidden="true">
                  {waterfall.map((c, i) => {
                    const h = Math.min(100, Math.max(8, c.deltaMs / 40));
                    const hot = c.deltaMs > 120;
                    const dim = c.deltaMs < 20;
                    return (
                      <span
                        key={i}
                        style={{ height: `${h}%` }}
                        className={hot ? 'hot' : dim ? 'dim' : undefined}
                        title={`${Math.round(c.deltaMs)}ms`}
                      />
                    );
                  })}
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
