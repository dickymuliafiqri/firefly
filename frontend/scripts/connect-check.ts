/**
 * Headless verification for the Connect page's snippet builder (run with
 * `npm run check:connect`). The project ships no test runner, so this bundles
 * the pure module (`src/lib/connect.ts`) with esbuild and asserts the
 * invariants a copied snippet must never break:
 *
 *   1. base-URL normalization: a missing scheme becomes `http://`, trailing
 *      slashes/query/hash are dropped, and a trailing `/v1` is stripped so the
 *      OpenAI base URL never doubles into `/v1/v1` (operators paste either form);
 *   2. the documented endpoint table is exactly the inbound data plane of
 *      `internal/server/router.go` — no invented route, no missing one;
 *   3. every preset substitutes the base URL, the key and the model, so a
 *      copied config is never half-filled;
 *   4. JSON presets parse and carry the keys their client reads
 *      (`provider.options.baseURL` / `models[0].apiBase`);
 *   5. masked or empty secrets are refused by `isUsableKey` and fall back to
 *      `PLACEHOLDER_KEY` — a copy never ships `sk-gw-••••1234` as a credential;
 *   6. no preset ever targets `/v1/messages` or `/v1/responses`: Firefly has no
 *      inbound Anthropic/Responses route, so such a snippet could only 404;
 *   7. `maskKey` reveals at most the last four characters.
 */
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  CLIENT_PRESETS,
  DEFAULT_ORIGIN,
  GATEWAY_ENDPOINTS,
  PLACEHOLDER_KEY,
  PLACEHOLDER_MODEL,
  clientCode,
  clientFields,
  connectionBundle,
  endpointUrl,
  gatewayOrigin,
  isJsonPreset,
  isUsableKey,
  maskKey,
  openAiBaseUrl,
} from '../src/lib/connect';
import type { ClientContext } from '../src/lib/connect';
import { ConnectPage } from '../src/pages/ConnectPage';

let failures = 0;

function check(label: string, actual: unknown, expected: unknown): void {
  const got = String(JSON.stringify(actual));
  const want = String(JSON.stringify(expected));
  if (got === want) {
    console.log(`  ok   ${label}`);
    return;
  }
  failures += 1;
  console.error(`  FAIL ${label}\n         got:      ${got}\n         expected: ${want}`);
}

function ok(label: string, condition: boolean): void {
  check(label, condition, true);
}

// --- Base URL normalization -------------------------------------------------
console.log('\nBase URL normalization');
check('bare host:port gains the http scheme', gatewayOrigin('localhost:8080'), 'http://localhost:8080');
check('trailing slash is dropped', gatewayOrigin('http://gw.example.com/'), 'http://gw.example.com');
check('https survives', gatewayOrigin('https://gw.example.com/'), 'https://gw.example.com');
check('a pasted /v1 is stripped (no /v1/v1)', gatewayOrigin('http://localhost:8080/v1'), 'http://localhost:8080');
check('a pasted /v1/ is stripped too', gatewayOrigin('http://localhost:8080/v1/'), 'http://localhost:8080');
check(
  'a reverse-proxy path prefix survives',
  gatewayOrigin('https://ex.com/firefly/v1'),
  'https://ex.com/firefly',
);
check('query and hash are dropped', gatewayOrigin('http://h:8080/v1?x=1#y'), 'http://h:8080');
check('empty input falls back to the dashboard origin', gatewayOrigin(''), DEFAULT_ORIGIN);
check('openAiBaseUrl appends /v1 once', openAiBaseUrl('http://localhost:8080/v1'), 'http://localhost:8080/v1');
check('openAiBaseUrl adds /v1 to a bare origin', openAiBaseUrl('localhost:8080'), 'http://localhost:8080/v1');
check('endpointUrl joins without a double slash', endpointUrl('http://h:8080', '/v1/models'), 'http://h:8080/v1/models');
check(
  'endpointUrl accepts a path without a leading slash',
  endpointUrl('http://h:8080/', 'v1/models'),
  'http://h:8080/v1/models',
);

// --- Endpoint table mirrors the router -------------------------------------
console.log('\nEndpoint table (mirrors internal/server/router.go)');
check(
  'documented paths are exactly the inbound data plane',
  GATEWAY_ENDPOINTS.map((e) => e.path).sort(),
  [
    '/v1/chat/completions',
    '/v1/completions',
    '/v1/compress',
    '/v1/embeddings',
    '/v1/models',
    '/v1/usage',
  ],
);
ok(
  'every endpoint carries a method and a purpose',
  GATEWAY_ENDPOINTS.every((e) => (e.method === 'GET' || e.method === 'POST') && e.note.length > 0),
);
ok(
  'no endpoint points at an unserved route',
  GATEWAY_ENDPOINTS.every((e) => !e.path.includes('messages') && !e.path.includes('responses')),
);

// --- Key handling -----------------------------------------------------------
console.log('\nTenant key handling');
check('a real key is usable', isUsableKey('sk-gw-0123456789abcdef'), true);
check('a masked key is refused', isUsableKey('sk-gw-••••1234'), false);
check('an empty key is refused', isUsableKey(''), false);
check('an undefined key is refused', isUsableKey(undefined), false);
check('a redacted key is refused', isUsableKey('redacted'), false);
check('a too-short value is refused', isUsableKey('sk-gw'), false);
check('maskKey keeps the prefix and last four', maskKey('sk-gw-0123456789abcdef'), 'sk-gw-••••cdef');
ok(
  'maskKey never reveals the middle of the secret',
  !maskKey('sk-gw-0123456789abcdef').includes('0123'),
);

// --- Preset substitution ----------------------------------------------------
const ctx: ClientContext = {
  origin: 'http://localhost:8080',
  baseUrl: 'http://localhost:8080/v1',
  key: 'sk-gw-0123456789abcdef',
  model: 'gpt-4o',
};
const emptyKeyCtx: ClientContext = { ...ctx, key: PLACEHOLDER_KEY };

console.log('\nClient presets');
ok('the roster is not empty', CLIENT_PRESETS.length >= 8);
ok('preset ids are unique', new Set(CLIENT_PRESETS.map((p) => p.id)).size === CLIENT_PRESETS.length);
ok('preset names are unique', new Set(CLIENT_PRESETS.map((p) => p.name)).size === CLIENT_PRESETS.length);
ok(
  'every preset has a summary and an https docs link',
  CLIENT_PRESETS.every((p) => p.summary.length > 0 && p.docs.startsWith('https://')),
);
ok(
  'every preset is usable: it has fields, a code block, or both',
  CLIENT_PRESETS.every((p) => (p.fields?.length ?? 0) > 0 || Boolean(p.code)),
);
ok(
  'Cline and OpenCode ship presets (the headline clients)',
  ['cline', 'opencode'].every((id) => CLIENT_PRESETS.some((p) => p.id === id)),
);

for (const preset of CLIENT_PRESETS) {
  const text = [
    ...clientFields(preset, ctx).map((f) => `${f.label}: ${f.value}`),
    clientCode(preset, ctx),
  ]
    .filter(Boolean)
    .join('\n');

  ok(`${preset.id}: substitutes the base URL`, text.includes(ctx.baseUrl));
  ok(`${preset.id}: substitutes the key`, text.includes(ctx.key));
  ok(`${preset.id}: substitutes the model`, text.includes(ctx.model));
  ok(
    `${preset.id}: never targets an unserved inbound route`,
    !/\/v1\/messages|\/v1\/responses/.test(text),
  );
}

console.log('\nFallbacks');
ok(
  'a placeholder model is used when the catalog has none',
  clientFields(CLIENT_PRESETS[0], { ...ctx, model: PLACEHOLDER_MODEL }).some((f) =>
    f.value.includes(PLACEHOLDER_MODEL),
  ),
);
ok(
  'the placeholder key reaches every snippet',
  CLIENT_PRESETS.every((p) =>
    [clientCode(p, emptyKeyCtx), ...clientFields(p, emptyKeyCtx).map((f) => f.value)]
      .filter(Boolean)
      .join('\n')
      .includes(PLACEHOLDER_KEY),
  ),
);
ok(
  'no snippet ever ships a masked key as a credential',
  CLIENT_PRESETS.every((p) => !clientCode(p, emptyKeyCtx).includes('••')),
);

// --- JSON presets parse -----------------------------------------------------
console.log('\nJSON config presets');
for (const preset of CLIENT_PRESETS.filter(isJsonPreset)) {
  const code = clientCode(preset, ctx);
  let parsed: unknown;
  try {
    parsed = JSON.parse(code);
  } catch (err) {
    check(`${preset.id}: emits valid JSON`, `parse error: ${String(err)}`, 'valid JSON');
    continue;
  }
  check(`${preset.id}: emits valid JSON`, typeof parsed, 'object');
  const text = JSON.stringify(parsed);
  ok(`${preset.id}: carries the base URL`, text.includes(ctx.baseUrl));
  ok(`${preset.id}: carries the api key`, text.includes(ctx.key));
  ok(`${preset.id}: carries the model`, text.includes(ctx.model));
}

const opencode = CLIENT_PRESETS.find((p) => p.id === 'opencode');
const ocParsed = JSON.parse(clientCode(opencode!, ctx)) as {
  provider?: Record<string, { options?: Record<string, string>; models?: Record<string, unknown> }>;
};
check('opencode.json provider name', Object.keys(ocParsed.provider ?? {}), ['firefly']);
check(
  'opencode.json uses the openai-compatible SDK',
  ocParsed.provider?.firefly?.options?.baseURL,
  ctx.baseUrl,
);
ok('opencode.json registers the model', Boolean(ocParsed.provider?.firefly?.models?.[ctx.model]));

const cont = CLIENT_PRESETS.find((p) => p.id === 'continue');
const contParsed = JSON.parse(clientCode(cont!, ctx)) as {
  models?: Array<{ provider?: string; apiBase?: string; apiKey?: string; model?: string }>;
};
check('Continue provider', contParsed.models?.[0]?.provider, 'openai');
check('Continue apiBase', contParsed.models?.[0]?.apiBase, ctx.baseUrl);
check('Continue model', contParsed.models?.[0]?.model, ctx.model);

// --- Copy-everything bundle -------------------------------------------------
console.log('\nConnection bundle');
const bundle = connectionBundle(ctx);
ok('bundle carries the gateway origin', bundle.includes(ctx.origin));
ok('bundle carries the base URL', bundle.includes(ctx.baseUrl));
ok('bundle carries the key', bundle.includes(ctx.key));
ok('bundle carries the model', bundle.includes(ctx.model));
ok(
  'bundle lists every client',
  CLIENT_PRESETS.every((p) => bundle.includes(`### ${p.name} (${p.kind})`)),
);
ok(
  'bundle lists every endpoint with its method',
  GATEWAY_ENDPOINTS.every((e) => bundle.includes(`${e.method} ${ctx.origin}${e.path}`)),
);
ok(
  'bundle can be limited to a subset of clients',
  connectionBundle(ctx, [opencode!]).includes('### OpenCode (CLI)') &&
    !connectionBundle(ctx, [opencode!]).includes('### Cline (IDE extension)'),
);
ok('bundle ends with a newline', bundle.endsWith('\n'));

// --- Page smoke render (JSX-free: createElement) ---------------------------
// The page body only renders once the catalog query has data, so the settings
// query is seeded in a fresh QueryClient — this is the same shape the Tenants
// page renders, minus the router. Both an empty and a populated catalog must
// render without throwing, and a masked tenant key must never appear in full.
console.log('\nPage render');

function renderPage(settings: Record<string, unknown>): string {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(['settings'], settings);
  return renderToStaticMarkup(
    createElement(QueryClientProvider, { client: qc }, createElement(ConnectPage, {})),
  );
}

const populated = renderPage({
  upstreams: [],
  models: [
    { public_name: 'gpt-4o', upstream: 'openai-main', upstream_model: 'gpt-4o' },
    { public_name: 'claude-sonnet-4-5', upstream: 'anthropic-prod', upstream_model: 'claude-sonnet-4-5' },
  ],
  tenants: [
    { name: 'alpha', api_key: 'sk-gw-aaaabbbbccccdddd', status: 'active', allowed_models: [] },
    { name: 'beta', api_key: 'sk-gw-••••9999', status: 'active', allowed_models: ['gpt-4o'] },
  ],
  combos: [{ name: 'smart', models: ['gpt-4o', 'claude-sonnet-4-5'] }],
});

ok('rendered the base URL', populated.includes(DEFAULT_ORIGIN));
ok('rendered the OpenAI base URL', populated.includes(`${DEFAULT_ORIGIN}/v1`));
ok('rendered both tenant options', populated.includes('alpha') && populated.includes('beta'));
ok('rendered the tenant key mask', populated.includes('sk-gw-••••dddd'));
ok('never printed a masked key in full', !populated.includes('sk-gw-••••9999'));
ok('rendered a catalog model', populated.includes('gpt-4o'));
ok('rendered the client roster', populated.includes('Cline') && populated.includes('OpenCode'));
ok('rendered the endpoint table', populated.includes('/v1/chat/completions'));

const empty = renderPage({ upstreams: [], models: [], tenants: [] });
ok('empty catalog still renders', empty.includes('No tenants registered yet'));
ok('empty catalog falls back to a placeholder model', empty.includes(PLACEHOLDER_MODEL));
ok(
  'empty catalog ships the placeholder key, never a secret',
  empty.includes(PLACEHOLDER_KEY) && !empty.includes('••'),
);

if (failures > 0) throw new Error(`${failures} check(s) failed`);
console.log('\nALL CHECKS PASSED');
