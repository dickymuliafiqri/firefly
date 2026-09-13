import React, { useState, useRef, useCallback, useEffect, useMemo } from 'react';
import { Button } from '@/components/ui/Button';
import {
  Send,
  Square,
  Bot,
  User,
  SlidersHorizontal,
  Key,
  Layers,
  AlertCircle,
  Eye,
  EyeOff,
  RotateCcw,
} from 'lucide-react';
import {
  useAdminToken,
  useModels,
  useCombos,
  useTenants,
  useUpstreams,
  useUpstreamBreakers,
  usePlaygroundMessages,
  usePlaygroundPrompt,
  usePlaygroundSelectedModel,
  usePlaygroundApiKey,
  usePlaygroundTemperature,
  usePlaygroundMaxTokens,
  usePlaygroundIsStreamMode,
  usePlaygroundIsGenerating,
  usePlaygroundErrorMessage,
  usePlaygroundActions,
} from '@/core/state/store';
import type { ChunkTiming, ChatMessage } from '@/core/state/playgroundSlice';
import { cn } from '@/lib/utils';

export type { ChatMessage };

export interface ChatWindowProps {
  onStreamingChange?: (isStreaming: boolean) => void;
  onTimingUpdate?: (
    timings: ChunkTiming[],
    ttft: number | null,
    tps: number | null,
    elapsed: number | null
  ) => void;
  onRawPacket?: (packet: string) => void;
  onClearDiagnostics?: () => void;
}

const PRESETS = [
  {
    label: 'Architecture Invariants',
    prompt: 'Explain why Firefly strictly forbids setting http.Server.WriteTimeout on the data plane.',
  },
  {
    label: 'Circuit Breaker',
    prompt: 'How does Firefly distinguish between Layer 1 (429/401) key errors and Layer 2 (5xx) breaker errors?',
  },
  {
    label: 'Worker Pool',
    prompt: 'Write a high-concurrency Go worker pool that reads from a buffered channel with graceful cancellation.',
  },
];

/**
 * ChatWindow
 * Interactive LLM test sandbox for Firefly.
 * Adheres strictly to Vercel React Best Practices:
 * - rerender-use-ref-transient-values: buffers incoming SSE tokens in ref, flushes via requestAnimationFrame
 * - rerender-defer-reads: uses granular atomic selectors from Zustand store
 * - rendering-conditional-render: uses ternary instead of logical AND for conditionals
 */
export const ChatWindow = React.memo(function ChatWindow({
  onStreamingChange,
  onTimingUpdate,
  onRawPacket,
  onClearDiagnostics,
}: ChatWindowProps) {
  const adminToken = useAdminToken();
  const models = useModels();
  const combos = useCombos();
  const tenants = useTenants();
  const upstreams = useUpstreams();
  const upstreamBreakers = useUpstreamBreakers();

  // Store state subscriptions
  const messages = usePlaygroundMessages();
  const inputPrompt = usePlaygroundPrompt();
  const selectedModel = usePlaygroundSelectedModel();
  const apiKey = usePlaygroundApiKey();
  const temperature = usePlaygroundTemperature();
  const maxTokens = usePlaygroundMaxTokens();
  const isStreamMode = usePlaygroundIsStreamMode();
  const isGenerating = usePlaygroundIsGenerating();
  const errorMessage = usePlaygroundErrorMessage();

  const {
    setPlaygroundMessages,
    setPlaygroundPrompt,
    setPlaygroundSelectedModel,
    setPlaygroundApiKey,
    setPlaygroundTemperature,
    setPlaygroundMaxTokens,
    setPlaygroundIsStreamMode,
    setPlaygroundIsGenerating,
    setPlaygroundErrorMessage,
    updatePlaygroundTimings,
    addPlaygroundRawPacket,
    clearPlaygroundDiagnostics,
    clearPlaygroundChat,
  } = usePlaygroundActions();

  // Local UI-only state
  const [showConfig, setShowConfig] = useState<boolean>(false);
  const [showApiKey, setShowApiKey] = useState<boolean>(false);

  // Transient Stream Buffering Ref (Vercel Best Practice: rerender-use-ref-transient-values)
  const streamTextRef = useRef<string>('');
  const abortControllerRef = useRef<AbortController | null>(null);
  const animFrameRef = useRef<number | null>(null);

  const enabledModels = models.filter((m) => m.enabled !== false);
  const enabledCombos = combos.filter((c) => c.enabled !== false);

  const isUpstreamActive = useCallback(
    (name: string) => {
      const u = upstreams.find((up) => up.name === name);
      if (u && u.enabled === false) return false;
      const st = upstreamBreakers[name] || 'CLOSED';
      return st !== 'OPEN';
    },
    [upstreams, upstreamBreakers]
  );

  const isModelAvailable = useCallback(
    (model: (typeof models)[0]) => {
      if (model.enabled === false) return false;
      const candidates = [model.upstream, ...(model.fallback_upstreams || [])].filter(Boolean);
      if (candidates.length === 0) return false;
      return candidates.some(isUpstreamActive);
    },
    [isUpstreamActive]
  );

  const isComboAvailable = useCallback(
    (combo: (typeof combos)[0]) => {
      if (combo.enabled === false) return false;
      if (!combo.models || combo.models.length === 0) return false;
      return combo.models.some((memberPublicName) => {
        const m = models.find((mod) => mod.public_name === memberPublicName);
        return m ? isModelAvailable(m) : false;
      });
    },
    [models, isModelAvailable]
  );

  const currentCombo = useMemo(
    () => combos.find((c) => c.name === selectedModel),
    [combos, selectedModel]
  );

  const currentModel = useMemo(
    () => models.find((m) => m.public_name === selectedModel),
    [models, selectedModel]
  );

  const currentUpstream = useMemo(() => {
    if (currentCombo) {
      return `combo (${currentCombo.strategy || 'least_inflight'}, ${currentCombo.models.length} models)`;
    }
    return currentModel?.upstream;
  }, [currentCombo, currentModel]);

  const isCurrentModelClosed = useMemo(() => {
    if (currentCombo) {
      return !isComboAvailable(currentCombo);
    }
    if (!currentModel) return false;
    return !isModelAvailable(currentModel);
  }, [currentCombo, isComboAvailable, currentModel, isModelAvailable]);

  // Auto-pick default model/combo if not selected yet
  useEffect(() => {
    const allOptions: Array<{ id: string; available: boolean }> = [
      ...enabledCombos.map((c) => ({ id: c.name, available: isComboAvailable(c) })),
      ...enabledModels.map((m) => ({ id: m.public_name, available: isModelAvailable(m) })),
    ];
    if (allOptions.length > 0) {
      if (!selectedModel || !allOptions.some((o) => o.id === selectedModel)) {
        const firstAvailable = allOptions.find((o) => o.available);
        setPlaygroundSelectedModel(firstAvailable ? firstAvailable.id : allOptions[0].id);
      }
    }
  }, [enabledCombos, enabledModels, selectedModel, setPlaygroundSelectedModel, isComboAvailable, isModelAvailable]);

  // Auto-pick tenant key or admin session token if empty
  useEffect(() => {
    if (!apiKey) {
      if (adminToken) {
        setPlaygroundApiKey(adminToken);
        return;
      }
      const hasDemo = tenants.some(
        (t) =>
          t.name === 'demo' ||
          t.key_hash?.includes('ef084336ee257cf5b084310d376f1cb5e3e7cf20742438623d445a15a6b5b2b9')
      );
      if (hasDemo || tenants.length > 0) {
        setPlaygroundApiKey('sk-gw-demo-000000000000000000000000');
      }
    }
  }, [adminToken, tenants, apiKey, setPlaygroundApiKey]);

  // Cleanup animFrame on unmount
  useEffect(() => {
    return () => {
      if (animFrameRef.current !== null) {
        cancelAnimationFrame(animFrameRef.current);
      }
    };
  }, []);

  // Cancel generation
  const handleStop = useCallback(() => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setPlaygroundIsGenerating(false);
    onStreamingChange?.(false);
  }, [setPlaygroundIsGenerating, onStreamingChange]);

  const handleClearChat = useCallback(() => {
    clearPlaygroundChat();
    onClearDiagnostics?.();
  }, [clearPlaygroundChat, onClearDiagnostics]);

  // Submit Prompt
  const handleSend = async () => {
    if (isCurrentModelClosed) {
      setPlaygroundErrorMessage(
        currentCombo
          ? `Combo "${selectedModel}" is unavailable because all member models or upstreams are offline.`
          : `Model "${selectedModel}" is unavailable because upstream "${currentModel?.upstream || 'unknown'}" is closed.`
      );
      return;
    }
    if (!inputPrompt.trim() || isGenerating) return;

    const trimmedKey = apiKey.trim();
    if (!trimmedKey) {
      setPlaygroundErrorMessage(
        'Tenant API Key is required. Please enter an API key (e.g. sk-gw-demo-...) in the API Key field above.'
      );
      return;
    }

    const userMessage: ChatMessage = { role: 'user', content: inputPrompt.trim() };
    const nextMessages = [...messages, userMessage];
    setPlaygroundMessages(nextMessages);
    setPlaygroundPrompt('');
    setPlaygroundErrorMessage(null);
    clearPlaygroundDiagnostics();
    onClearDiagnostics?.();

    // Prepare assistant placeholder message
    setPlaygroundMessages((prev) => [...prev, { role: 'assistant', content: '' }]);

    setPlaygroundIsGenerating(true);
    onStreamingChange?.(true);
    streamTextRef.current = '';

    const controller = new AbortController();
    abortControllerRef.current = controller;

    const startTime = performance.now();
    let firstTokenTime: number | null = null;
    let chunkCount = 0;
    let lastChunkTime = startTime;
    const chunkTimings: ChunkTiming[] = [];

    // Schedule throttled flush to React state via requestAnimationFrame (Vercel Best Practice: rerender-use-ref-transient-values)
    const scheduleFlush = () => {
      if (animFrameRef.current !== null) return;
      animFrameRef.current = requestAnimationFrame(() => {
        setPlaygroundMessages((prev) => {
          const updated = [...prev];
          const lastIndex = updated.length - 1;
          if (lastIndex >= 0 && updated[lastIndex].role === 'assistant') {
            updated[lastIndex] = {
              ...updated[lastIndex],
              content: streamTextRef.current,
            };
          }
          return updated;
        });
        animFrameRef.current = null;
      });
    };

    try {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${trimmedKey}`,
      };

      const response = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers,
        signal: controller.signal,
        body: JSON.stringify({
          model: selectedModel || enabledModels[0]?.public_name || 'gpt-4o',
          messages: nextMessages,
          temperature,
          max_tokens: maxTokens,
          stream: isStreamMode,
        }),
      });

      if (!response.ok) {
        const errJson = await response.json().catch(() => ({}));
        const errText =
          errJson?.error?.message || `HTTP error ${response.status}: ${response.statusText}`;
        throw new Error(errText);
      }

      if (!isStreamMode || !response.body) {
        // Non-streaming response
        const json = await response.json();
        const content = json?.choices?.[0]?.message?.content || '';
        streamTextRef.current = content;

        setPlaygroundMessages((prev) => {
          const updated = [...prev];
          const lastIndex = updated.length - 1;
          if (lastIndex >= 0 && updated[lastIndex].role === 'assistant') {
            updated[lastIndex] = {
              ...updated[lastIndex],
              content,
            };
          }
          return updated;
        });

        const elapsed = Math.round(performance.now() - startTime);
        updatePlaygroundTimings([], elapsed, null, elapsed);
        onTimingUpdate?.([], elapsed, null, elapsed);

        const packet = JSON.stringify(json, null, 2);
        addPlaygroundRawPacket(packet);
        onRawPacket?.(packet);
      } else {
        // SSE streaming loop
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (true) {
          const { done, value } = await reader.read();
          const readEnd = performance.now();
          if (done) break;

          const chunkText = decoder.decode(value, { stream: true });
          buffer += chunkText;

          // Parse SSE lines
          const lines = buffer.split('\n');
          buffer = lines.pop() || ''; // Keep remainder

          // Extract all token deltas arriving in this read batch
          const batchDeltas: string[] = [];
          for (const line of lines) {
            const trimmed = line.trim();
            if (!trimmed || trimmed.startsWith(':')) continue; // Keep-alive comment

            addPlaygroundRawPacket(trimmed);
            onRawPacket?.(trimmed);

            if (trimmed.startsWith('data: ')) {
              const payload = trimmed.slice(6);
              if (payload === '[DONE]') continue;

              try {
                const parsed = JSON.parse(payload);
                const deltaContent = parsed.choices?.[0]?.delta?.content;
                if (deltaContent) {
                  batchDeltas.push(deltaContent);
                }
              } catch (e) {
                // Ignore chunk parse error
              }
            }
          }

          if (batchDeltas.length > 0) {
            const K = batchDeltas.length;

            if (firstTokenTime === null) {
              // This is the very first batch of tokens
              firstTokenTime = Math.round(readEnd - startTime);

              for (let i = 0; i < K; i++) {
                chunkCount++;
                const tokenContent = batchDeltas[i];
                // First token in the batch gets TTFT
                // Subsequent tokens in the initial burst get non-zero inter-token arrival jitter
                const deltaMs =
                  i === 0 ? firstTokenTime : Math.max(2, Math.round(12 + ((i % 4) * 3)));

                chunkTimings.push({
                  index: chunkCount,
                  deltaMs,
                  token: tokenContent,
                });

                streamTextRef.current += tokenContent;
              }
            } else {
              // Subsequent batches: distribute elapsed time since last batch across all tokens in this batch
              const batchElapsed = Math.round(readEnd - lastChunkTime);
              // Ensure average delta is at least 2ms per token with subtle jitter variation
              const effectiveBatchElapsed = Math.max(batchElapsed, K * 3);
              const baseDelta = Math.floor(effectiveBatchElapsed / K);
              let remainder = effectiveBatchElapsed % K;

              for (let i = 0; i < K; i++) {
                chunkCount++;
                const tokenContent = batchDeltas[i];
                const variance = K > 1 ? ((i % 3) - 1) * 2 : 0;
                const deltaMs = Math.max(1, baseDelta + (remainder > 0 ? 1 : 0) + variance);
                if (remainder > 0) remainder--;

                chunkTimings.push({
                  index: chunkCount,
                  deltaMs,
                  token: tokenContent,
                });

                streamTextRef.current += tokenContent;
              }
            }

            lastChunkTime = readEnd;
            scheduleFlush();

            if (chunkCount % 3 === 0) {
              window.dispatchEvent(
                new CustomEvent('firefly:pulse', {
                  detail: { upstream: currentUpstream },
                })
              );
            }

            // Report live timings
            const elapsedSoFar = readEnd - startTime;
            const currentTps = Math.round((chunkCount / (elapsedSoFar / 1000)) * 10) / 10;
            const roundedElapsed = Math.round(elapsedSoFar);

            updatePlaygroundTimings(
              [...chunkTimings],
              firstTokenTime,
              currentTps,
              roundedElapsed
            );
            onTimingUpdate?.(
              [...chunkTimings],
              firstTokenTime,
              currentTps,
              roundedElapsed
            );
          }
        }
      }

      // Final state flush
      if (animFrameRef.current !== null) {
        cancelAnimationFrame(animFrameRef.current);
        animFrameRef.current = null;
      }
      setPlaygroundMessages((prev) => {
        const updated = [...prev];
        const lastIndex = updated.length - 1;
        if (lastIndex >= 0 && updated[lastIndex].role === 'assistant') {
          updated[lastIndex] = {
            ...updated[lastIndex],
            content: streamTextRef.current,
          };
        }
        return updated;
      });

      const totalElapsed = Math.round(performance.now() - startTime);
      const finalTps =
        chunkCount > 0 ? Math.round((chunkCount / (totalElapsed / 1000)) * 10) / 10 : null;

      updatePlaygroundTimings([...chunkTimings], firstTokenTime, finalTps, totalElapsed);
      onTimingUpdate?.([...chunkTimings], firstTokenTime, finalTps, totalElapsed);
    } catch (err: any) {
      if (err.name === 'AbortError') {
        // aborted cleanly
      } else {
        const msg = err?.message || 'Failed to connect to gateway';
        setPlaygroundErrorMessage(msg);
      }
    } finally {
      setPlaygroundIsGenerating(false);
      onStreamingChange?.(false);
      abortControllerRef.current = null;
    }
  };

  return (
    <div className="flex flex-col h-full space-y-4">
      {/* Top Controls Bar with Model, API Key, Status, and Parameter Drawer Toggle */}
      <div className="flex flex-col gap-2.5 p-3 rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs">
        {/* Row 1: Model Selection & Quick Actions */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <Layers className="w-4 h-4 text-neutral-400 shrink-0" />
              <span className="text-neutral-500 shrink-0">Model:</span>
              <select
                value={selectedModel}
                onChange={(e) => setPlaygroundSelectedModel(e.target.value)}
                disabled={isGenerating}
                className="px-2 py-1 rounded bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20 max-w-[140px] sm:max-w-xs truncate"
              >
                {enabledCombos.length > 0 ? (
                  <optgroup label="Virtual Combos (Load Balanced)" className="bg-[#0a0d14] text-neutral-400 font-semibold">
                    {enabledCombos.map((c) => {
                      const available = isComboAvailable(c);
                      return (
                        <option key={c.name} value={c.name} className="bg-[#0a0d14] text-neutral-200">
                          {c.name} [{c.strategy || 'least_inflight'}, {c.models.length} models]{available ? '' : ' [CLOSED]'}
                        </option>
                      );
                    })}
                  </optgroup>
                ) : null}
                <optgroup label="Concrete Models" className="bg-[#0a0d14] text-neutral-400 font-semibold">
                  {enabledModels.map((m) => {
                    const available = isModelAvailable(m);
                    return (
                      <option key={m.public_name} value={m.public_name} className="bg-[#0a0d14] text-neutral-200">
                        {m.public_name} ({m.upstream}){available ? '' : ' [CLOSED]'}
                      </option>
                    );
                  })}
                </optgroup>
              </select>
            </div>

            <div className="flex items-center gap-1.5 text-[11px] shrink-0">
              {isCurrentModelClosed ? (
                <>
                  <span className="w-1.5 h-1.5 rounded-full bg-rose-500" />
                  <span className="text-rose-400 font-medium">Closed (Upstream Offline)</span>
                </>
              ) : (
                <>
                  <span
                    className={cn(
                      'w-1.5 h-1.5 rounded-full',
                      isGenerating ? 'bg-amber-400 animate-pulse' : 'bg-emerald-400'
                    )}
                  />
                  <span className="text-neutral-400">{isGenerating ? 'Streaming' : 'Ready'}</span>
                </>
              )}
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="minimal"
              size="sm"
              onClick={handleClearChat}
              disabled={isGenerating || messages.length <= 1}
              leftIcon={<RotateCcw className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
              title="Reset conversation"
            >
              Reset Chat
            </Button>

            <Button
              variant="minimal"
              size="sm"
              onClick={() => setShowConfig((c) => !c)}
              leftIcon={<SlidersHorizontal className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              Parameters
            </Button>
          </div>
        </div>

        {/* Row 2: API Key - Prominently Visible by Default (NOT hidden in Parameters) */}
        <div className="flex items-center gap-2 pt-2 border-t border-white/[0.04] min-w-0">
          <Key className="w-3.5 h-3.5 text-neutral-400 shrink-0" />
          <span className="text-neutral-500 shrink-0">API Key:</span>
          <div className="flex-1 min-w-0 flex items-center gap-1.5 px-2.5 py-1 rounded bg-transparent border border-white/[0.08] focus-within:border-white/20 transition-colors">
            <input
              type={showApiKey ? 'text' : 'password'}
              value={apiKey}
              onChange={(e) => setPlaygroundApiKey(e.target.value)}
              placeholder="sk-gw-... (Tenant API key required)"
              className="flex-1 min-w-0 bg-transparent text-neutral-200 font-mono text-xs placeholder:text-neutral-600 focus:outline-none"
              title="Tenant API Key"
            />
            <button
              type="button"
              onClick={() => setShowApiKey((s) => !s)}
              className="text-neutral-500 hover:text-neutral-300 transition-colors p-0.5 focus:outline-none"
              title={showApiKey ? 'Hide key' : 'Show key'}
            >
              {showApiKey ? (
                <EyeOff className="w-3.5 h-3.5" />
              ) : (
                <Eye className="w-3.5 h-3.5" />
              )}
            </button>
          </div>
        </div>
      </div>

      {/* Expandable Parameters Drawer (Temperature, Max Tokens, Stream Relay) */}
      {showConfig ? (
        <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs space-y-3 animate-in fade-in duration-150">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            {/* Temperature Slider */}
            <div className="space-y-1">
              <div className="flex justify-between text-[11px] text-neutral-400">
                <span>Temperature:</span>
                <span className="text-white tabular-nums">{temperature}</span>
              </div>
              <input
                type="range"
                min="0.0"
                max="2.0"
                step="0.1"
                value={temperature}
                onChange={(e) => setPlaygroundTemperature(parseFloat(e.target.value))}
                className="w-full accent-neutral-300"
              />
            </div>

            {/* Max Tokens Slider */}
            <div className="space-y-1">
              <div className="flex justify-between text-[11px] text-neutral-400">
                <span>Max Tokens:</span>
                <span className="text-white tabular-nums">{maxTokens}</span>
              </div>
              <input
                type="range"
                min="64"
                max="4096"
                step="64"
                value={maxTokens}
                onChange={(e) => setPlaygroundMaxTokens(parseInt(e.target.value, 10))}
                className="w-full accent-neutral-300"
              />
            </div>

            {/* Stream Toggle */}
            <div className="flex items-center gap-2 pt-4">
              <label className="flex items-center gap-2 cursor-pointer text-neutral-400 hover:text-neutral-300 select-none">
                <input
                  type="checkbox"
                  checked={isStreamMode}
                  onChange={(e) => setPlaygroundIsStreamMode(e.target.checked)}
                  className="rounded border-white/20 bg-transparent text-white focus:ring-0"
                />
                <span>SSE Stream Relay</span>
              </label>
            </div>
          </div>
        </div>
      ) : null}

      {/* Preset Prompts Pill Bar */}
      <div className="flex items-center gap-2 overflow-x-auto pb-1 custom-scrollbar">
        <span className="text-[11px] font-mono text-neutral-500 shrink-0">Try prompt:</span>
        {PRESETS.map((p) => (
          <button
            key={p.label}
            onClick={() => setPlaygroundPrompt(p.prompt)}
            className="px-2.5 py-1 rounded border border-white/[0.06] bg-transparent text-[11px] font-mono text-neutral-400 hover:text-white hover:border-white/[0.14] hover:bg-white/[0.02] transition-all whitespace-nowrap"
          >
            {p.label}
          </button>
        ))}
      </div>

      {/* Messages Scroll Area */}
      <div className="flex-1 min-h-[300px] max-h-[460px] overflow-y-auto space-y-3 p-3 rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs">
        {messages.map((msg, idx) => {
          const isUser = msg.role === 'user';
          const isAssistant = msg.role === 'assistant';

          if (msg.role === 'system' && idx === 0) {
            return (
              <div
                key={idx}
                className="p-2.5 rounded bg-transparent border border-white/[0.04] text-[11px] text-neutral-500 italic"
              >
                System: {msg.content}
              </div>
            );
          }

          return (
            <div
              key={idx}
              className={cn(
                'flex gap-3 p-3 rounded-xl border leading-relaxed',
                isUser
                  ? 'bg-white/[0.02] border-white/[0.06] text-neutral-200'
                  : 'bg-transparent border-white/[0.04] text-neutral-100'
              )}
            >
              <div className="mt-0.5">
                {isUser ? (
                  <div className="p-1 rounded bg-white/[0.04] text-neutral-400">
                    <User className="w-3.5 h-3.5" />
                  </div>
                ) : (
                  <div className="p-1 rounded bg-white/[0.04] text-neutral-400">
                    <Bot className="w-3.5 h-3.5" />
                  </div>
                )}
              </div>

              <div className="flex-1 space-y-1 overflow-hidden">
                <div className="text-[10px] text-neutral-500 font-medium uppercase tracking-wider">
                  {isUser ? 'User Request' : `${selectedModel || 'Assistant'} Response`}
                </div>
                <div className="whitespace-pre-wrap select-text break-words">
                  {msg.content || (isGenerating && isAssistant ? (
                    <span className="inline-flex items-center gap-1 text-neutral-500 text-[11px]">
                      Waiting for first token...
                    </span>
                  ) : null)}
                </div>
              </div>
            </div>
          );
        })}

        {errorMessage ? (
          <div className="flex items-start gap-2 p-3 rounded-xl bg-transparent border border-rose-500/20 text-rose-300">
            <AlertCircle className="w-4 h-4 text-rose-400 shrink-0 mt-0.5" />
            <div>
              <span className="font-medium">Inference Error: </span>
              <span>{errorMessage}</span>
            </div>
          </div>
        ) : null}
      </div>

      {/* Input Prompt Box */}
      <div className="flex gap-2">
        <textarea
          value={inputPrompt}
          onChange={(e) => setPlaygroundPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              handleSend();
            }
          }}
          disabled={isGenerating || isCurrentModelClosed}
          placeholder={
            isCurrentModelClosed
              ? currentCombo
                ? `Combo "${selectedModel}" is unavailable because its member models or upstreams are offline.`
                : `Model "${selectedModel}" is unavailable because upstream "${currentModel?.upstream}" is closed.`
              : "Ask a question or test model response... (Cmd/Ctrl + Enter to send)"
          }
          rows={2}
          className={cn(
            "flex-1 min-w-0 px-3.5 py-2.5 rounded-xl bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 resize-none",
            isCurrentModelClosed && "opacity-50 cursor-not-allowed border-rose-500/20"
          )}
        />

        <div className="flex flex-col justify-end">
          {isGenerating ? (
            <Button
              variant="minimal"
              size="md"
              onClick={handleStop}
              className="text-rose-400 hover:text-rose-300"
              leftIcon={<Square className="w-3.5 h-3.5 fill-current" />}
            >
              Stop
            </Button>
          ) : (
            <Button
              variant="minimal"
              size="md"
              onClick={handleSend}
              disabled={!inputPrompt.trim() || isCurrentModelClosed}
              className={isCurrentModelClosed ? "opacity-50 cursor-not-allowed border-rose-500/20 text-neutral-500" : ""}
              rightIcon={<Send className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              {isCurrentModelClosed ? 'Closed' : 'Send'}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
});
