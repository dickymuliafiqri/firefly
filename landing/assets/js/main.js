/* Landing page interactions: provider + feature tabs, copy chips. */
(function () {
  'use strict';

  var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* ---- provider data (grounded in internal/adapter/*) ---- */
  var PROVIDERS = {
    openai: {
      name: 'OpenAI',
      proto: 'OpenAI / vLLM',
      blurb: 'Pass-through for OpenAI, Azure OpenAI, vLLM and Ollama — public model names rewritten to upstream-private ones, credentials injected server-side.',
      facts: ['OpenAI · Azure OpenAI · vLLM · Ollama', 'Transparent SSE relay, gzip-aware', 'Keyring secret injection'],
      wire: [
        ['POST /v1/chat/completions', 'POST /v1/chat/completions'],
        ['model: gpt-4o-mini', 'model: <upstream private name>'],
        ['auth: sk-gw-… (tenant)', 'auth: <keyring secret>']
      ]
    },
    anthropic: {
      name: 'Anthropic',
      proto: 'Anthropic Messages',
      blurb: 'Translates the OpenAI wire format to the Anthropic Messages API — requests, replies and every SSE chunk re-framed on the fly.',
      facts: ['Bi-directional schema translation', 'Streaming + non-streaming payloads', 'System prompts and SSE events re-framed'],
      wire: [
        ['POST /v1/chat/completions', 'POST /v1/messages'],
        ['messages: [{role, content}]', 'system + messages blocks'],
        ['OpenAI SSE chunk', 'Anthropic SSE event']
      ]
    },
    antigravity: {
      name: 'Antigravity',
      proto: 'Google Cloud Code',
      blurb: 'Speaks Google Cloud Code\u2019s internal protocol directly, with an authentic companion project binding — no proxy in between.',
      facts: ['Gemini 3.8 / 3.7 Flash · Gemini 2.5 Pro', 'Claude Sonnet tiers via Cloud Code', 'Authentic companion project ID'],
      wire: [
        ['POST /v1/chat/completions', 'Cloud Code internal JSON'],
        ['OpenAI tool schema', 'Google tool declaration'],
        ['OpenAI SSE chunk', 'Google SSE envelope']
      ]
    },
    cline: {
      name: 'Cline',
      proto: 'Cline API',
      blurb: 'Proxies the Cline API with the client identity headers it expects, unwraps the response envelope and relays the stream untouched.',
      facts: ['api.cline.bot endpoints', 'HTTP-Referer / X-Title identity', 'Envelope unwrapping + SSE relay'],
      wire: [
        ['POST /v1/chat/completions', 'POST /v1/chat/completions'],
        ['—', 'HTTP-Referer, X-Title headers'],
        ['Cline envelope', 'OpenAI-compatible result']
      ]
    },
    codebuddy: {
      name: 'CodeBuddy',
      proto: 'CodeBuddy CN / Intl',
      blurb: 'Runs the full RFC 8628 device-authorization flow for CodeBuddy China and International, then forwards completions with the payload rewrites handled for you.',
      facts: ['China + International endpoints', 'RFC 8628 device grants', 'Completion streaming relay'],
      wire: [
        ['—', 'device authorization grant'],
        ['POST /v1/chat/completions', 'CodeBuddy completion payload'],
        ['OpenAI SSE chunk', 'CodeBuddy stream frame']
      ]
    },
    grok: {
      name: 'Grok',
      proto: 'xAI Responses',
      blurb: 'Speaks the xAI Responses API both ways \u2014 reasoning output included \u2014 with harvested account tokens pooled across the keyring.',
      facts: ['cli-chat-proxy.grok.com', 'OpenAI \u2194 Responses translation', 'Harvested token pooling'],
      wire: [
        ['POST /v1/chat/completions', 'POST /v1/responses'],
        ['messages: [{role, content}]', 'input: [{type, content}]'],
        ['OpenAI SSE chunk', 'Responses SSE event']
      ]
    },
    opencode: {
      name: 'OpenCode',
      proto: 'OpenCode Zen',
      blurb: 'Routes to the OpenCode Zen gateways \u2014 keyless Free tier or Go subscription keys \u2014 with Cline tool-calling preserved end to end.',
      facts: ['Keyless free tier + Go keys', 'Dual-route dispatch', 'Deterministic session isolation'],
      wire: [
        ['POST /v1/chat/completions', '/responses <em>or</em> /chat/completions'],
        ['tools: [{function}]', 'sanitized tool schema'],
        ['tool_calls finish', '0-based delta stream']
      ]
    }
  };

  var pPanel = document.getElementById('panel-provider');
  var elName = document.getElementById('pv-name');
  var elBlurb = document.getElementById('pv-blurb');
  var elFacts = document.getElementById('pv-facts');
  var elWire = document.getElementById('pv-wire');
  var elProto = document.getElementById('pv-proto');

  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function renderProvider(key) {
    var p = PROVIDERS[key];
    if (!p || !pPanel) return;
    elName.textContent = p.name;
    elBlurb.textContent = p.blurb;
    elProto.textContent = p.proto;
    elFacts.innerHTML = p.facts.map(function (f) { return '<li>' + esc(f) + '</li>'; }).join('');
    elWire.innerHTML = p.wire.map(function (row) {
      return '<span class="w-in">' + row[0] + '</span><span class="w-arrow">→</span><span class="w-out">' + row[1] + '</span>';
    }).join('\n');
  }

  /* ---- tab strips: roving tabindex, arrow keys, click ---- */
  function tabStrip(list, activate) {
    function select(btn, focus) {
      list.forEach(function (t) {
        var on = t === btn;
        t.setAttribute('aria-selected', String(on));
        t.tabIndex = on ? 0 : -1;
      });
      if (focus) btn.focus();
      activate(btn);
    }
    list.forEach(function (t, i) {
      t.addEventListener('click', function () { select(t, false); });
      t.addEventListener('keydown', function (e) {
        var step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        var to = step ? list[(i + step + list.length) % list.length]
          : e.key === 'Home' ? list[0]
          : e.key === 'End' ? list[list.length - 1]
          : null;
        if (!to) return;
        e.preventDefault();
        select(to, true);
      });
    });
    return select;
  }

  function each(root, sel) {
    return Array.prototype.slice.call(root.querySelectorAll(sel));
  }

  /* providers — one shared panel, redrawn on switch */
  var pTabs = each(document, '.tabs [role="tab"][data-provider]');
  if (pTabs.length) {
    tabStrip(pTabs, function (btn) {
      if (reduced) { renderProvider(btn.dataset.provider); return; }
      pPanel.classList.add('is-swapping');
      setTimeout(function () {
        renderProvider(btn.dataset.provider);
        pPanel.classList.remove('is-swapping');
      }, 160);
    });
    pTabs[0].tabIndex = 0;
    renderProvider(pTabs[0].dataset.provider);
  }

  /* features — one panel per tab, hidden toggled */
  var fTabs = each(document, '[role="tab"][aria-controls^="fp-"]');
  if (fTabs.length) {
    tabStrip(fTabs, function (btn) {
      fTabs.forEach(function (t) {
        var panelEl = document.getElementById(t.getAttribute('aria-controls'));
        if (panelEl) panelEl.hidden = t !== btn;
      });
    });
    fTabs[0].tabIndex = 0;
  }

  /* ---- feature diagrams ---- */

  /* Load balancing: the serving key and its in-flight count advance one step at a
   * time. The step is read back from CSS (--dg-step) so the highlight cannot drift
   * away from the packet animation riding the same step. */
  each(document, '.dg-tree[data-keys]').forEach(function (tree) {
    var rows = each(tree, '.dg-row');
    if (reduced || rows.length < 2) return;
    var stepMs = (parseFloat(getComputedStyle(tree).getPropertyValue('--dg-step')) || 1.9) * 1000;
    var at = 0;
    setInterval(function () {
      if (tree.offsetParent === null) return; /* the plate lives on another tab */
      rows[at].removeAttribute('data-active');
      at = (at + 1) % rows.length;
      rows[at].setAttribute('data-active', '');
      rows.forEach(function (row, n) {
        var count = row.querySelector('.dg-inflight');
        if (count) count.textContent = n === at ? '1' : '0';
      });
    }, stepMs);
  });

  /* Observability: Prometheus counters only ever climb, so these do too — and they
   * hold still while the plate is off screen. */
  each(document, '[data-tick]').forEach(function (el) {
    var n = parseInt(el.getAttribute('data-tick'), 10);
    if (reduced || !isFinite(n)) return;
    setInterval(function () {
      if (el.offsetParent === null) return;
      n += 1 + Math.floor(Math.random() * 3);
      el.textContent = n.toLocaleString('en-US');
    }, 1500);
  });

  /* ---- copy chips ---- */
  document.querySelectorAll('[data-copy]').forEach(function (chip) {
    chip.addEventListener('click', function () {
      var text = chip.getAttribute('data-copy');
      var hint = chip.querySelector('.install-hint');
      var done = function () {
        if (!hint) return;
        hint.textContent = 'copied';
        chip.classList.add('copied');
        setTimeout(function () {
          hint.textContent = hint.dataset.label || 'copy';
          chip.classList.remove('copied');
        }, 1800);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, done);
      } else {
        done();
      }
    });
  });
})();
