/* Night-forest parallax + firefly flight paths.
 *
 * Layers opt in with data-parallax="scrollAmp,mouseAmp":
 *   scrollAmp — px of drift as the layer's section crosses the viewport
 *   mouseAmp  — px of drift at full pointer offset from the centre
 * Trails are inline SVGs marked data-trail: their first <path> is the flight
 * path and three fireflies are animated along it.
 * Under prefers-reduced-motion nothing moves; trail fireflies are parked.
 */
(function () {
  'use strict';

  var SVG_NS = 'http://www.w3.org/2000/svg';
  var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function clamp(v, a, b) { return v < a ? a : v > b ? b : v; }

  /* ---------------- flight trails ---------------- */

  var trails = [];

  Array.prototype.forEach.call(document.querySelectorAll('[data-trail]'), function (svg) {
    var path = svg.querySelector('path');
    if (!path) return;
    var len = 0;
    try { len = path.getTotalLength(); } catch (e) { return; }
    if (!len) return;

    function dot(r, base) {
      var c = document.createElementNS(SVG_NS, 'circle');
      c.setAttribute('r', r);
      c.setAttribute('cx', -20);
      c.setAttribute('cy', -20);
      c.setAttribute('fill', '#f0ffb4');
      c.setAttribute('opacity', base);
      svg.appendChild(c);
      return { node: c, base: base };
    }

    var halos = [dot(7.5, 0.07), dot(6, 0.05), dot(5, 0.03)];
    var cores = [dot(2.3, 0.95), dot(1.9, 0.6), dot(1.5, 0.32)];
    var lags = [0, 0.055, 0.11];
    var freqs = [1.3, 1.05, 0.85];
    var phases = [0, 2.1, 4.2];

    trails.push({
      svg: svg,
      path: path,
      len: len,
      halos: halos,
      cores: cores,
      lags: lags,
      freqs: freqs,
      phases: phases,
      speed: 0.05 + Math.random() * 0.03,
      start: Math.random()
    });
  });

  function drawDots(tr, head, t, flicker) {
    for (var i = 0; i < 3; i++) {
      var prog = head - tr.lags[i];
      if (prog < 0) prog += 1;
      var pt = tr.path.getPointAtLength(prog * tr.len);
      var a = 1;
      if (flicker) {
        var s = 0.5 + 0.5 * Math.sin(t * tr.freqs[i] + tr.phases[i]);
        a = 0.35 + 0.65 * Math.pow(s, 1.6);
      }
      tr.cores[i].node.setAttribute('cx', pt.x);
      tr.cores[i].node.setAttribute('cy', pt.y);
      tr.cores[i].node.setAttribute('opacity', (tr.cores[i].base * a).toFixed(3));
      tr.halos[i].node.setAttribute('cx', pt.x);
      tr.halos[i].node.setAttribute('cy', pt.y);
      tr.halos[i].node.setAttribute('opacity', (tr.halos[i].base * a).toFixed(3));
    }
  }

  if (reduced) {
    for (var ti = 0; ti < trails.length; ti++) drawDots(trails[ti], 0.62, 0, false);
    return;
  }

  /* ---------------- parallax layers ---------------- */

  var layers = [];

  Array.prototype.forEach.call(document.querySelectorAll('[data-parallax]'), function (el) {
    var parts = (el.getAttribute('data-parallax') || '').split(',');
    var sAmp = parseFloat(parts[0]) || 0;
    var mAmp = parseFloat(parts[1]) || 0;
    if (!sAmp && !mAmp) return;
    layers.push({
      el: el,
      scope: el.closest('section, footer') || el.parentElement || el,
      sAmp: sAmp,
      mAmp: mAmp,
      /* The footer band is pinned to the page bottom: any vertical offset would
       * either stretch the document (when the band is still below the fold) or
       * lift the ground line off the bottom edge. Those layers keep their place
       * and only take the horizontal pointer drift. */
      pinnedY: el.closest('footer') !== null
    });
  });

  var scopes = [];
  layers.forEach(function (L) {
    if (scopes.indexOf(L.scope) === -1) scopes.push(L.scope);
  });

  var mx = 0, my = 0, tx = 0, ty = 0;
  var running = true;
  var vw = window.innerWidth, vh = window.innerHeight;

  /* ---------------- pinned scene ----------------
   * Scene elements leave the scroll flow: one clamped offset holds them on screen
   * while the content rises past them.
   *   G      — the ground line's distance above the viewport bottom
   *   W      — settle window; the scene catches down to the base over 2.5·G of scroll
   *   Omax   — the hard stop: the ground line may not pass the end of <main>
   *   o(s)   = min(s + G·clamp(s / W, 0, 1), Omax)   settle, then pin 1:1 with the scroll
   * Every value is read from the live layout each frame: the band's document bottom
   * moves when fonts land, <main>'s end moves when the feature tabs open, and the
   * viewport height moves on resize. Nothing here is cached across frames. */

  var sceneEls = [];
  Array.prototype.forEach.call(document.querySelectorAll('[data-scene], [data-scene-grow]'), function (el) {
    sceneEls.push({
      el: el,
      mAmp: parseFloat(el.getAttribute('data-scene-mouse')) || 0,
      cap: parseFloat(el.getAttribute('data-scene-grow')) || 0
    });
  });

  var paints = [];
  Array.prototype.forEach.call(document.querySelectorAll(
    '.forest-paint--night, .forest-paint--soil, .forest-paint--front-night, .forest-paint--front-soil'
  ), function (el) {
    /* Ruling 20: test the bare 'soil', not '--soil'. `forest-paint--front-soil`'s double
       dash sits before `front`, so the group name ends `-soil` with a single dash and the
       '--soil' test reads false — which drove the front group with the *night* ramp
       (fully opaque at rest, inverted crossfade, invisible at the release). */
    paints.push({ el: el, soil: el.className.indexOf('soil') !== -1 });
  });
  var skyEls = Array.prototype.slice.call(document.querySelectorAll('[data-scene-dims="soil"]'));
  var lifeEls = Array.prototype.slice.call(document.querySelectorAll('[data-scene]'));

  var band = document.querySelector('.hero .forest');
  var mainEl = document.querySelector('main');
  var small = window.matchMedia('(max-width: 960px)');
  var sceneReady = !!(band && mainEl && sceneEls.length);

  function applyScene(s, bandRect, mainRect) {
    var on = sceneReady && !small.matches;
    var i;
    if (!on) {
      /* Parked (≤960px): the pointer drift is all the scene keeps, and no soil ramp may
         survive a resize across the gate. Every opacity cleared here must cover every set the
         `on` branch writes: sceneEls (transform + opacity), paints, and skyEls / lifeEls — the
         last two only because each is a filtered subset of sceneEls. A future dims-only element
         would not be, and would strand its inline opacity across the gate. */
      for (i = 0; i < sceneEls.length; i++) {
        var P = sceneEls[i];
        P.el.style.transform = 'translate3d(' + (mx * P.mAmp).toFixed(2) + 'px,' + (my * P.mAmp * 0.35).toFixed(2) + 'px,0)';
        P.el.style.opacity = '';
      }
      for (i = 0; i < paints.length; i++) paints[i].el.style.opacity = '';
      return;
    }
    var bandDocBottom = bandRect.bottom + s;
    var G = Math.max(0, vh - bandDocBottom);
    var W = Math.max(1, 2.5 * G);
    var Omax = Math.max(0, mainRect.bottom + s - bandDocBottom);
    var o = Math.min(s + G * clamp(s / W, 0, 1), Omax);
    var release = Math.max(W, Omax - G);
    var g = clamp((s - W) / Math.max(1, release - W), 0, 1);
    /* The band may never cover more than half the viewport, which binds on short
       and ultrawide windows where 17vw is already a large share of the height.
       The guard caps growth; it never shrinks the forest under the reader. */
    var bandCap = Math.max(1, (0.5 * vh) / (bandRect.height || 1));
    for (i = 0; i < sceneEls.length; i++) {
      var L = sceneEls[i];
      var x = mx * L.mAmp;
      var y = o + my * L.mAmp * 0.35;
      var t = 'translate3d(' + x.toFixed(2) + 'px,' + y.toFixed(2) + 'px,0)';
      if (L.cap) {
        var cap = band.contains(L.el) ? Math.min(L.cap, bandCap) : L.cap;
        t += ' scale(' + (1 + (cap - 1) * g).toFixed(4) + ')';
      }
      L.el.style.transform = t;
    }

    /* Beat 3: the palette turns to earth over the last 60vh of <main>, the sky
       withdraws with it, and the whole scene is absorbed over the first 30vh of
       the release so it cannot double up with the footer's own soil band. */
    var soil = clamp((s - (release - 0.6 * vh)) / (0.6 * vh), 0, 1);
    var fade = 1 - clamp((s - release) / (0.3 * vh), 0, 1);
    for (i = 0; i < paints.length; i++) {
      paints[i].el.style.opacity = ((paints[i].soil ? soil : 1 - soil) * fade).toFixed(3);
    }
    for (i = 0; i < skyEls.length; i++) skyEls[i].style.opacity = ((1 - soil) * fade).toFixed(3);
    for (i = 0; i < lifeEls.length; i++) lifeEls[i].style.opacity = fade.toFixed(3);
  }

  window.addEventListener('resize', function () {
    vw = window.innerWidth;
    vh = window.innerHeight;
  }, { passive: true });

  window.addEventListener('pointermove', function (e) {
    if (e.pointerType === 'touch') return;
    tx = (e.clientX / vw - 0.5) * 2;
    ty = (e.clientY / vh - 0.5) * 2;
  }, { passive: true });

  function frame(now) {
    if (!running) return;
    var t = now / 1000;
    var s = window.pageYOffset;
    mx += (tx - mx) * 0.055;
    my += (ty - my) * 0.055;

    var rects = [];
    var i;
    for (i = 0; i < scopes.length; i++) rects.push(scopes[i].getBoundingClientRect());

    var visible = [];
    for (i = 0; i < trails.length; i++) {
      var r = trails[i].svg.getBoundingClientRect();
      visible.push(r.bottom > -80 && r.top < vh + 80 && r.width > 0);
    }

    if (sceneReady) applyScene(s, band.getBoundingClientRect(), mainEl.getBoundingClientRect());

    for (i = 0; i < layers.length; i++) {
      var L = layers[i];
      var sr = rects[scopes.indexOf(L.scope)];
      var p = clamp((vh - sr.top) / (vh + sr.height), 0, 1);
      var y = L.pinnedY ? 0 : -(p - 0.5) * L.sAmp + my * L.mAmp * 0.35;
      var x = mx * L.mAmp;
      L.el.style.transform = 'translate3d(' + x.toFixed(2) + 'px,' + y.toFixed(2) + 'px,0)';
    }

    for (i = 0; i < trails.length; i++) {
      if (!visible[i]) continue;
      var tr = trails[i];
      var head = (tr.start + t * tr.speed) % 1;
      drawDots(tr, head, t, true);
    }

    requestAnimationFrame(frame);
  }

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      running = false;
    } else if (!running) {
      running = true;
      requestAnimationFrame(frame);
    }
  });

  requestAnimationFrame(frame);
})();
