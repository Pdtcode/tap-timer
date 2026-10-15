(() => {
  'use strict';

  // ---------------------------------------------------------------------------
  // Storage (localStorage can throw in private mode / when blocked)
  // ---------------------------------------------------------------------------
  const store = {
    get(key, fallback) {
      try {
        const v = localStorage.getItem(key);
        return v == null ? fallback : JSON.parse(v);
      } catch { return fallback; }
    },
    set(key, value) {
      try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* ignore */ }
    },
  };
  const session = {
    get(key) { try { return sessionStorage.getItem(key); } catch { return null; } },
    set(key, v) { try { sessionStorage.setItem(key, v); } catch { /* ignore */ } },
  };

  // ---------------------------------------------------------------------------
  // Translations: the server puts this page's strings (web/i18n, "js." keys)
  // in #tt-i18n. tr("hint.start") or tr("party.player", { n: 2 }); trn picks
  // key_one / key_other by count. PREFIX is the language's URL prefix ("" or "/es").
  // ---------------------------------------------------------------------------
  const I18N = (() => {
    try { return JSON.parse(document.getElementById('tt-i18n').textContent) || {}; } catch { return {}; }
  })();
  const tr = (key, vars) => {
    let s = I18N[key] ?? key;
    if (vars) for (const k in vars) s = s.split(`{${k}}`).join(String(vars[k]));
    return s;
  };
  const trn = (key, n, vars) => tr(`${key}_${n === 1 ? 'one' : 'other'}`, { n, ...vars });
  const PREFIX = document.documentElement.dataset.prefix || '';

  const TARGETS = [3, 5, 10, 15, 30];
  const MAX_SECONDS = 99.99;
  const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // ---------------------------------------------------------------------------
  // Seven-segment display (inline SVG), styled after the app: upright pill
  // segments, dim "ghost" segments, and the decimal point as its own round cell.
  // ---------------------------------------------------------------------------
  const SEG = 12; // segment thickness (viewBox units)
  const SEGMENTS = {
    a: [16, 2, 32, SEG], g: [16, 48, 32, SEG], d: [16, 94, 32, SEG], // x, y, w, h
    f: [2, 15, SEG, 32], b: [50, 15, SEG, 32],
    e: [2, 61, SEG, 32], c: [50, 61, SEG, 32],
  };
  const GLYPHS = {
    0: 'abcdef', 1: 'bc', 2: 'abdeg', 3: 'abcdg', 4: 'bcfg', 5: 'acdfg', 6: 'acdefg',
    7: 'abc', 8: 'abcdefg', 9: 'abcdfg', '-': 'g', ' ': '',
  };
  const SVG_NS = 'http://www.w3.org/2000/svg';

  function svgEl(name, attrs) {
    const el = document.createElementNS(SVG_NS, name);
    for (const k in attrs) el.setAttribute(k, attrs[k]);
    return el;
  }

  /**
   * @param {HTMLElement} el
   * @param {string} pattern  "#" per digit, "." for the decimal point, e.g. "##.##"
   * set(text) right-aligns text into the digits; leading blank digits collapse,
   * so "5.03" reads as three digits like the app and grows to four at 10s.
   */
  function createDisplay(el, pattern) {
    el.textContent = '';
    const digits = [];
    let dot = null;
    for (const ch of pattern) {
      if (ch === '.') {
        const svg = svgEl('svg', { viewBox: '0 0 22 108', class: 'digit digit--dot', focusable: 'false' });
        dot = svgEl('circle', { cx: 11, cy: 100, r: 7, class: 'seg' });
        svg.appendChild(dot);
        el.appendChild(svg);
        continue;
      }
      const svg = svgEl('svg', { viewBox: '0 0 64 108', class: 'digit', focusable: 'false' });
      const segs = {};
      for (const name in SEGMENTS) {
        const [x, y, w, h] = SEGMENTS[name];
        segs[name] = svgEl('rect', { x, y, width: w, height: h, rx: SEG / 2, class: 'seg' });
        svg.appendChild(segs[name]);
      }
      el.appendChild(svg);
      digits.push({ svg, segs });
    }
    return {
      set(text) {
        const chars = [...text.replace('.', '')];
        while (chars.length < digits.length) chars.unshift(' ');
        const offset = chars.length - digits.length;
        let leading = true;
        digits.forEach((d, i) => {
          const ch = chars[offset + i];
          leading = leading && ch === ' ' && i < digits.length - 1;
          d.svg.classList.toggle('digit--hidden', leading);
          const lit = GLYPHS[ch] ?? '';
          for (const name in d.segs) d.segs[name].classList.toggle('on', lit.includes(name));
        });
        if (dot) dot.classList.toggle('on', text.includes('.'));
      },
    };
  }

  // ---------------------------------------------------------------------------
  // Ambient particles: one field of slow drifting motes covering the viewport,
  // like the app's screen, plus a burst on every tap. It's drawn on a fixed
  // full-page layer behind all content, and again inside each opaque panel
  // (cards, tap zones) at the same screen positions, so a mote drifting out of
  // the tap zone carries on across the page instead of vanishing at the edge.
  // ---------------------------------------------------------------------------
  const MOTE_SPEED = 2.25; // drift speed of the ambient motes (1 = the original slow drift)

  class Particles {
    /** @param {{density?:number, max?:number}} opts  one mote per `density` px², capped at `max` */
    constructor(host, { density = 11000, max = 100 } = {}) {
      this.density = density;
      this.max = max;
      this.layers = [];
      this.ambient = [];
      this.sparks = [];
      this.raf = 0;
      this.w = 0;
      this.h = 0;
      this.base = this.addLayer(host);
      this.resize();
      window.addEventListener('resize', () => this.resize());
      // Paused (reduced motion), panels still need redrawing as the page scrolls under the fixed field.
      window.addEventListener('scroll', () => { if (!this.running()) this.draw(); }, { passive: true });
      document.addEventListener('visibilitychange', () => this.kick());
      document.addEventListener('tt:theme', () => { this.layers.forEach((l) => this.readColor(l)); this.draw(); });
    }

    // Show the field inside `host`. The canvas sits under the host's content
    // but above its background.
    addLayer(host) {
      const canvas = document.createElement('canvas');
      canvas.className = 'particles';
      canvas.setAttribute('aria-hidden', 'true');
      if (getComputedStyle(host).position === 'static') host.style.position = 'relative';
      host.style.isolation = 'isolate';
      host.prepend(canvas);
      const layer = { host, canvas, ctx: canvas.getContext('2d'), w: 0, h: 0 };
      this.readColor(layer);
      this.layers.push(layer);
      new ResizeObserver(() => {
        this.sizeLayer(layer);
        if (layer === this.base) this.resize(); // the field follows the page layer's size
        else this.draw();
      }).observe(host);
      return layer;
    }

    readColor(layer) {
      layer.color = getComputedStyle(layer.host).getPropertyValue('--seg-on').trim() || '#30fc60';
    }

    sizeLayer(layer) {
      const { width, height } = layer.host.getBoundingClientRect();
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      layer.w = width;
      layer.h = height;
      layer.canvas.width = Math.round(width * dpr);
      layer.canvas.height = Math.round(height * dpr);
      layer.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }

    resize() {
      // The page layer's own size (it covers the largest viewport, see .ambient).
      const box = this.base.host.getBoundingClientRect();
      const w = box.width || window.innerWidth;
      const h = Math.max(box.height, window.innerHeight);
      // Stretch existing motes over the new area, so a taller or wider screen
      // (a phone toolbar hiding, a rotation) has no empty strip.
      if (this.w && this.h && (w !== this.w || h !== this.h)) {
        for (const p of this.ambient) {
          p.bx *= w / this.w;
          p.x *= w / this.w;
          p.y *= h / this.h;
        }
      }
      this.w = w;
      this.h = h;
      const target = Math.round(Math.min(this.max, (this.w * this.h) / this.density));
      while (this.ambient.length < target) this.ambient.push(this.mote(true));
      this.ambient.length = target;
      this.draw();
      this.kick();
    }

    mote(anywhere) {
      const soft = Math.random() < 0.35; // out-of-focus bokeh dots
      const x = Math.random() * this.w;
      return {
        x,
        bx: x, // centre line the mote sways around
        y: anywhere ? Math.random() * this.h : this.h + 8,
        r: soft ? 2.5 + Math.random() * 3 : 0.8 + Math.random() * 1.8,
        vx: (Math.random() - 0.5) * 6 * MOTE_SPEED,
        vy: -(2 + Math.random() * 8) * MOTE_SPEED,
        a: soft ? 0.08 + Math.random() * 0.12 : 0.18 + Math.random() * 0.4,
        tw: Math.random() * Math.PI * 2,
        // Gentle side-to-side wave as it rises: each mote has its own width,
        // pace and starting point, so they don't sway in step.
        sway: 18 + Math.random() * 22, // px either side
        swaySpeed: 0.3 + Math.random() * 0.4, // radians/s: one full wave every ~9-21s, independent of MOTE_SPEED
        phase: Math.random() * Math.PI * 2,
        soft,
      };
    }

    // Burst from (x, y) in viewport coordinates. Particles fly out (past the
    // tap zone's edge if they get that far), slow down and linger as extra
    // ambient motes before fading.
    burst(x, y, count = 28) {
      if (reducedMotion || !this.w) return;
      for (let i = 0; i < count; i++) {
        const angle = Math.random() * Math.PI * 2;
        const speed = 40 + Math.random() * 240;
        this.sparks.push({
          x, y,
          vx: Math.cos(angle) * speed,
          vy: Math.sin(angle) * speed,
          r: 0.8 + Math.random() * 2.4,
          life: 0,
          max: 1.4 + Math.random() * 2.2,
          a: 0.45 + Math.random() * 0.5,
        });
      }
      if (this.sparks.length > 240) this.sparks.splice(0, this.sparks.length - 240);
      this.kick();
    }

    running() { return !document.hidden && !reducedMotion; }

    kick() {
      if (this.raf || !this.running()) return;
      this.last = performance.now();
      this.raf = requestAnimationFrame((t) => this.frame(t));
    }

    frame(t) {
      this.raf = 0;
      const dt = Math.min(0.05, (t - this.last) / 1000);
      this.last = t;
      for (const p of this.ambient) {
        p.bx += p.vx * dt;
        p.y += p.vy * dt;
        p.tw += dt * 1.3;
        p.phase += p.swaySpeed * dt;
        if (p.y < -8) Object.assign(p, this.mote(false));
        if (p.bx < -8) p.bx = this.w + 8;
        else if (p.bx > this.w + 8) p.bx = -8;
        p.x = p.bx + Math.sin(p.phase) * p.sway;
      }
      const drag = Math.exp(-2.6 * dt);
      this.sparks = this.sparks.filter((s) => {
        s.life += dt;
        s.vx *= drag;
        s.vy = s.vy * drag - 6 * dt; // settle into a slow upward drift
        s.x += s.vx * dt;
        s.y += s.vy * dt;
        return s.life < s.max;
      });
      this.draw();
      if (this.running()) this.raf = requestAnimationFrame((ts) => this.frame(ts));
    }

    draw() {
      for (const l of this.layers) {
        if (!l.w || !l.h) continue; // hidden (e.g. a Party screen not on show)
        const r = l === this.base ? { left: 0, top: 0, bottom: this.h } : l.host.getBoundingClientRect();
        if (r.bottom < 0 || r.top > this.h) continue; // scrolled out of view
        const { ctx } = l;
        ctx.clearRect(0, 0, l.w, l.h);
        ctx.fillStyle = l.color;
        for (const p of this.ambient) {
          const x = p.x - r.left;
          const y = p.y - r.top;
          if (x < -8 || y < -8 || x > l.w + 8 || y > l.h + 8) continue;
          ctx.globalAlpha = p.a * (0.65 + 0.35 * Math.sin(p.tw));
          ctx.beginPath();
          ctx.arc(x, y, p.r, 0, Math.PI * 2);
          ctx.fill();
        }
        for (const s of this.sparks) {
          const x = s.x - r.left;
          const y = s.y - r.top;
          if (x < -8 || y < -8 || x > l.w + 8 || y > l.h + 8) continue;
          const k = 1 - s.life / s.max;
          ctx.globalAlpha = s.a * k * k;
          ctx.beginPath();
          ctx.arc(x, y, s.r * (0.5 + 0.5 * k), 0, Math.PI * 2);
          ctx.fill();
        }
        ctx.globalAlpha = 1;
      }
    }
  }

  let field; // the page's particle field, created at boot

  const fmt = (s) => Math.min(Math.max(s, 0), MAX_SECONDS).toFixed(2);
  const isExact = (diff) => Math.abs(diff) < 0.005;
  const fmtDiff = (diff) => (isExact(diff) ? '±' : diff > 0 ? '+' : '-') + Math.abs(diff).toFixed(2) + 's';

  const RATINGS = [
    [0.005, tr('rating.perfect'), 'perfect'],
    [0.05, tr('rating.incredible'), 'great'],
    [0.1, tr('rating.sharp'), 'great'],
    [0.25, tr('rating.close'), 'good'],
    [0.5, tr('rating.not_bad'), 'ok'],
    [1, tr('rating.warm'), 'ok'],
    [Infinity, tr('rating.way_off'), 'bad'],
  ];
  const rate = (diff) => RATINGS.find(([max]) => Math.abs(diff) < max || max === Infinity);

  // ---------------------------------------------------------------------------
  // Hidden timer: owns the tap zone, measures start→stop with event timestamps
  // ---------------------------------------------------------------------------
  class HiddenTimer {
    /**
     * @param {HTMLElement} zone
     * @param {{onStart:Function, onStop:Function, repeat?:boolean, restartDelay?:number}} opts
     *   repeat: a tap after a finished round starts a new one (solo). Party turns are one-shot.
     */
    constructor(zone, opts) {
      this.zone = zone;
      this.opts = { repeat: false, restartDelay: 700, ...opts };
      this.state = 'idle';
      this.t0 = 0;
      this.lockUntil = 0;
      this.capTimer = 0;
      field.addLayer(zone);

      zone.addEventListener('pointerdown', (e) => {
        if (!e.isPrimary || e.button !== 0) return;
        e.preventDefault(); // no text selection / double-tap zoom; also suppresses mouse-driven focus…
        zone.focus({ preventScroll: true, focusVisible: false }); // …so focus it ourselves (no ring for pointer users), keeping Space bound to the timer
        this.tap(eventTime(e)); // time first; effects after
        field.burst(e.clientX, e.clientY);
      });
      zone.addEventListener('contextmenu', (e) => e.preventDefault());
      // Position the hover spotlight (CSS ::before) under the mouse.
      zone.addEventListener('pointermove', (e) => {
        if (e.pointerType !== 'mouse') return;
        const r = zone.getBoundingClientRect();
        zone.style.setProperty('--mx', `${e.clientX - r.left}px`);
        zone.style.setProperty('--my', `${e.clientY - r.top}px`);
      });
      document.addEventListener('keydown', (e) => {
        if (e.repeat || (e.code !== 'Space' && e.key !== 'Enter')) return;
        if (!isShown(zone)) return;
        const a = document.activeElement;
        if (a && a !== document.body && a !== zone) return; // let focused controls handle their own keys
        e.preventDefault();
        this.tap(eventTime(e));
        const r = zone.getBoundingClientRect();
        field.burst(r.left + r.width / 2, r.top + r.height / 2);
      });
    }

    tap(now) {
      if (this.zone.dataset.loading) return; // still warming up (see warmUp)
      if (now < this.lockUntil) return;
      if (this.state === 'running') return this.stop(now);
      if (this.state === 'idle' || (this.state === 'done' && this.opts.repeat)) this.start(now);
    }

    start(now) {
      this.state = 'running';
      this.t0 = now;
      this.lockUntil = now + 150; // swallow accidental double taps
      clearTimeout(this.capTimer);
      this.capTimer = setTimeout(() => {
        if (this.state === 'running') this.stop(this.t0 + MAX_SECONDS * 1000);
      }, MAX_SECONDS * 1000);
      this.opts.onStart();
    }

    stop(now) {
      clearTimeout(this.capTimer);
      this.state = 'done';
      this.lockUntil = now + this.opts.restartDelay;
      this.opts.onStop(Math.min((now - this.t0) / 1000, MAX_SECONDS));
    }

    reset() {
      clearTimeout(this.capTimer);
      this.state = 'idle';
      this.lockUntil = 0;
    }
  }

  // Event timestamps share performance.now()'s time origin in modern browsers and
  // reflect when the input actually happened, not when our handler ran.
  function eventTime(e) {
    const now = performance.now();
    const ts = e.timeStamp;
    return ts > 0 && ts <= now + 1 && now - ts < 1000 ? ts : now;
  }

  function isShown(el) {
    return el.offsetParent !== null || el.getClientRects().length > 0;
  }

  function track(name, params) {
    if (typeof window.gtag === 'function') window.gtag('event', name, params);
  }

  // ---------------------------------------------------------------------------
  // Share panel: draws a 1080×1080 result card (seven-segment time, pixel type,
  // App Store badge) and wires native share, social links, copy and save.
  // Every shared message carries the site link and the /get App Store link.
  // ---------------------------------------------------------------------------
  const CARD = 1080;
  const GREEN = '#30fc60';
  const PIXEL_FONT = '"Press Start 2P", monospace';

  // Resolves when the browser has a quiet moment (or after `ms` at the latest).
  const idle = (ms = 400) => new Promise((resolve) => {
    if ('requestIdleCallback' in window) requestIdleCallback(resolve, { timeout: ms });
    else setTimeout(resolve, 120);
  });

  // Ready to play: the pixel font is in and the page has painted and settled.
  // Capped, so a slow font can never keep the game from starting.
  const warmUp = Promise.race([
    Promise.all([
      document.fonts ? document.fonts.load(`32px "Press Start 2P"`).catch(() => {}) : null,
      new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
    ]).then(() => idle(500)),
    new Promise((resolve) => setTimeout(resolve, 1500)),
  ]);

  const loadImage = (src) => new Promise((resolve) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => resolve(null); // the card still renders without it
    img.src = src;
  });

  function roundRect(ctx, x, y, w, h, r) {
    ctx.beginPath();
    if (ctx.roundRect) ctx.roundRect(x, y, w, h, r);
    else ctx.rect(x, y, w, h);
    ctx.fill();
  }

  // Centred pixel text, shrunk until it fits. `hero` adds a hard drop shadow and glow.
  function pixelText(ctx, text, x, y, size, color, maxWidth, hero = false) {
    ctx.font = `${size}px ${PIXEL_FONT}`;
    while (size > 12 && ctx.measureText(text).width > maxWidth) {
      size -= 4;
      ctx.font = `${size}px ${PIXEL_FONT}`;
    }
    if (hero) {
      ctx.fillStyle = '#0b4a1c';
      ctx.fillText(text, x + size / 10, y + size / 10);
      ctx.shadowColor = 'rgba(48, 252, 96, 0.55)';
      ctx.shadowBlur = 28;
    }
    ctx.fillStyle = color;
    ctx.fillText(text, x, y);
    ctx.shadowBlur = 0;
  }

  // Same geometry as createDisplay(): 64×108 digit cells, a 22-wide dot cell
  // that tucks into its neighbours, dim ghost segments behind lit ones.
  function drawSevenSeg(ctx, text, cx, bottom, h) {
    const k = h / 108;
    const gap = h * 0.09;
    const overlap = h * 0.07;
    const cells = [...text].map((ch) => (ch === '.' ? null : GLYPHS[ch] ?? ''));
    let width = 0;
    cells.forEach((g, i) => { width += (g === null ? 22 * k - 2 * overlap : 64 * k) + (i ? gap : 0); });
    let x = cx - width / 2;
    const top = bottom - h;
    ctx.shadowColor = 'rgba(48, 252, 96, 0.6)';
    cells.forEach((g, i) => {
      if (i) x += gap;
      if (g === null) {
        x -= overlap;
        ctx.fillStyle = GREEN;
        ctx.shadowBlur = 28;
        ctx.beginPath();
        ctx.arc(x + 11 * k, top + 100 * k, 7 * k, 0, Math.PI * 2);
        ctx.fill();
        x += 22 * k - overlap;
        return;
      }
      for (const name in SEGMENTS) {
        const [sx, sy, sw, sh] = SEGMENTS[name];
        const on = g.includes(name);
        ctx.fillStyle = on ? GREEN : 'rgba(48, 252, 96, 0.09)';
        ctx.shadowBlur = on ? 28 : 0;
        roundRect(ctx, x + sx * k, top + sy * k, sw * k, sh * k, (SEG * k) / 2);
      }
      x += 64 * k;
    });
    ctx.shadowBlur = 0;
  }

  function renderCard(r, icon, badge) {
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = CARD;
    const ctx = canvas.getContext('2d');
    const mid = CARD / 2;

    ctx.fillStyle = '#040e08';
    ctx.fillRect(0, 0, CARD, CARD);
    const glow = ctx.createRadialGradient(mid, 470, 0, mid, 470, 640);
    glow.addColorStop(0, 'rgba(48, 252, 96, 0.16)');
    glow.addColorStop(1, 'rgba(48, 252, 96, 0)');
    ctx.fillStyle = glow;
    ctx.fillRect(0, 0, CARD, CARD);

    // Ambient motes, like the site background.
    ctx.fillStyle = GREEN;
    for (let i = 0; i < 80; i++) {
      ctx.globalAlpha = 0.08 + Math.random() * 0.35;
      ctx.beginPath();
      ctx.arc(Math.random() * CARD, Math.random() * CARD, 1 + Math.random() * 4, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;

    // Notched pixel frame (corners left open, like the pixel buttons).
    const f = 36;
    const t = 6;
    ctx.fillStyle = 'rgba(48, 252, 96, 0.45)';
    ctx.fillRect(f + t, f, CARD - 2 * (f + t), t);
    ctx.fillRect(f + t, CARD - f - t, CARD - 2 * (f + t), t);
    ctx.fillRect(f, f + t, t, CARD - 2 * (f + t));
    ctx.fillRect(CARD - f - t, f + t, t, CARD - 2 * (f + t));

    // Brand row: app icon + wordmark.
    ctx.textBaseline = 'alphabetic';
    ctx.font = `32px ${PIXEL_FONT}`;
    const brand = 'TAP TIMER';
    const iconW = icon ? 96 : 0;
    const x0 = mid - (iconW + ctx.measureText(brand).width) / 2;
    if (icon) ctx.drawImage(icon, x0, 92, 72, 72);
    ctx.textAlign = 'left';
    ctx.fillStyle = '#eef1f7';
    ctx.fillText(brand, x0 + iconW, 144);

    // With a stats row (solo), everything above it moves up and the clock shrinks a little.
    const y = r.stats
      ? { headline: 272, clock: 560, clockH: 220, goal: 628, line: 672, cta: 872, url: 914, badge: 934 }
      : { headline: 300, clock: 620, clockH: 250, goal: 700, line: 752, cta: 850, url: 900, badge: 930 };
    ctx.textAlign = 'center';
    pixelText(ctx, r.headline.toUpperCase(), mid, y.headline, 64, GREEN, 920, true);
    drawSevenSeg(ctx, fmt(r.time), mid, y.clock, y.clockH);
    pixelText(ctx, tr('card.goal', { goal: fmt(r.target) }), mid, y.goal, 28, 'rgba(48, 252, 96, 0.75)', 920);
    pixelText(ctx, r.line, mid, y.line, 20, '#93a89b', 920);
    if (r.stats) drawStats(ctx, r.stats, mid, 704);
    pixelText(ctx, tr('card.cta'), mid, y.cta, 36, '#ebf3ee', 920);
    pixelText(ctx, tr('card.play_free', { host: location.host.toUpperCase() }), mid, y.url, 16, '#93a89b', 920);
    if (badge) {
      const bh = 80;
      const bw = (bh * 119.664) / 40; // badge aspect ratio
      ctx.drawImage(badge, mid - bw / 2, y.badge, bw, bh);
    }
    return canvas;
  }

  // The site's Best / Average / Rounds boxes, drawn as notched pixel frames.
  function drawStats(ctx, stats, mid, top) {
    const w = 240;
    const h = 104;
    const gap = 24;
    const t = 4;
    const cells = [[tr('card.best'), stats.best], [tr('card.average'), stats.avg], [tr('card.rounds'), stats.rounds]];
    cells.forEach(([label, value], i) => {
      const x = mid - (3 * w + 2 * gap) / 2 + i * (w + gap);
      ctx.fillStyle = 'rgba(48, 252, 96, 0.06)';
      ctx.fillRect(x, top, w, h);
      ctx.fillStyle = 'rgba(48, 252, 96, 0.35)';
      ctx.fillRect(x + t, top - t, w - 2 * t, t);
      ctx.fillRect(x + t, top + h, w - 2 * t, t);
      ctx.fillRect(x - t, top, t, h);
      ctx.fillRect(x + w, top, t, h);
      pixelText(ctx, label, x + w / 2, top + 38, 16, '#93a89b', w - 24);
      pixelText(ctx, value, x + w / 2, top + 82, 28, '#ebf3ee', w - 24);
    });
  }

  function createSharePanel(el) {
    if (!el) return { show() {}, hide() {} };
    const mode = el.dataset.mode;
    const img = el.querySelector('[data-share-img]');
    const preview = el.querySelector('[data-share-preview]');
    const lightbox = el.querySelector('[data-share-lightbox]');
    const lightboxImg = el.querySelector('[data-lightbox-img]');
    const saveBtn = el.querySelector('[data-share-save]');
    const nativeBtn = el.querySelector('[data-share-native]');
    const copyBtn = el.querySelector('[data-share-copy]');
    const links = [...el.querySelectorAll('[data-share-to]')];
    const enc = encodeURIComponent;
    let assets = null;
    let current = null;
    let file = null;
    let blobUrl = '';
    let seq = 0;

    nativeBtn.hidden = typeof navigator.share !== 'function';

    // Load the card's font and images now, not on the first stop tap.
    const loadAssets = () => (assets = assets || Promise.all([
      loadImage(el.dataset.icon),
      loadImage(el.dataset.badge),
      document.fonts ? document.fonts.load(`32px ${PIXEL_FONT}`).catch(() => {}) : null,
    ]));
    loadAssets();

    /** @param {{headline:string, time:number, target:number, line:string, text:string, url:string}} r */
    async function show(r) {
      const appUrl = el.dataset.appStore; // straight to the App Store, with campaign tags
      const message = `${r.text}\n${r.url}\n\n${tr('share.app_line', { url: appUrl })}`;
      current = { ...r, message };
      file = null;
      const hrefs = {
        x: `https://twitter.com/intent/tweet?text=${enc(`${r.text}\n\n${tr('share.x_app', { url: appUrl })}`)}&url=${enc(r.url)}`,
        facebook: `https://www.facebook.com/sharer/sharer.php?u=${enc(r.url)}`,
        whatsapp: `https://wa.me/?text=${enc(message)}`,
      };
      links.forEach((a) => (a.href = hrefs[a.dataset.shareTo]));
      el.hidden = false;

      const my = ++seq;
      const [icon, badge] = await loadAssets();
      // Drawing and encoding the 1080px card takes a moment; do it after the
      // stop has shown on screen so the result never stutters.
      await idle();
      if (my !== seq) return;
      const blob = await new Promise((resolve) => renderCard(r, icon, badge).toBlob(resolve, 'image/png'));
      if (my !== seq || !blob) return;
      if (blobUrl) URL.revokeObjectURL(blobUrl);
      blobUrl = URL.createObjectURL(blob);
      file = new File([blob], 'tap-timer-result.png', { type: 'image/png' });
      img.src = saveBtn.href = blobUrl;
      img.alt = tr('card.alt', { headline: r.headline, time: fmt(r.time), goal: fmt(r.target) });
    }

    function hide() {
      seq++;
      el.hidden = true;
    }

    nativeBtn.addEventListener('click', async () => {
      if (!current) return;
      const data = { title: 'Tap Timer', text: current.message };
      if (file && navigator.canShare && navigator.canShare({ files: [file] })) data.files = [file];
      try {
        await navigator.share(data);
        track('share', { mode, method: 'native' });
      } catch { /* cancelled or unsupported */ }
    });
    links.forEach((a) => a.addEventListener('click', () => track('share', { mode, method: a.dataset.shareTo })));
    saveBtn.addEventListener('click', (e) => {
      if (!file) return e.preventDefault(); // card still rendering
      track('share', { mode, method: 'image' });
    });
    // Clicking the preview shows the card larger on the page (Save image downloads it).
    preview.addEventListener('click', () => {
      if (!blobUrl) return; // card still rendering
      lightboxImg.src = blobUrl;
      lightboxImg.alt = img.alt;
      if (typeof lightbox.showModal === 'function') lightbox.showModal();
      else window.open(blobUrl, '_blank'); // very old browsers without <dialog>
      track('share_preview', { mode });
    });
    lightbox.addEventListener('click', () => lightbox.close()); // anywhere, including the image
    copyBtn.addEventListener('click', async () => {
      if (!current) return;
      try {
        await navigator.clipboard.writeText(current.message);
        copyBtn.textContent = tr('share.copied');
        track('share', { mode, method: 'copy' });
      } catch {
        copyBtn.textContent = tr('share.copy_failed');
      }
      setTimeout(() => (copyBtn.textContent = tr('share.copy_link')), 2000);
    });

    return { show, hide };
  }

  // ---------------------------------------------------------------------------
  // Solo mode (home page)
  // ---------------------------------------------------------------------------
  function initSolo(root) {
    const zone = root.querySelector('[data-tapzone]');
    const display = createDisplay(root.querySelector('[data-display]'), '##.##');
    const hint = root.querySelector('[data-hint]');
    const goalEl = root.querySelector('[data-goal]');
    const resultEl = root.querySelector('[data-result]');
    const ratingEl = root.querySelector('[data-rating]');
    const deltaEl = root.querySelector('[data-delta]');
    const share = createSharePanel(root.querySelector('[data-share-panel]'));
    const nudge = root.querySelector('[data-nudge]');
    const targetBtns = [...root.querySelectorAll('[data-target]')];
    const stealthBtn = root.querySelector('[data-stealth]');
    const keyhint = root.querySelector('.keyhint');
    const resetBtn = root.querySelector('[data-reset-stats]');
    const statEls = Object.fromEntries([...root.querySelectorAll('[data-stat]')].map((el) => [el.dataset.stat, el]));

    const fromUrl = Number(new URLSearchParams(location.search).get('t'));
    let target = TARGETS.includes(fromUrl) ? fromUrl : store.get('tt.target', 5);
    if (!TARGETS.includes(target)) target = 5;
    let last = null;
    let sessionRounds = 0;
    // Stats cover this page view only: they reset on refresh or navigation.
    const stats = {};
    try { localStorage.removeItem('tt.stats'); } catch { /* ignore */ } // drop stats saved by older versions
    // Stealth on (default) hides the clock while it runs. Off is practice:
    // the clock counts up. Rounds still count toward stats but aren't shareable.
    // Every visit starts in stealth; turning it off lasts for this page view only.
    let stealth = true;
    try { localStorage.removeItem('tt.stealth'); } catch { /* ignore */ } // drop the choice saved by older versions
    let tick = 0;

    const timer = new HiddenTimer(zone, {
      repeat: true,
      onStart() {
        if (keyhint) keyhint.hidden = true; // the tip has done its job once someone plays
        resultEl.hidden = true;
        share.hide();
        zone.dataset.state = 'running';
        hint.textContent = tr('hint.stop');
        [...targetBtns, stealthBtn, resetBtn].forEach((b) => (b.disabled = true));
        if (stealth) {
          display.set('-.--');
        } else {
          const run = () => {
            display.set(fmt((performance.now() - timer.t0) / 1000));
            tick = requestAnimationFrame(run);
          };
          run();
        }
      },
      onStop(elapsed) {
        cancelAnimationFrame(tick);
        zone.dataset.state = 'done';
        hint.textContent = '';
        [...targetBtns, stealthBtn, resetBtn].forEach((b) => (b.disabled = false));
        last = { elapsed, target, diff: elapsed - target, practice: !stealth };
        recordStats(last);
        sessionRounds++;
        track('round_complete', { mode: 'solo', target, stealth, diff: Number(Math.abs(last.diff).toFixed(2)) });
        showResult(last); // show the final time immediately so the stop feels instant
      },
    });

    function showResult(r) {
      display.set(fmt(r.elapsed));
      const [, label, tier] = rate(r.diff);
      ratingEl.textContent = label;
      ratingEl.dataset.tier = tier;
      deltaEl.textContent = isExact(r.diff)
        ? tr('solo.dead_on', { goal: fmt(r.target) })
        : tr(r.diff > 0 ? 'solo.over' : 'solo.under', { diff: fmtDiff(r.diff) });
      if (r.practice) {
        deltaEl.textContent += tr('solo.practice');
        share.hide();
      } else {
        share.show({
          headline: label,
          time: r.elapsed,
          target: r.target,
          stats: statText(r.target),
          line: isExact(r.diff) ? tr('card.dead_on') : tr(r.diff > 0 ? 'card.over' : 'card.under', { diff: fmtDiff(r.diff) }),
          text: isExact(r.diff)
            ? tr('solo.share_exact', { goal: fmt(r.target) })
            : tr('solo.share_off', { time: fmt(r.elapsed), goal: fmt(r.target), off: Math.abs(r.diff).toFixed(2) }),
          url: `${location.origin}${PREFIX}/?t=${r.target}`,
        });
      }
      resultEl.hidden = false;
      // On short phone screens the rating can land just below the fold: nudge it into view.
      if (resultEl.getBoundingClientRect().bottom > window.innerHeight) {
        resultEl.scrollIntoView({ block: 'nearest', behavior: reducedMotion ? 'auto' : 'smooth' });
      }
      hint.textContent = tr('hint.again');
      renderStats();
      if (sessionRounds >= 3 && !session.get('tt.nudged')) {
        nudge.hidden = false;
        session.set('tt.nudged', '1');
      }
    }

    function recordStats(r) {
      const s = stats[r.target] || (stats[r.target] = { best: null, rounds: 0, signed: 0 });
      const abs = Math.abs(r.diff);
      s.rounds += 1;
      s.signed += r.diff; // signed sum for the +/- average
      if (s.best == null || abs < s.best) s.best = abs;
    }

    // Best / Average / Rounds for a goal, as shown in the stat line and on the share card.
    function statText(t) {
      const s = stats[t];
      return {
        best: s ? s.best.toFixed(2) + 's' : '–',
        avg: s ? fmtDiff(s.signed / s.rounds) : '–', // + late, - early
        rounds: s ? String(s.rounds) : '0',
      };
    }

    function renderStats() {
      const text = statText(target);
      for (const key in text) statEls[key].textContent = text[key];
    }

    function setTarget(t) {
      target = t;
      store.set('tt.target', t);
      targetBtns.forEach((b) => b.setAttribute('aria-checked', String(Number(b.dataset.target) === t)));
      goalEl.textContent = fmt(t);
      timer.reset();
      zone.dataset.state = 'idle';
      resultEl.hidden = true;
      share.hide();
      display.set('0.00');
      hint.textContent = tr('hint.start');
      renderStats();
    }

    targetBtns.forEach((b) => b.addEventListener('click', () => setTarget(Number(b.dataset.target))));

    function setStealth(on) {
      stealth = on;
      stealthBtn.setAttribute('aria-pressed', String(on));
      stealthBtn.title = tr(on ? 'solo.stealth_on' : 'solo.stealth_off');
      zone.dataset.stealth = on ? 'on' : 'off';
    }
    stealthBtn.addEventListener('click', () => {
      setStealth(!stealth);
      track('stealth_toggle', { stealth });
    });
    setStealth(stealth);

    resetBtn.addEventListener('click', () => {
      for (const t in stats) delete stats[t];
      renderStats();
      track('stats_reset', { target });
    });

    setTarget(target);

    // A quick loading state so the first tap can't land mid-setup.
    zone.dataset.loading = 'true';
    hint.textContent = tr('hint.loading');
    warmUp.then(() => {
      delete zone.dataset.loading;
      if (zone.dataset.state === 'idle') hint.textContent = tr('hint.start');
    });
  }

  // ---------------------------------------------------------------------------
  // Party mode (Goal Challenge, pass-the-phone)
  // ---------------------------------------------------------------------------
  // Player 1–3 follow the app's defaults (red, periwinkle, pink).
  const PLAYER_COLORS = ['#ff4d4d', '#7c7cff', '#ff5fae', '#ffb627', '#56ccff', '#30fc60', '#ffe45c', '#b57bff'];
  const MIN_PLAYERS = 2;
  const MAX_PLAYERS = 8;

  function initParty(root) {
    const screens = Object.fromEntries([...root.querySelectorAll('[data-screen]')].map((s) => [s.dataset.screen, s]));
    const $ = (sel) => root.querySelector(sel);

    const targetSel = $('[data-party-target]');
    const list = $('[data-players]');
    const countEl = $('[data-player-count]');
    const addBtn = $('[data-add-player]');

    const zone = $('[data-tapzone]');
    const display = createDisplay($('[data-display]'), '##.##');
    const hint = $('[data-hint]');
    const nextBtn = $('[data-next]');
    const share = createSharePanel($('[data-share-panel]'));

    const defaultNames = [tr('party.player', { n: 1 }), tr('party.player', { n: 2 })];
    let names = store.get('tt.party.players', defaultNames);
    if (!Array.isArray(names) || names.length < MIN_PLAYERS) names = defaultNames;
    names = names.slice(0, MAX_PLAYERS).map(String);
    const savedTarget = store.get('tt.party.target', 5);
    if (TARGETS.includes(savedTarget)) targetSel.value = String(savedTarget);

    let game = null;
    let nextTimeout = 0;

    function show(name) {
      for (const key in screens) screens[key].hidden = key !== name;
      // Mid-game and at the reveal, drop the page intro so the tap zone, "Pass to…"
      // button and leaderboard fit on a phone screen.
      document.body.classList.toggle('party-playing', name !== 'setup');
      const header = document.querySelector('.site-header')?.offsetHeight || 0; // sticky, so it covers the top
      const top = root.getBoundingClientRect().top + window.scrollY - header - 12;
      if (window.scrollY > top) window.scrollTo({ top, behavior: reducedMotion ? 'auto' : 'smooth' });
    }

    // --- setup -------------------------------------------------------------
    function saveNames() { store.set('tt.party.players', names); }

    function renderPlayers(focusIndex) {
      list.textContent = '';
      names.forEach((name, i) => {
        const li = document.createElement('li');
        li.className = 'player-row';
        li.style.setProperty('--player', PLAYER_COLORS[i]);

        const dot = document.createElement('span');
        dot.className = 'player-dot';
        dot.setAttribute('aria-hidden', 'true');

        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'input';
        input.maxLength = 16;
        input.autocomplete = 'off';
        input.spellcheck = false;
        input.value = name;
        input.placeholder = tr('party.player', { n: i + 1 });
        input.setAttribute('aria-label', tr('party.player_name', { n: i + 1 }));
        input.addEventListener('input', () => { names[i] = input.value; saveNames(); });
        input.addEventListener('focus', () => input.select());

        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'icon-btn';
        remove.setAttribute('aria-label', tr('party.remove', { n: i + 1 }));
        remove.textContent = '✕';
        remove.disabled = names.length <= MIN_PLAYERS;
        remove.addEventListener('click', () => { names.splice(i, 1); saveNames(); renderPlayers(); });

        li.append(dot, input, remove);
        list.appendChild(li);
        if (i === focusIndex) input.focus();
      });
      addBtn.disabled = names.length >= MAX_PLAYERS;
      countEl.textContent = `(${names.length}/${MAX_PLAYERS})`;
    }

    addBtn.addEventListener('click', () => {
      if (names.length >= MAX_PLAYERS) return;
      names.push(tr('party.player', { n: names.length + 1 }));
      saveNames();
      renderPlayers(names.length - 1);
    });

    targetSel.addEventListener('change', () => store.set('tt.party.target', Number(targetSel.value)));

    $('[data-start]').addEventListener('click', () => {
      const target = Number(targetSel.value);
      startGame(target, names.map((n, i) => ({ name: n.trim() || tr('party.player', { n: i + 1 }), color: PLAYER_COLORS[i] })));
    });

    function startGame(target, players) {
      game = { target, players, idx: 0, results: [] };
      track('party_start', { target, players: players.length });
      goPass();
    }

    // --- pass & turn -------------------------------------------------------
    function current() { return game.players[game.idx]; }

    function goPass() {
      const p = current();
      const nameEl = $('[data-pass-name]');
      nameEl.textContent = p.name;
      nameEl.style.color = p.color;
      $('[data-pass-goal]').textContent = `${fmt(game.target)}s`;
      $('[data-pass-progress]').textContent = tr('party.progress', { n: game.idx + 1, total: game.players.length });
      show('pass');
    }

    $('[data-ready]').addEventListener('click', () => {
      const p = current();
      screens.turn.style.setProperty('--player', p.color);
      document.dispatchEvent(new Event('tt:theme')); // re-tint particles for this player
      $('[data-turn-name]').textContent = p.name;
      $('[data-turn-goal]').textContent = fmt(game.target);
      clearTimeout(nextTimeout);
      nextBtn.hidden = true;
      timer.reset();
      zone.dataset.state = 'idle';
      display.set('0.00');
      hint.textContent = tr('hint.start');
      show('turn');
    });

    const timer = new HiddenTimer(zone, {
      repeat: false,
      onStart() {
        zone.dataset.state = 'running';
        display.set('-.--');
        hint.textContent = tr('hint.stop');
      },
      onStop(elapsed) {
        game.results.push({ ...current(), elapsed, diff: elapsed - game.target });
        zone.dataset.state = 'locked';
        hint.textContent = tr('hint.locked');
        const last = game.idx === game.players.length - 1;
        nextBtn.textContent = last ? tr('party.reveal') : tr('party.pass_to', { name: game.players[game.idx + 1].name });
        // Short delay so the stopping tap can't land on the button underneath.
        nextTimeout = setTimeout(() => { nextBtn.hidden = false; }, 450);
      },
    });

    nextBtn.addEventListener('click', () => {
      if (game.idx < game.players.length - 1) {
        game.idx++;
        goPass();
      } else {
        showResults();
      }
    });

    // --- results -----------------------------------------------------------
    function showResults() {
      const ranked = [...game.results].sort((a, b) => Math.abs(a.diff) - Math.abs(b.diff));
      const board = $('[data-leaderboard]');
      board.textContent = '';
      let rank = 0;
      let prev = null;
      ranked.forEach((r, i) => {
        const key = Math.abs(r.diff).toFixed(2);
        if (key !== prev) rank = i + 1;
        prev = key;

        const li = document.createElement('li');
        li.className = 'leader' + (rank === 1 ? ' leader--first' : '');
        li.style.setProperty('--player', r.color);
        // Reveal from last place up to the winner.
        li.style.setProperty('--delay', `${(ranked.length - 1 - i) * 220}ms`);

        const rankEl = document.createElement('span');
        rankEl.className = 'leader__rank';
        rankEl.textContent = String(rank);
        const nameEl = document.createElement('span');
        nameEl.className = 'leader__name';
        nameEl.textContent = r.name;
        const timeEl = document.createElement('span');
        timeEl.className = 'leader__time';
        timeEl.textContent = `${fmt(r.elapsed)}s`;
        const diffEl = document.createElement('span');
        diffEl.className = 'leader__diff';
        diffEl.textContent = fmtDiff(r.diff);

        li.append(rankEl, nameEl, timeEl, diffEl);
        board.appendChild(li);
      });

      const winners = ranked.filter((r) => Math.abs(r.diff).toFixed(2) === Math.abs(ranked[0].diff).toFixed(2));
      $('[data-winner]').textContent = winners.length > 1
        ? tr('party.tie', { names: winners.map((w) => w.name).join(' & ') })
        : tr('party.wins', { name: winners[0].name });
      $('[data-results-goal]').textContent = fmt(game.target);
      const best = winners[0];
      const off = Math.abs(best.diff).toFixed(2);
      share.show({
        headline: winners.length > 1 ? tr('party.tie_headline') : tr('party.wins', { name: best.name }),
        time: best.elapsed,
        target: game.target,
        line: tr('card.party_line', { n: game.players.length, result: isExact(best.diff) ? tr('card.dead_on') : tr('card.off', { off }) }),
        text: winners.length > 1
          ? tr('party.share_tie', { names: winners.map((w) => w.name).join(' & '), off, goal: fmt(game.target) })
          : tr('party.share_win', { name: best.name, time: fmt(best.elapsed), goal: fmt(game.target), off }),
        url: `${location.origin}${PREFIX}/party`,
      });
      track('party_complete', { target: game.target, players: game.players.length });
      show('results');
    }

    $('[data-rematch]').addEventListener('click', () => startGame(game.target, game.players));
    $('[data-edit]').addEventListener('click', () => { renderPlayers(); show('setup'); });

    renderPlayers();
    show('setup');
  }

  // ---------------------------------------------------------------------------
  // Online rooms: everyone on their own phone, same goal, hidden times.
  // The server keeps the room; this page shows whatever state it sends and
  // times the player's own taps locally (so network lag never affects a score).
  // ---------------------------------------------------------------------------
  function initOnline(root) {
    const $ = (sel) => root.querySelector(sel);
    const screens = Object.fromEntries([...root.querySelectorAll('[data-screen]')].map((s) => [s.dataset.screen, s]));
    const banner = $('[data-banner]');
    const nameInput = $('[data-name]');
    const codeInput = $('[data-code]');
    const entryError = $('[data-entry-error]');
    const goalSel = $('[data-goal]');
    const roundsSel = $('[data-rounds]');
    const startBtn = $('[data-start]');
    const nextBtn = $('[data-next]');
    const zone = $('[data-tapzone]');
    const display = createDisplay($('[data-display]'), '##.##');
    const hint = $('[data-hint]');

    const ERRORS = Object.fromEntries(['room_not_found', 'room_full', 'match_in_progress', 'busy', 'rate_limited',
      'room_closed', 'server_restarting', 'not_enough_players'].map((k) => [k, tr(`online.err.${k}`)]));

    // A per-tab token lets a reload (or a dropped connection) take back the
    // same seat. sessionStorage, so two tabs on one device are two players.
    let token = session.get('tt.online.token');
    if (!token || !/^[A-Za-z0-9_-]{16,64}$/.test(token)) {
      const bytes = crypto.getRandomValues(new Uint8Array(18));
      token = btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      session.set('tt.online.token', token);
    }
    nameInput.value = store.get('tt.online.name', '') || '';

    let code = root.dataset.room || '';
    let ws = null;
    let me = '';
    let state = null;
    let retries = 0;
    let retryTimer = 0;
    let gone = false; // the room can't be rejoined; stop reconnecting
    let pending = null; // a result waiting for the connection to come back
    let lockedRound = 0; // the round this page submitted a time for (0: none yet this match)

    function show(name) {
      for (const key in screens) screens[key].hidden = key !== name;
      document.body.classList.toggle('party-playing', name !== 'entry');
    }

    function setBanner(text) {
      banner.textContent = text || '';
      banner.hidden = !text;
    }

    function entryFail(msg) {
      settle();
      entryError.textContent = msg;
      entryError.hidden = !msg;
      show('entry');
    }

    // --- connecting -----------------------------------------------------------
    // Creating or joining is usually instant. If it isn't (the server waking up
    // after being idle, or a slow network), show a "Connecting…" screen rather
    // than a button that seems to do nothing. A short delay avoids a flash.
    let connectingTimer = 0;
    function connecting() {
      clearTimeout(connectingTimer);
      connectingTimer = setTimeout(() => show('connecting'), 300);
    }
    function settle() { clearTimeout(connectingTimer); }

    // Keep the server awake while this page is open, so Create and Join don't
    // have to wait for it to start. (In a room, the connection itself does this.)
    setInterval(() => {
      if (!document.hidden && !ws) fetch('/healthz', { cache: 'no-store' }).catch(() => {});
    }, 120000);

    function playerName() {
      const n = nameInput.value.trim();
      store.set('tt.online.name', n);
      return n;
    }

    // --- create / join ------------------------------------------------------
    async function create() {
      entryFail('');
      connecting();
      try {
        const res = await fetch('/api/rooms', { method: 'POST' });
        const body = await res.json();
        if (body.error === 'server_full') return waitForSpot('create');
        if (!res.ok) return entryFail(ERRORS[body.error] || tr('online.create_failed'));
        track('room_create', {});
        enter(body.code);
      } catch {
        entryFail(tr('online.unreachable'));
      }
    }

    function enter(c) {
      code = c.toUpperCase();
      history.replaceState(null, '', `${PREFIX}/r/${code}`);
      session.set('tt.online.room', code); // so a reload of this tab rejoins straight away
      gone = false;
      retries = 0;
      connect();
    }

    $('[data-create]').addEventListener('click', create);
    $('[data-join]').addEventListener('click', () => enter(code));
    $('[data-join-code]').addEventListener('click', () => {
      const c = codeInput.value.trim().toUpperCase();
      if (!/^[23456789A-HJ-NP-Z]{4}$/.test(c)) return entryFail(tr('online.bad_code'));
      enter(c);
    });
    codeInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') $('[data-join-code]').click(); });
    codeInput.addEventListener('input', () => { codeInput.value = codeInput.value.toUpperCase(); });

    // --- connection ---------------------------------------------------------
    function connect() {
      clearTimeout(retryTimer);
      if (!state) connecting(); // first connection to this room (reconnects show a banner instead)
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws/${code}`);
      ws.onopen = () => {
        retries = 0;
        send({ t: 'join', name: playerName(), token });
      };
      ws.onmessage = (e) => {
        let m;
        try { m = JSON.parse(e.data); } catch { return; }
        if (m.t === 'welcome') {
          settle();
          me = m.you;
          waitTries = 0;
          setBanner('');
          if (pending) send(pending);
          pending = null;
        } else if (m.t === 'state') {
          render(m.state);
        } else if (m.t === 'error') {
          onError(m.code);
        }
      };
      ws.onclose = () => {
        ws = null;
        if (gone) return;
        if (retries >= 8) {
          setBanner(tr('online.lost_banner'));
          return entryFail(tr('online.lost'));
        }
        setBanner(tr('online.reconnecting'));
        retryTimer = setTimeout(connect, Math.min(8000, 500 * 2 ** retries++));
      };
    }

    function send(msg) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(msg));
        return true;
      }
      return false;
    }

    // --- rooms full: wait for a spot ---------------------------------------
    // The server caps how many players can be online at once. Rather than an
    // error, show a waiting screen and keep trying; join as soon as there's room.
    const fullCountdown = $('[data-full-countdown]');
    let waitTimer = 0;
    let tickTimer = 0;
    let waitTries = 0;

    function waitForSpot(mode) {
      settle();
      gone = true; // stop the normal reconnect loop while we wait
      if (ws) {
        ws.onclose = null;
        ws.close();
        ws = null;
      }
      setBanner('');
      show('full');
      if (waitTries++ === 0) track('online_full', { mode });
      // 8-14s, so a crowd of waiting pages doesn't retry in lockstep.
      let left = 8 + Math.floor(Math.random() * 7);
      const tick = () => {
        fullCountdown.textContent = tr('online.trying_in', { s: left });
        if (left-- > 0) tickTimer = setTimeout(tick, 1000);
      };
      clearTimeout(tickTimer);
      tick();
      clearTimeout(waitTimer);
      waitTimer = setTimeout(() => {
        fullCountdown.textContent = tr('online.checking');
        if (mode === 'create') {
          create();
        } else {
          gone = false;
          retries = 0;
          connect();
        }
      }, (left + 1) * 1000);
    }

    $('[data-full-cancel]').addEventListener('click', () => {
      clearTimeout(waitTimer);
      clearTimeout(tickTimer);
      waitTries = 0;
      gone = true;
      show('entry');
    });

    function onError(c) {
      if (c === 'server_full') return waitForSpot('join');
      if (['room_not_found', 'room_full', 'match_in_progress', 'room_closed', 'server_restarting'].includes(c)) {
        gone = true;
        state = null;
        me = '';
        setBanner('');
        // Back to "create or join", keeping the code box handy.
        $('[data-join-only]').hidden = true;
        $('[data-create-only]').hidden = false;
        history.replaceState(null, '', `${PREFIX}/online`);
        session.set('tt.online.room', '');
        return entryFail(ERRORS[c]);
      }
      if (ERRORS[c]) setBanner(ERRORS[c]);
    }

    // --- host controls ------------------------------------------------------
    function sendSettings() {
      send({ t: 'settings', goal: Number(goalSel.value), rounds: Number(roundsSel.value) });
    }
    goalSel.addEventListener('change', sendSettings);
    roundsSel.addEventListener('change', sendSettings);
    startBtn.addEventListener('click', () => send({ t: 'start' }));
    nextBtn.addEventListener('click', () => {
      if (!state) return;
      send({ t: state.phase === 'match_end' ? 'rematch' : 'start' });
    });

    const inviteURL = () => `${location.origin}${PREFIX}/r/${code}`;
    $('[data-copy]').addEventListener('click', async (e) => {
      try {
        await navigator.clipboard.writeText(inviteURL());
        e.target.textContent = tr('share.copied');
        setTimeout(() => { e.target.textContent = tr('share.copy_link'); }, 1600);
      } catch { prompt(tr('online.copy_prompt'), inviteURL()); }
    });
    const shareBtn = $('[data-share]');
    if (!navigator.share) shareBtn.hidden = true;
    shareBtn.addEventListener('click', () => {
      navigator.share({ title: tr('online.share_title'), text: tr('online.share_text', { code }), url: inviteURL() }).catch(() => {});
    });

    // --- round --------------------------------------------------------------
    const timer = new HiddenTimer(zone, {
      repeat: false,
      onStart() {
        zone.dataset.state = 'running';
        display.set('-.--');
        hint.textContent = tr('hint.stop');
      },
      onStop(elapsed) {
        zone.dataset.state = 'locked';
        hint.textContent = tr('hint.locked');
        lockedRound = state.round;
        const msg = { t: 'result', round: state.round, elapsed: Number(elapsed.toFixed(2)) };
        if (!send(msg)) pending = msg;
        track('round_complete', { mode: 'online', target: state.goal });
      },
    });

    // --- rendering ----------------------------------------------------------
    function render(s) {
      const prev = state;
      state = s;
      const isHost = s.host === me;
      root.querySelectorAll('[data-host-only]').forEach((el) => { el.hidden = !isHost; });
      root.querySelectorAll('[data-guest-only]').forEach((el) => { el.hidden = isHost; });
      const byId = Object.fromEntries(s.players.map((p) => [p.id, p]));

      if (s.phase === 'lobby') {
        renderLobby(s, isHost);
        show('lobby');
      } else if (s.phase === 'round') {
        const mine = byId[me];
        const done = Boolean(mine && mine.done);
        // A fresh tap zone whenever a round starts. Round numbers restart at 1
        // every match, so "a different number" alone isn't enough: anything
        // coming from another screen (lobby, reveal, a reconnect) is new too.
        const fresh = !prev || prev.phase !== 'round' || prev.round !== s.round;
        // Safety net: never leave the zone locked for a round this page didn't
        // submit, when the server agrees we haven't played it.
        const stale = zone.dataset.state === 'locked' && lockedRound !== s.round && !done;
        if (fresh || stale) {
          timer.reset();
          lockedRound = done ? s.round : 0;
          zone.dataset.state = done ? 'locked' : 'idle';
          display.set('0.00');
          hint.textContent = tr(done ? 'hint.locked' : 'hint.start');
          if (fresh && prev && prev.phase !== 'round') track('online_round', { round: s.round });
        }
        $('[data-round-label]').textContent = tr('online.round_of', { round: s.round, total: s.settings.rounds });
        $('[data-round-goal]').textContent = fmt(s.goal);
        $('[data-turn-goal]').textContent = fmt(s.goal);
        const doneCount = s.players.filter((p) => p.done).length;
        const waiting = s.players.filter((p) => p.connected && !p.done).length;
        $('[data-progress]').textContent = done
          ? (waiting ? tr('online.waiting_more', { n: waiting }) : tr('online.revealing'))
          : tr('online.locked_count', { done: doneCount, total: s.players.length });
        show('round');
      } else {
        renderResults(s, byId, isHost);
        show('results');
        if (s.phase === 'match_end' && (!prev || prev.phase !== 'match_end')) {
          track('match_complete', { players: s.players.length, rounds: s.settings.rounds });
        }
      }
    }

    function goalText(g) { return g === 0 ? tr('online.mixed_goals') : tr('online.goal_text', { goal: fmt(g) }); }

    function renderLobby(s, isHost) {
      $('[data-room-code]').textContent = s.code;
      const qr = $('[data-qr]');
      if (qr.dataset.code !== s.code) {
        qr.src = `${PREFIX}/r/${s.code}/qr.svg`;
        qr.dataset.code = s.code;
      }
      $('[data-player-count]').textContent = `(${s.players.length}/8)`;
      const list = $('[data-players]');
      list.textContent = '';
      for (const p of s.players) {
        const li = document.createElement('li');
        li.className = 'online__player' + (p.connected ? '' : ' online__player--away');
        li.style.setProperty('--player', p.color);
        const dot = document.createElement('span');
        dot.className = 'player-dot';
        dot.setAttribute('aria-hidden', 'true');
        const name = document.createElement('span');
        name.className = 'online__player-name';
        name.textContent = p.name;
        const tags = document.createElement('span');
        tags.className = 'online__tags';
        tags.textContent = [p.id === me && tr('online.tag_you'), p.id === s.host && tr('online.tag_host'), !p.connected && tr('online.tag_away')].filter(Boolean).join(' · ');
        li.append(dot, name, tags);
        list.appendChild(li);
      }
      const connected = s.players.filter((p) => p.connected).length;
      if (isHost) {
        // Don't fight the host's own select while they're using it.
        if (document.activeElement !== goalSel) goalSel.value = String(s.settings.goal);
        if (document.activeElement !== roundsSel) roundsSel.value = String(s.settings.rounds);
        startBtn.disabled = connected < 2;
        startBtn.textContent = tr(connected < 2 ? 'online.waiting_players' : 'online.start_match');
      } else {
        $('[data-settings-text]').textContent =
          trn('online.settings', s.settings.rounds, { rounds: s.settings.rounds, goal: goalText(s.settings.goal) });
      }
    }

    function leaderRow(p, rank, timeText, diffText, first, delay) {
      const li = document.createElement('li');
      li.className = 'leader' + (first ? ' leader--first' : '');
      li.style.setProperty('--player', p.color);
      li.style.setProperty('--delay', `${delay}ms`);
      const cells = [['leader__rank', String(rank)], ['leader__name', p.name + (p.id === me ? tr('online.you_suffix') : '')], ['leader__time', timeText], ['leader__diff', diffText]];
      for (const [cls, text] of cells) {
        const span = document.createElement('span');
        span.className = cls;
        span.textContent = text;
        li.appendChild(span);
      }
      return li;
    }

    function renderResults(s, byId, isHost) {
      const final = s.phase === 'match_end';
      $('[data-results-title]').textContent = final
        ? trn('online.final', s.settings.rounds)
        : tr('online.round_goal', { round: s.round, total: s.settings.rounds, goal: fmt(s.goal) });

      // This round, closest first, revealed from last place up.
      const reveal = $('[data-reveal]');
      reveal.textContent = '';
      let rank = 0;
      s.reveal.forEach((r, i) => {
        if (i === 0 || Math.abs(r.diff).toFixed(2) !== Math.abs(s.reveal[i - 1].diff).toFixed(2)) rank = i + 1;
        const p = byId[r.player];
        if (!p) return;
        reveal.appendChild(leaderRow(p, rank,
          r.missed ? tr('online.missed') : `${fmt(r.elapsed)}s`,
          fmtDiff(r.diff),
          r.won, (s.reveal.length - 1 - i) * 220));
      });

      // Match standings: lowest total error first.
      const standings = $('[data-standings]');
      standings.textContent = '';
      const order = [...s.players].sort((a, b) => a.rank - b.rank);
      order.forEach((p, i) => {
        standings.appendChild(leaderRow(p, p.rank, tr('online.total_off', { off: p.total.toFixed(2) }),
          trn('online.wins', p.wins), final && p.rank === 1, s.reveal.length * 220 + i * 120));
      });
      $('[data-standings-label]').hidden = s.settings.rounds === 1;
      standings.hidden = s.settings.rounds === 1;

      const leaders = order.filter((p) => p.rank === 1);
      const roundWinners = s.reveal.filter((r) => r.won).map((r) => byId[r.player]?.name).filter(Boolean);
      $('[data-winner]').textContent = final
        ? (leaders.length > 1 ? tr('online.match_tie', { names: leaders.map((p) => p.name).join(' & ') }) : tr('online.match_win', { name: leaders[0].name }))
        : (roundWinners.length ? trn('online.round_take', roundWinners.length, { names: roundWinners.join(' & ') }) : tr('online.nobody'));
      $('[data-winner]').classList.toggle('winner--round', !final);

      if (isHost) nextBtn.textContent = final ? tr('online.rematch') : tr('online.start_round', { n: s.round + 1 });
      // Mine, rated like solo, so the reveal still says how I did.
      const mine = s.reveal.find((r) => r.player === me);
      if (mine && !mine.missed && !final) {
        $('[data-results-title]').textContent += tr('online.you_rating', { rating: rate(mine.diff)[1] });
      }
    }

    if (code) {
      // Arrived on /r/CODE: join straight away if we already have a name
      // (e.g. after a reload); otherwise ask for one first.
      if (session.get('tt.online.room') === code && nameInput.value.trim()) enter(code);
      else show('entry');
    } else {
      show('entry');
    }
  }

  // ---------------------------------------------------------------------------
  // Ads: only initialise units that are actually visible at this viewport.
  // Hidden units (e.g. the desktop sidebar on a phone) are removed so AdSense
  // never tries to fill a zero-width slot.
  // ---------------------------------------------------------------------------
  //
  // House ads (our own campaigns, internal/server/houseads.go) wait in
  // <template data-house-frame>s. They fill a slot with no AdSense unit, and
  // take over a slot AdSense reports unfilled.
  //
  // Campaigns rotate over the slots actually SHOWN, which only the browser
  // knows: counting hidden slots too put both visible slots on a wide screen
  // on the same campaign. The count starts at random so a page's few slots
  // don't open on the same campaign every visit.
  // ---------------------------------------------------------------------------
  const PHONE = window.matchMedia('(max-width: 559px)');
  let houseTurn = Math.floor(Math.random() * 1000);

  // Insert the campaign's first creative that suits this viewport and fits
  // the slot: houseads.go lists them best fit first (widest), so a wide
  // column gets the leaderboard and a phone the box. The check happens before
  // insertion because a hidden iframe still downloads its creative in full.
  // Returns false when nothing fits.
  function showHouseAd(ad) {
    const viewport = PHONE.matches ? 'mobile' : 'desktop';
    const room = ad.clientWidth;
    const fits = (t) =>
      (!t.dataset.viewport || t.dataset.viewport === viewport) &&
      (!t.dataset.width || Number(t.dataset.width) <= room);

    const campaigns = new Map();
    ad.querySelectorAll('template[data-house-frame]').forEach((t) => {
      const id = t.dataset.campaign;
      if (!campaigns.has(id)) campaigns.set(id, []);
      campaigns.get(id).push(t);
    });
    const ids = [...campaigns.keys()];

    // This turn's campaign first, then the others, so a slot only goes empty
    // when nothing at all fits it.
    for (let i = 0; i < ids.length; i++) {
      const id = ids[(houseTurn + i) % ids.length];
      const t = campaigns.get(id).find(fits);
      if (t) {
        ad.append(t.content.cloneNode(true));
        houseTurn += 1;
        return true;
      }
    }
    return false;
  }

  function initAds() {
    document.querySelectorAll('.ad:not(.ad--placeholder)').forEach((ad) => {
      if (ad.offsetWidth === 0) {
        ad.remove();
        return;
      }
      const ins = ad.querySelector('ins.adsbygoogle:not([data-adsbygoogle-status])');
      if (!ins) {
        if (ad.hasAttribute('data-house') && !showHouseAd(ad)) ad.remove();
        return;
      }
      if (ad.hasAttribute('data-house')) {
        // Unsold inventory goes to our own campaigns instead of collapsing.
        const mo = new MutationObserver(() => {
          if (ins.dataset.adStatus !== 'unfilled') return;
          mo.disconnect();
          ins.remove();
          if (!showHouseAd(ad)) ad.remove();
        });
        mo.observe(ins, { attributes: true, attributeFilter: ['data-ad-status'] });
      }
      try { (window.adsbygoogle = window.adsbygoogle || []).push({}); } catch { /* blocked */ }
    });
  }

  // ---------------------------------------------------------------------------
  // Boot
  // ---------------------------------------------------------------------------
  const ambient = document.createElement('div');
  ambient.className = 'ambient';
  ambient.setAttribute('aria-hidden', 'true');
  document.body.prepend(ambient);
  field = new Particles(ambient);

  document.querySelectorAll('[data-mini-display]').forEach((el) => {
    const text = el.dataset.miniDisplay;
    createDisplay(el, text.replace(/[^.]/g, '#')).set(text);
  });
  document.querySelectorAll('[data-solo]').forEach(initSolo);
  document.querySelectorAll('[data-party]').forEach(initParty);
  document.querySelectorAll('[data-online]').forEach(initOnline);
  initAds();

  document.addEventListener('click', (e) => {
    const cta = e.target.closest('[data-cta]');
    if (cta) track('app_store_click', { source: cta.dataset.cta });
  });
})();
