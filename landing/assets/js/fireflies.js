/* Firefly swarm — a quiet night-forest canvas behind the whole page.
   Each dot is a drifting firefly: a warm core, a lime halo, and a
   flicker all its own. Nearby fireflies draw faint constellation
   lines, echoing the gateway dashboard. */
(function () {
  'use strict';

  var canvas = document.getElementById('fireflies');
  if (!canvas) return;

  var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var ctx = canvas.getContext('2d');
  var dpr = Math.min(window.devicePixelRatio || 1, 2);
  var W = 0, H = 0;
  var flies = [];
  var running = true;
  var scrollY = 0;

  function rand(min, max) { return min + Math.random() * (max - min); }

  function makeFly(w, h, spread) {
    return {
      x: rand(0, w),
      y: rand(h * (1 - spread), h),
      vx: rand(-0.10, 0.10),
      vy: rand(-0.07, 0.07),
      size: rand(0.9, 2.3),
      depth: rand(0.35, 1),
      phase: rand(0, Math.PI * 2),
      freq: rand(0.4, 1.4),
      wander: rand(0, Math.PI * 2)
    };
  }

  function resize() {
    W = window.innerWidth;
    H = window.innerHeight;
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    seed();
    if (reduced) drawFrame(0);
  }

  function seed() {
    var count = Math.round(Math.min(110, Math.max(30, (W * H) / 26000)));
    flies = [];
    for (var i = 0; i < count; i++) {
      // spread 0.55 → most of the swarm lives in the lower forest band,
      // a few drift up into the sky like sparks
      flies.push(makeFly(W, H, i % 7 === 0 ? 1 : 0.55));
    }
  }

  function flicker(t, fly) {
    var a = 0.5 + 0.5 * Math.sin(t * fly.freq + fly.phase);
    // occasional dark blinks: sharpen the low end of the sine
    return 0.12 + 0.88 * Math.pow(a, 1.8);
  }

  function drawFrame(t) {
    ctx.clearRect(0, 0, W, H);
    ctx.globalCompositeOperation = 'lighter';

    var i, j, f, alpha, x, y;

    for (i = 0; i < flies.length; i++) {
      f = flies[i];
      x = f.x;
      y = f.y + scrollY * (f.depth - 0.5) * 0.08;
      if (y < -20 || y > H + 20) continue;
      alpha = flicker(t, f) * (0.45 + 0.55 * f.depth);

      var halo = ctx.createRadialGradient(x, y, 0, x, y, f.size * 7);
      halo.addColorStop(0, 'rgba(190, 242, 100, ' + (0.30 * alpha).toFixed(3) + ')');
      halo.addColorStop(1, 'rgba(190, 242, 100, 0)');
      ctx.fillStyle = halo;
      ctx.beginPath();
      ctx.arc(x, y, f.size * 7, 0, Math.PI * 2);
      ctx.fill();

      ctx.fillStyle = 'rgba(240, 255, 180, ' + (0.85 * alpha).toFixed(3) + ')';
      ctx.beginPath();
      ctx.arc(x, y, f.size, 0, Math.PI * 2);
      ctx.fill();
    }

    // constellation lines between close neighbours
    for (i = 0; i < flies.length; i++) {
      for (j = i + 1; j < flies.length; j++) {
        var a = flies[i], b = flies[j];
        var dx = a.x - b.x;
        var dy = (a.y + scrollY * (a.depth - 0.5) * 0.08) - (b.y + scrollY * (b.depth - 0.5) * 0.08);
        var d2 = dx * dx + dy * dy;
        if (d2 < 16900) { // < 130px
          var o = (1 - Math.sqrt(d2) / 130) * 0.085;
          ctx.strokeStyle = 'rgba(190, 242, 100, ' + o.toFixed(3) + ')';
          ctx.lineWidth = 1;
          ctx.beginPath();
          ctx.moveTo(a.x, a.y + scrollY * (a.depth - 0.5) * 0.08);
          ctx.lineTo(b.x, b.y + scrollY * (b.depth - 0.5) * 0.08);
          ctx.stroke();
        }
      }
    }

    ctx.globalCompositeOperation = 'source-over';
  }

  var last = 0;
  function loop(now) {
    if (!running) return;
    var dt = Math.min(48, now - last);
    last = now;
    var t = now / 1000;

    for (var i = 0; i < flies.length; i++) {
      var f = flies[i];
      f.wander += (Math.random() - 0.5) * 0.08;
      f.vx += Math.cos(f.wander) * 0.004;
      f.vy += Math.sin(f.wander * 0.9) * 0.003;
      f.vx = Math.max(-0.22, Math.min(0.22, f.vx));
      f.vy = Math.max(-0.16, Math.min(0.16, f.vy));
      f.x += f.vx * (dt / 16);
      f.y += f.vy * (dt / 16);
      if (f.x < -30) f.x = W + 30;
      if (f.x > W + 30) f.x = -30;
      if (f.y < H * 0.18) { f.vy = Math.abs(f.vy); f.y = H * 0.18 + 2; }
      if (f.y > H + 30) f.y = H * 0.3;
    }

    drawFrame(t);
    requestAnimationFrame(loop);
  }

  window.addEventListener('resize', resize, { passive: true });
  window.addEventListener('scroll', function () { scrollY = window.scrollY; }, { passive: true });
  document.addEventListener('visibilitychange', function () {
    if (reduced) return;
    if (document.hidden) {
      running = false;
    } else if (!running) {
      running = true;
      last = performance.now();
      requestAnimationFrame(loop);
    }
  });

  resize();
  if (!reduced) requestAnimationFrame(loop);
})();
