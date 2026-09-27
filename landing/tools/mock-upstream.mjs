// Minimal OpenAI-compatible mock upstream for demo traffic + screenshots.
import http from 'node:http';

const WORDS = 'constellation firefly lantern dusk lucid ember nova drift quiet photon moor timber fern dew amber lime cyan jade hollow creek willow birch sedge rune'.split(' ');

function pick(a) { return a[Math.floor(Math.random() * a.length)]; }

function fakeText() {
  const n = 40 + Math.floor(Math.random() * 80);
  const out = [];
  for (let i = 0; i < n; i++) out.push(pick(WORDS));
  return out.join(' ') + '.';
}

function sseChunk(res, model, delta, finish = null, usage = null) {
  const body = { id: 'chatcmpl-demo-' + Math.random().toString(36).slice(2), object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model, choices: [{ index: 0, delta, finish_reason: finish }] };
  if (usage) body.usage = usage;
  res.write(`data: ${JSON.stringify(body)}\n\n`);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (req.method === 'GET' && url.pathname === '/v1/models') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ object: 'list', data: [{ id: 'gpt-4o-mini', object: 'model' }, { id: 'deepseek-chat', object: 'model' }] }));
    return;
  }
  if (req.method === 'POST' && (url.pathname === '/v1/chat/completions' || url.pathname === '/chat/completions')) {
    let raw = '';
    req.on('data', (c) => (raw += c));
    req.on('end', () => {
      let body = {};
      try { body = JSON.parse(raw || '{}'); } catch {}
      const model = body.model || 'gpt-4o-mini';
      const stream = !!body.stream;
      const text = fakeText();
      const usage = { prompt_tokens: 180 + Math.floor(Math.random() * 900), completion_tokens: Math.round(text.length / 4), total_tokens: 0 };
      usage.total_tokens = usage.prompt_tokens + usage.completion_tokens;

      if (!stream) {
        setTimeout(() => {
          res.writeHead(200, { 'content-type': 'application/json' });
          res.end(JSON.stringify({ id: body.id || 'chatcmpl-demo', object: 'chat.completion', created: Math.floor(Date.now() / 1000), model, choices: [{ index: 0, message: { role: 'assistant', content: text }, finish_reason: 'stop' }], usage }));
        }, 80 + Math.random() * 250);
        return;
      }
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache', connection: 'keep-alive', 'x-accel-buffering': 'no' });
      const tokens = text.split(' ');
      let i = 0;
      const t0 = 90 + Math.random() * 140; // simulated TTFT
      setTimeout(function next() {
        if (i < tokens.length) {
          sseChunk(res, model, { role: i === 0 ? 'assistant' : undefined, content: (i === 0 ? '' : ' ') + tokens[i] });
          i++;
          setTimeout(next, 4 + Math.random() * 26);
        } else {
          sseChunk(res, model, {}, 'stop', usage);
          res.write('data: [DONE]\n\n');
          res.end();
        }
      }, t0);
    });
    return;
  }
  res.writeHead(404, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ error: { message: 'not found', type: 'invalid_request_error' } }));
});

server.listen(9091, '127.0.0.1', () => console.log('mock upstream on 127.0.0.1:9091'));
