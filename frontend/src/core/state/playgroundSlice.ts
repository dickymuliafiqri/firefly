import type { StateCreator } from 'zustand';

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
  /** Optional model reasoning / "thinking" trace, streamed via delta.reasoning_content. */
  reasoning?: string;
}

export interface ChunkTiming {
  index: number;
  deltaMs: number;
  token: string;
}

export interface PlaygroundSlice {
  playgroundMessages: ChatMessage[];
  playgroundPrompt: string;
  playgroundSelectedModel: string;
  playgroundApiKey: string;
  playgroundApiKeyManuallyEdited: boolean;
  playgroundTemperature: number;
  playgroundMaxTokens: number;
  playgroundIsStreamMode: boolean;
  playgroundIsGenerating: boolean;
  playgroundErrorMessage: string | null;

  playgroundTtftMs: number | null;
  playgroundTotalDurationMs: number | null;
  playgroundTps: number | null;
  playgroundTimings: ChunkTiming[];
  playgroundRawPackets: string[];

  // Actions
  setPlaygroundMessages: (
    messages: ChatMessage[] | ((prev: ChatMessage[]) => ChatMessage[])
  ) => void;
  setPlaygroundPrompt: (prompt: string) => void;
  setPlaygroundSelectedModel: (model: string) => void;
  setPlaygroundApiKey: (key: string, options?: { manual?: boolean }) => void;
  setPlaygroundTemperature: (temp: number) => void;
  setPlaygroundMaxTokens: (tokens: number) => void;
  setPlaygroundIsStreamMode: (isStream: boolean) => void;
  setPlaygroundIsGenerating: (isGenerating: boolean) => void;
  setPlaygroundErrorMessage: (msg: string | null) => void;
  updatePlaygroundTimings: (
    timings: ChunkTiming[],
    ttft: number | null,
    tps: number | null,
    elapsed: number | null
  ) => void;
  addPlaygroundRawPacket: (packet: string) => void;
  clearPlaygroundDiagnostics: () => void;
  clearPlaygroundChat: () => void;
}

const INITIAL_MESSAGES: ChatMessage[] = [
  {
    role: 'system',
    content: 'You are a helpful AI assistant connected through the Firefly nocturnal gateway.',
  },
];

export const createPlaygroundSlice: StateCreator<
  PlaygroundSlice,
  [],
  [],
  PlaygroundSlice
> = (set) => ({
  playgroundMessages: INITIAL_MESSAGES,
  playgroundPrompt: '',
  playgroundSelectedModel: '',
  playgroundApiKey: '',
  playgroundApiKeyManuallyEdited: false,
  playgroundTemperature: 0.7,
  playgroundMaxTokens: 1024,
  playgroundIsStreamMode: true,
  playgroundIsGenerating: false,
  playgroundErrorMessage: null,

  playgroundTtftMs: null,
  playgroundTotalDurationMs: null,
  playgroundTps: null,
  playgroundTimings: [],
  playgroundRawPackets: [],

  setPlaygroundMessages: (messages) =>
    set((state) => ({
      playgroundMessages:
        typeof messages === 'function'
          ? messages(state.playgroundMessages)
          : messages,
    })),

  setPlaygroundPrompt: (prompt) =>
    set(() => ({ playgroundPrompt: prompt })),

  setPlaygroundSelectedModel: (model) =>
    set(() => ({ playgroundSelectedModel: model })),

  setPlaygroundApiKey: (key, options) => {
    set(() => ({
      playgroundApiKey: key,
      playgroundApiKeyManuallyEdited: options?.manual ?? true,
    }));
  },

  setPlaygroundTemperature: (temp) =>
    set(() => ({ playgroundTemperature: temp })),

  setPlaygroundMaxTokens: (tokens) =>
    set(() => ({ playgroundMaxTokens: tokens })),

  setPlaygroundIsStreamMode: (isStream) =>
    set(() => ({ playgroundIsStreamMode: isStream })),

  setPlaygroundIsGenerating: (isGenerating) =>
    set(() => ({ playgroundIsGenerating: isGenerating })),

  setPlaygroundErrorMessage: (msg) =>
    set(() => ({ playgroundErrorMessage: msg })),

  updatePlaygroundTimings: (timings, ttft, tps, elapsed) =>
    set((state) => ({
      playgroundTimings: timings,
      playgroundTtftMs: ttft !== null ? ttft : state.playgroundTtftMs,
      playgroundTps: tps !== null ? tps : state.playgroundTps,
      playgroundTotalDurationMs:
        elapsed !== null ? elapsed : state.playgroundTotalDurationMs,
    })),

  addPlaygroundRawPacket: (packet) =>
    set((state) => {
      const prev = state.playgroundRawPackets;
      const next =
        prev.length > 100 ? [...prev.slice(-99), packet] : [...prev, packet];
      return { playgroundRawPackets: next };
    }),

  clearPlaygroundDiagnostics: () =>
    set(() => ({
      playgroundTimings: [],
      playgroundRawPackets: [],
      playgroundTtftMs: null,
      playgroundTotalDurationMs: null,
      playgroundTps: null,
    })),

  clearPlaygroundChat: () =>
    set(() => ({
      playgroundMessages: INITIAL_MESSAGES,
      playgroundErrorMessage: null,
      playgroundTimings: [],
      playgroundRawPackets: [],
      playgroundTtftMs: null,
      playgroundTotalDurationMs: null,
      playgroundTps: null,
    })),
});
