// Generate demo traffic through the gateway so dashboard charts look alive.
const KEY = 'sk-gw-demo-dev-0001';
const MODELS = ['smart-router', 'gpt-4o-mini', 'deepseek-chat', 'gpt-4.1-mini', 'llama-3.3-70b-instruct', 'smart-router'];
const BASE = 'http://127.0.0.1:8088/v1/chat/completions';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function one(model, stream) {
  try {
    const res = await fetch(BASE, {
      method: 'POST',
      headers: { authorization: `Bearer ${KEY}`, 'content-type': 'application/json' },
      body: JSON.stringify({
        model,
        stream,
        messages: [{ role: 'user', content: 'Describe a forest at night in one sentence.' }],
      }),
    });
    if (stream && res.body) {
      const reader = res.body.getReader();
      for (;;) {
        const { done } = await reader.read();
        if (done) break;
      }
    } else {
      await res.text();
    }
    return res.status;
  } catch {
    return 0;
  }
}

(async () => {
  for (let i = 0; i < 48; i++) {
    const model = MODELS[i % MODELS.length];
    one(model, i % 3 !== 0);
    await sleep(400 + Math.random() * 900);
  }
  await sleep(15000); // let stragglers finish
  console.log('traffic done');
})();
