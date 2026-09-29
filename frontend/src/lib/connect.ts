/**
 * connect.ts — copy-ready connection values for external AI clients
 * (Services → Connect page).
 *
 * The dashboard is served by the *same* listener as the data plane
 * (`-addr`, default `0.0.0.0:8080`), so `window.location.origin` is the
 * gateway's base URL whenever the operator opens the dashboard on the
 * gateway host. Remote installs (Cloudflare Tunnel, reverse proxy) can
 * override it — the page persists the override, this module stays pure.
 *
 * Every preset below speaks the OpenAI HTTP surface Firefly actually
 * exposes, mirrored from `internal/server/router.go`: `/v1/chat/completions`,
 * `/v1/completions`, `/v1/embeddings`, `/v1/compress`, `/v1/models`,
 * `/v1/usage`. The Anthropic-native `/v1/messages` and `/v1/responses`
 * routes are **not** served — the `anthropic` protocol is an outbound
 * translation — so no preset may point an Anthropic/Responses-native client
 * at the gateway.
 */

export const DEFAULT_ORIGIN = 'http://localhost:8080';
/** Stand-in used whenever the catalog cannot reveal the tenant secret. */
export const PLACEHOLDER_KEY = 'sk-gw-YOUR_KEY';
/** Stand-in used when the catalog has no model to suggest. */
export const PLACEHOLDER_MODEL = 'your-model-id';

const BASE_URL_STORE = 'firefly.connect.baseUrl.v1';
const KEY_STORE_PREFIX = 'firefly.connect.key.';

export interface GatewayEndpoint {
  method: string;
  path: string;
  note: string;
}

/** The inbound data plane, mirrored from `internal/server/router.go`. */
export const GATEWAY_ENDPOINTS: GatewayEndpoint[] = [
  { method: 'POST', path: '/v1/chat/completions', note: 'Chat completions — the route every AI agent calls.' },
  { method: 'POST', path: '/v1/completions', note: 'Legacy text completions.' },
  { method: 'POST', path: '/v1/embeddings', note: 'Embeddings.' },
  { method: 'POST', path: '/v1/compress', note: 'Standalone Token Saver prompt / tool-output compression.' },
  { method: 'GET', path: '/v1/models', note: 'Model catalog visible to this tenant key.' },
  { method: 'GET', path: '/v1/usage', note: 'Tenant quota, consumption and expiry.' },
];

/** Origin of the dashboard serving this page (same origin as the data plane). */
export function defaultOrigin(): string {
  return typeof window === 'undefined' ? DEFAULT_ORIGIN : window.location.origin;
}

/**
 * Normalizes operator input into a bare gateway origin: a missing scheme
 * becomes `http://`, trailing slashes and query/hash are dropped, and a
 * trailing `/v1` is stripped so `openAiBaseUrl` can append it exactly once
 * (an operator pasting the OpenAI base URL must not get `/v1/v1`).
 * Empty input falls back to the dashboard origin.
 */
export function gatewayOrigin(raw?: string | null): string {
  let value = (raw ?? '').trim().replace(/[?#].*$/, '');
  if (!value) return defaultOrigin();
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(value)) value = 'http://' + value;
  const trimmed = value.replace(/\/+$/, '');
  const stripped = trimmed.replace(/\/v1$/i, '');
  return stripped || trimmed || defaultOrigin();
}

/** OpenAI-compatible base URL every preset hands to its client (`origin/v1`). */
export function openAiBaseUrl(raw?: string | null): string {
  return gatewayOrigin(raw) + '/v1';
}

/** Absolute URL for one of `GATEWAY_ENDPOINTS`, joined without a double slash. */
export function endpointUrl(raw: string | null | undefined, path: string): string {
  return gatewayOrigin(raw) + (path.startsWith('/') ? path : `/${path}`);
}

/**
 * True when a value can be used as a real bearer credential. Masked catalog
 * keys (`sk-gw-••••1234`) and empty secrets are refused so a copied snippet
 * never ships a placeholder dressed up as a secret.
 */
export function isUsableKey(key?: string | null): boolean {
  const value = (key ?? '').trim();
  if (value.length < 8) return false;
  if (/[•*]/.test(value)) return false;
  if (/redacted/i.test(value)) return false;
  return true;
}

/** Reveal-safe preview: `sk-gw-••••1234` (never the middle of the secret). */
export function maskKey(key: string): string {
  const value = key.trim();
  if (value.length <= 4) return '•'.repeat(Math.max(value.length, 1));
  return `${value.slice(0, 6)}••••${value.slice(-4)}`;
}

/** Session-scoped tenant key pasted by the operator (never persisted to disk). */
export function readTenantKey(tenant: string): string {
  if (typeof window === 'undefined' || !tenant) return '';
  try {
    return window.sessionStorage.getItem(KEY_STORE_PREFIX + tenant) ?? '';
  } catch {
    return '';
  }
}

export function writeTenantKey(tenant: string, key: string): void {
  if (typeof window === 'undefined' || !tenant) return;
  try {
    const value = key.trim();
    if (value) window.sessionStorage.setItem(KEY_STORE_PREFIX + tenant, value);
    else window.sessionStorage.removeItem(KEY_STORE_PREFIX + tenant);
  } catch {
    // Private mode / storage disabled: the value stays in component state.
  }
}

/** Persisted base-URL override (non-secret; survives a reload). */
export function readBaseUrl(): string {
  if (typeof window === 'undefined') return '';
  try {
    return window.localStorage.getItem(BASE_URL_STORE) ?? '';
  } catch {
    return '';
  }
}

export function writeBaseUrl(url: string): void {
  if (typeof window === 'undefined') return;
  try {
    const value = url.trim();
    if (value) window.localStorage.setItem(BASE_URL_STORE, value);
    else window.localStorage.removeItem(BASE_URL_STORE);
  } catch {
    // Storage disabled: the field keeps working for this page view.
  }
}

/** Values substituted into every preset. */
export interface ClientContext {
  /** Gateway origin without `/v1` (e.g. `http://localhost:8080`). */
  origin: string;
  /** OpenAI-compatible base URL (`origin/v1`). */
  baseUrl: string;
  /** Real tenant key, or `PLACEHOLDER_KEY` when the secret is unrecoverable. */
  key: string;
  /** Public model id (`models[].public_name` or a combo name). */
  model: string;
}

export interface ClientField {
  label: string;
  value: (ctx: ClientContext) => string;
}

export interface ClientCode {
  /** Caption above the block, e.g. the file / shell it belongs to. */
  label: string;
  language: 'json' | 'bash' | 'python' | 'javascript';
  code: (ctx: ClientContext) => string;
}

export interface ClientPreset {
  id: string;
  name: string;
  kind: 'IDE extension' | 'CLI' | 'Desktop' | 'SDK' | 'HTTP';
  docs: string;
  /** Where to paste what — one sentence, shown under the client name. */
  summary: string;
  /** GUI clients: one row per form field, each independently copyable. */
  fields?: ClientField[];
  /** File/env clients and SDKs: a copyable code block. */
  code?: ClientCode;
}

const baseField = (label = 'Base URL'): ClientField => ({
  label,
  value: (ctx) => ctx.baseUrl,
});
const keyField = (label = 'API Key'): ClientField => ({
  label,
  value: (ctx) => ctx.key,
});
const modelField = (label = 'Model ID'): ClientField => ({
  label,
  value: (ctx) => ctx.model,
});

/** JSON with 2-space indentation — every emitted JSON block must parse. */
function json(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

export const CLIENT_PRESETS: ClientPreset[] = [
  {
    id: 'cline',
    name: 'Cline',
    kind: 'IDE extension',
    docs: 'https://docs.cline.bot/provider-config/openai-compatible',
    summary:
      'VS Code → Cline → Settings → API Configuration. Pick “OpenAI Compatible” and paste the four fields below.',
    fields: [
      { label: 'API Provider', value: () => 'OpenAI Compatible' },
      baseField(),
      keyField(),
      modelField(),
    ],
  },
  {
    id: 'roo',
    name: 'Roo Code',
    kind: 'IDE extension',
    docs: 'https://docs.roocode.com/providers/openai-compatible',
    summary:
      'VS Code → Roo Code → Settings → Providers. Choose “OpenAI Compatible”, then fill these fields.',
    fields: [
      { label: 'API Provider', value: () => 'OpenAI Compatible' },
      baseField(),
      keyField(),
      modelField(),
    ],
  },
  {
    id: 'kilo',
    name: 'Kilo Code',
    kind: 'IDE extension',
    docs: 'https://kilocode.ai/docs/features/api-configuration-profiles',
    summary:
      'VS Code → Kilo Code → Settings → Providers. Choose “OpenAI Compatible” and paste these fields.',
    fields: [
      { label: 'API Provider', value: () => 'OpenAI Compatible' },
      baseField(),
      keyField(),
      modelField(),
    ],
  },
  {
    id: 'cursor',
    name: 'Cursor',
    kind: 'Desktop',
    docs: 'https://docs.cursor.com/settings/api-keys',
    summary:
      'Cursor → Settings → Models: enable the OpenAI key override, paste the base URL, then add the model as a custom name.',
    fields: [
      baseField('Override OpenAI Base URL'),
      keyField('OpenAI API Key'),
      modelField('Custom model name'),
    ],
  },
  {
    id: 'continue',
    name: 'Continue',
    kind: 'IDE extension',
    docs: 'https://docs.continue.dev/customize/model-providers/openai',
    summary:
      'Add this entry to ~/.continue/config.json — it powers the Continue chat, edit and autocomplete models.',
    fields: [{ label: 'Config file', value: () => '~/.continue/config.json' }],
    code: {
      label: 'config.json',
      language: 'json',
      code: (ctx) =>
        json({
          models: [
            {
              title: 'Firefly',
              provider: 'openai',
              model: ctx.model,
              apiBase: ctx.baseUrl,
              apiKey: ctx.key,
            },
          ],
        }),
    },
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    kind: 'CLI',
    docs: 'https://opencode.ai/docs/providers/#openai-compatible',
    summary:
      'Save this to ~/.config/opencode/opencode.json (or a project-level opencode.json), then start it with `opencode`.',
    fields: [{ label: 'Config file', value: () => '~/.config/opencode/opencode.json' }],
    code: {
      label: 'opencode.json',
      language: 'json',
      code: (ctx) =>
        json({
          $schema: 'https://opencode.ai/config.json',
          provider: {
            firefly: {
              npm: '@ai-sdk/openai-compatible',
              name: 'Firefly',
              options: { baseURL: ctx.baseUrl, apiKey: ctx.key },
              models: { [ctx.model]: { name: ctx.model } },
            },
          },
        }),
    },
  },
  {
    id: 'aider',
    name: 'Aider',
    kind: 'CLI',
    docs: 'https://aider.chat/docs/llms/openai-compatible.html',
    summary:
      'Export both variables (or pass `--openai-api-base` / `--openai-api-key`) and address the model with the `openai/` prefix.',
    fields: [
      { label: 'OPENAI_API_BASE', value: (ctx) => ctx.baseUrl },
      { label: 'OPENAI_API_KEY', value: (ctx) => ctx.key },
    ],
    code: {
      label: 'shell',
      language: 'bash',
      code: (ctx) =>
        [
          `export OPENAI_API_BASE=${ctx.baseUrl}`,
          `export OPENAI_API_KEY=${ctx.key}`,
          '',
          `aider --model openai/${ctx.model}`,
        ].join('\n'),
    },
  },
  {
    id: 'python',
    name: 'OpenAI Python SDK',
    kind: 'SDK',
    docs: 'https://platform.openai.com/docs/libraries/python-library',
    summary:
      'Point the official SDK at the gateway: streaming, tools and embeddings all pass through unchanged.',
    code: {
      label: 'streaming_chat.py',
      language: 'python',
      code: (ctx) =>
        [
          'from openai import OpenAI',
          '',
          'client = OpenAI(',
          `    base_url="${ctx.baseUrl}",`,
          `    api_key="${ctx.key}",`,
          ')',
          '',
          'stream = client.chat.completions.create(',
          `    model="${ctx.model}",`,
          '    messages=[{"role": "user", "content": "Hello from Firefly"}],',
          '    stream=True,',
          ')',
          '',
          'for chunk in stream:',
          '    print(chunk.choices[0].delta.content or "", end="")',
        ].join('\n'),
    },
  },
  {
    id: 'node',
    name: 'OpenAI Node SDK',
    kind: 'SDK',
    docs: 'https://platform.openai.com/docs/libraries/node-js-library',
    summary:
      'The same contract from JavaScript/TypeScript — `baseURL` (capital URL) is the only change.',
    code: {
      label: 'streaming-chat.mjs',
      language: 'javascript',
      code: (ctx) =>
        [
          'import OpenAI from "openai";',
          '',
          'const client = new OpenAI({',
          `  baseURL: "${ctx.baseUrl}",`,
          `  apiKey: "${ctx.key}",`,
          '});',
          '',
          'const stream = await client.chat.completions.create({',
          `  model: "${ctx.model}",`,
          '  messages: [{ role: "user", content: "Hello from Firefly" }],',
          '  stream: true,',
          '});',
          '',
          'for await (const chunk of stream) {',
          '  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");',
          '}',
        ].join('\n'),
    },
  },
  {
    id: 'http',
    name: 'cURL / raw HTTP',
    kind: 'HTTP',
    docs: 'https://platform.openai.com/docs/api-reference/chat',
    summary:
      'Any HTTP client works: the gateway speaks the OpenAI wire surface, including SSE streaming.',
    code: {
      label: 'shell',
      language: 'bash',
      code: (ctx) =>
        [
          `curl ${ctx.baseUrl}/chat/completions \\`,
          `  -H "Authorization: Bearer ${ctx.key}" \\`,
          '  -H "Content-Type: application/json" \\',
          `  -d '{"model":"${ctx.model}","messages":[{"role":"user","content":"Hello from Firefly"}],"stream":true}'`,
        ].join('\n'),
    },
  },
];

/** One field of a preset, with its value already substituted. */
export interface ResolvedField {
  label: string;
  value: string;
}

/** GUI fields of one preset, ready to copy. */
export function clientFields(preset: ClientPreset, ctx: ClientContext): ResolvedField[] {
  return (preset.fields ?? []).map((field) => ({ label: field.label, value: field.value(ctx) }));
}

/** Code block of one preset, or `''` for GUI-only clients. */
export function clientCode(preset: ClientPreset, ctx: ClientContext): string {
  return preset.code ? preset.code.code(ctx) : '';
}

/** True for presets whose block is a JSON config file that must parse. */
export function isJsonPreset(preset: ClientPreset): boolean {
  return preset.code?.language === 'json';
}

/**
 * One text bundle with everything an operator needs, so a single Copy button
 * can hand the connection to a teammate (the secret is included only when the
 * caller passed a real key — otherwise it carries `PLACEHOLDER_KEY`).
 */
export function connectionBundle(
  ctx: ClientContext,
  presets: ClientPreset[] = CLIENT_PRESETS,
): string {
  const lines: string[] = [
    '# Firefly — client connection',
    '',
    `Gateway base URL : ${ctx.origin}`,
    `OpenAI base URL  : ${ctx.baseUrl}`,
    `API key          : ${ctx.key}`,
    `Default model    : ${ctx.model}`,
    '',
    '## Endpoints',
  ];
  for (const endpoint of GATEWAY_ENDPOINTS) {
    lines.push(`- ${endpoint.method} ${ctx.origin}${endpoint.path} — ${endpoint.note}`);
  }
  lines.push('', '## Clients');
  for (const preset of presets) {
    lines.push('', `### ${preset.name} (${preset.kind})`, preset.summary);
    for (const field of clientFields(preset, ctx)) {
      lines.push(`- ${field.label}: ${field.value}`);
    }
    const code = clientCode(preset, ctx);
    if (code) lines.push('', '```', code, '```');
  }
  lines.push('');
  return lines.join('\n');
}
