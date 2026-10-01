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
  // Ambient particles: slow drifting motes like the app's screen, plus a burst
  // on every tap. Canvas sits behind the display; paused when off-screen.
  // The same class also fills a fixed full-page layer behind all content.
  // ---------------------------------------------------------------------------
  class Particles {
    /** @param {{density?:number, max?:number}} opts  one mote per `density` px², capped at `max` */
    constructor(host, { density = 8000, max = 46 } = {}) {
      this.host = host;
      this.density = density;
      this.max = max;
      this.canvas = document.createElement('canvas');
      this.canvas.className = 'particles';
      this.canvas.setAttribute('aria-hidden', 'true');
      host.prepend(this.canvas);
      this.ctx = this.canvas.getContext('2d');
      this.ambient = [];
      this.sparks = [];
      this.visible = true;
      this.raf = 0;
      this.w = 0;
      this.h = 0;
      this.readColor();
      new ResizeObserver(() => this.resize()).observe(host);
      new IntersectionObserver(([e]) => { this.visible = e.isIntersecting; this.kick(); }).observe(host);
      document.addEventListener('visibilitychange', () => this.kick());
      document.addEventListener('tt:theme', () => { this.readColor(); this.draw(); });
    }

    readColor() {
      this.color = getComputedStyle(this.host).getPropertyValue('--seg-on').trim() || '#30fc60';
    }

    resize() {
      const { width, height } = this.host.getBoundingClientRect();
      if (!width || !height) return;
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      this.w = width;
      this.h = height;
      this.canvas.width = Math.round(width * dpr);
      this.canvas.height = Math.round(height * dpr);
      this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const target = Math.round(Math.min(this.max, Math.max(14, (width * height) / this.density)));
      while (this.ambient.length < target) this.ambient.push(this.mote(true));
      this.ambient.length = target;
      this.draw();
      this.kick();
    }

    mote(anywhere) {
      const soft = Math.random() < 0.35; // out-of-focus bokeh dots
      return {
        x: Math.random() * this.w,
        y: anywhere ? Math.random() * this.h : this.h + 8,
        r: soft ? 2.5 + Math.random() * 3 : 0.8 + Math.random() * 1.8,
        vx: (Math.random() - 0.5) * 6,
        vy: -(2 + Math.random() * 8),
        a: soft ? 0.08 + Math.random() * 0.12 : 0.18 + Math.random() * 0.4,
        tw: Math.random() * Math.PI * 2,
        soft,
      };
    }

    // Burst from (x, y) in host coordinates. Particles fly out, slow down and
    // linger as extra ambient motes before fading.
    burst(x, y, count = 28) {
      if (reducedMotion || !this.w) return;
      this.readColor(); // party turns tint the zone per player
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

    running() { return this.visible && !document.hidden && !reducedMotion; }

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
        p.x += p.vx * dt;
        p.y += p.vy * dt;
        p.tw += dt * 1.3;
        if (p.y < -8) Object.assign(p, this.mote(false));
        if (p.x < -8) p.x = this.w + 8;
        else if (p.x > this.w + 8) p.x = -8;
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
      const { ctx } = this;
      ctx.clearRect(0, 0, this.w, this.h);
      ctx.fillStyle = this.color;
      for (const p of this.ambient) {
        ctx.globalAlpha = p.a * (0.65 + 0.35 * Math.sin(p.tw));
        ctx.beginPath();
        ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2);
        ctx.fill();
      }
      for (const s of this.sparks) {
        const k = 1 - s.life / s.max;
        ctx.globalAlpha = s.a * k * k;
        ctx.beginPath();
        ctx.arc(s.x, s.y, s.r * (0.5 + 0.5 * k), 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.globalAlpha = 1;
    }
  }

  const fmt = (s) => Math.min(Math.max(s, 0), MAX_SECONDS).toFixed(2);
  const isExact = (diff) => Math.abs(diff) < 0.005;
  const fmtDiff = (diff) => (isExact(diff) ? '±' : diff > 0 ? '+' : '-') + Math.abs(diff).toFixed(2) + 's';

  const RATINGS = [
    [0.005, 'PERFECT!', 'perfect'],
    [0.05, 'INCREDIBLE', 'great'],
    [0.1, 'SHARP', 'great'],
    [0.25, 'CLOSE', 'good'],
    [0.5, 'NOT BAD', 'ok'],
    [1, 'WARM', 'ok'],
    [Infinity, 'WAY OFF', 'bad'],
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
      this.fx = new Particles(zone);

      zone.addEventListener('pointerdown', (e) => {
        if (!e.isPrimary || e.button !== 0) return;
        e.preventDefault(); // no text selection / double-tap zoom; also suppresses mouse-driven focus…
        zone.focus({ preventScroll: true, focusVisible: false }); // …so focus it ourselves (no ring for pointer users), keeping Space bound to the timer
        this.tap(eventTime(e)); // time first; effects after
        const r = zone.getBoundingClientRect();
        this.fx.burst(e.clientX - r.left, e.clientY - r.top);
      });
      zone.addEventListener('contextmenu', (e) => e.preventDefault());
      document.addEventListener('keydown', (e) => {
        if (e.repeat || (e.code !== 'Space' && e.key !== 'Enter')) return;
        if (!isShown(zone)) return;
        const a = document.activeElement;
        if (a && a !== document.body && a !== zone) return; // let focused controls handle their own keys
        e.preventDefault();
        this.tap(eventTime(e));
        this.fx.burst(this.fx.w / 2, this.fx.h / 2);
      });
    }

    tap(now) {
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

    ctx.textAlign = 'center';
    pixelText(ctx, r.headline.toUpperCase(), mid, 300, 64, GREEN, 920, true);
    drawSevenSeg(ctx, fmt(r.time), mid, 620, 250);
    pixelText(ctx, `GOAL ${fmt(r.target)}s`, mid, 700, 28, 'rgba(48, 252, 96, 0.75)', 920);
    pixelText(ctx, r.line, mid, 752, 20, '#93a89b', 920);
    pixelText(ctx, 'CAN YOU BEAT IT?', mid, 850, 36, '#ebf3ee', 920);
    pixelText(ctx, `PLAY FREE AT ${location.host.toUpperCase()}`, mid, 900, 16, '#93a89b', 920);
    if (badge) {
      const bh = 80;
      const bw = (bh * 119.664) / 40; // badge aspect ratio
      ctx.drawImage(badge, mid - bw / 2, 930, bw, bh);
    }
    return canvas;
  }

  function createSharePanel(el) {
    if (!el) return { show() {}, hide() {} };
    const mode = el.dataset.mode;
    const img = el.querySelector('[data-share-img]');
    const preview = el.querySelector('[data-share-preview]');
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

    /** @param {{headline:string, time:number, target:number, line:string, text:string, url:string}} r */
    async function show(r) {
      const appUrl = `${location.origin}/get?src=share_${mode}`;
      const message = `${r.text}\n${r.url}\n\n📱 Tap Timer for iPhone & iPad: ${appUrl}`;
      current = { ...r, message };
      file = null;
      const hrefs = {
        x: `https://twitter.com/intent/tweet?text=${enc(`${r.text}\n\n📱 iPhone & iPad app: ${appUrl}`)}&url=${enc(r.url)}`,
        facebook: `https://www.facebook.com/sharer/sharer.php?u=${enc(r.url)}`,
        whatsapp: `https://wa.me/?text=${enc(message)}`,
      };
      links.forEach((a) => (a.href = hrefs[a.dataset.shareTo]));
      el.hidden = false;

      const my = ++seq;
      assets = assets || Promise.all([
        loadImage(el.dataset.icon),
        loadImage(el.dataset.badge),
        document.fonts ? document.fonts.load(`32px ${PIXEL_FONT}`).catch(() => {}) : null,
      ]);
      const [icon, badge] = await assets;
      if (my !== seq) return;
      const blob = await new Promise((resolve) => renderCard(r, icon, badge).toBlob(resolve, 'image/png'));
      if (my !== seq || !blob) return;
      if (blobUrl) URL.revokeObjectURL(blobUrl);
      blobUrl = URL.createObjectURL(blob);
      file = new File([blob], 'tap-timer-result.png', { type: 'image/png' });
      img.src = preview.href = saveBtn.href = blobUrl;
      img.alt = `Result card: ${r.headline}, ${fmt(r.time)}s with a goal of ${fmt(r.target)}s`;
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
    [saveBtn, preview].forEach((a) => a.addEventListener('click', (e) => {
      if (!file) return e.preventDefault(); // card still rendering
      track('share', { mode, method: 'image' });
    }));
    copyBtn.addEventListener('click', async () => {
      if (!current) return;
      try {
        await navigator.clipboard.writeText(current.message);
        copyBtn.textContent = 'Copied!';
        track('share', { mode, method: 'copy' });
      } catch {
        copyBtn.textContent = 'Could not copy';
      }
      setTimeout(() => (copyBtn.textContent = 'Copy link'), 2000);
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
    const statEls = Object.fromEntries([...root.querySelectorAll('[data-stat]')].map((el) => [el.dataset.stat, el]));

    const fromUrl = Number(new URLSearchParams(location.search).get('t'));
    let target = TARGETS.includes(fromUrl) ? fromUrl : store.get('tt.target', 5);
    if (!TARGETS.includes(target)) target = 5;
    let last = null;
    let sessionRounds = 0;
    // Stealth on (default) hides the clock while it runs. Off is practice:
    // the clock counts up, and rounds aren't saved to stats or shareable.
    let stealth = store.get('tt.stealth', true) !== false;
    let tick = 0;

    const timer = new HiddenTimer(zone, {
      repeat: true,
      onStart() {
        resultEl.hidden = true;
        share.hide();
        zone.dataset.state = 'running';
        hint.textContent = 'Tap to stop';
        [...targetBtns, stealthBtn].forEach((b) => (b.disabled = true));
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
        [...targetBtns, stealthBtn].forEach((b) => (b.disabled = false));
        last = { elapsed, target, diff: elapsed - target, practice: !stealth };
        if (!last.practice) recordStats(last);
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
        ? `Dead on ${fmt(r.target)}s!`
        : `${fmtDiff(r.diff)} ${r.diff > 0 ? 'over' : 'under'} the goal`;
      if (r.practice) {
        deltaEl.textContent += ' · practice, not saved';
        share.hide();
      } else {
        share.show({
          headline: label,
          time: r.elapsed,
          target: r.target,
          line: isExact(r.diff) ? 'DEAD ON!' : `${fmtDiff(r.diff)} ${r.diff > 0 ? 'OVER' : 'UNDER'}`,
          text: isExact(r.diff)
            ? `⏱️ I stopped the hidden clock at exactly ${fmt(r.target)}s on Tap Timer. Can you?`
            : `⏱️ I stopped the hidden clock at ${fmt(r.elapsed)}s (goal ${fmt(r.target)}s) on Tap Timer, off by ${Math.abs(r.diff).toFixed(2)}s. Can you beat me?`,
          url: `${location.origin}/?t=${r.target}`,
        });
      }
      resultEl.hidden = false;
      hint.textContent = 'Tap to go again';
      renderStats();
      if (sessionRounds >= 3 && !session.get('tt.nudged')) {
        nudge.hidden = false;
        session.set('tt.nudged', '1');
      }
    }

    function recordStats(r) {
      const all = store.get('tt.stats', {});
      const s = all[r.target] || { best: null, rounds: 0, total: 0 };
      const abs = Math.abs(r.diff);
      s.rounds += 1;
      s.total += abs;
      // Signed sum for the +/- average. Tracked separately because stats saved
      // before it existed only have the unsigned total.
      s.signed = (s.signed || 0) + r.diff;
      s.signedRounds = (s.signedRounds || 0) + 1;
      if (s.best == null || abs < s.best) s.best = abs;
      all[r.target] = s;
      store.set('tt.stats', all);
    }

    function renderStats() {
      const s = store.get('tt.stats', {})[target];
      statEls.best.textContent = s && s.best != null ? s.best.toFixed(2) + 's' : '–';
      statEls.avg.textContent = !s || !s.rounds ? '–'
        : s.signedRounds ? fmtDiff(s.signed / s.signedRounds) // + late, - early
          : (s.total / s.rounds).toFixed(2) + 's';
      statEls.rounds.textContent = s ? String(s.rounds) : '0';
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
      hint.textContent = 'Tap to start';
      renderStats();
    }

    targetBtns.forEach((b) => b.addEventListener('click', () => setTarget(Number(b.dataset.target))));

    function setStealth(on) {
      stealth = on;
      stealthBtn.setAttribute('aria-pressed', String(on));
      stealthBtn.title = on ? 'Stealth on: the clock hides while it runs' : 'Stealth off: practice with the clock visible';
      zone.dataset.stealth = on ? 'on' : 'off';
    }
    stealthBtn.addEventListener('click', () => {
      setStealth(!stealth);
      store.set('tt.stealth', stealth);
      track('stealth_toggle', { stealth });
    });
    setStealth(stealth);

    setTarget(target);
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

    let names = store.get('tt.party.players', ['Player 1', 'Player 2']);
    if (!Array.isArray(names) || names.length < MIN_PLAYERS) names = ['Player 1', 'Player 2'];
    names = names.slice(0, MAX_PLAYERS).map(String);
    const savedTarget = store.get('tt.party.target', 5);
    if (TARGETS.includes(savedTarget)) targetSel.value = String(savedTarget);

    let game = null;
    let nextTimeout = 0;

    function show(name) {
      for (const key in screens) screens[key].hidden = key !== name;
      // Mid-game, drop the page intro so the tap zone and "Pass to…" button fit on a phone screen.
      document.body.classList.toggle('party-playing', name === 'pass' || name === 'turn');
      const top = root.getBoundingClientRect().top + window.scrollY - 12;
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
        input.placeholder = `Player ${i + 1}`;
        input.setAttribute('aria-label', `Player ${i + 1} name`);
        input.addEventListener('input', () => { names[i] = input.value; saveNames(); });
        input.addEventListener('focus', () => input.select());

        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'icon-btn';
        remove.setAttribute('aria-label', `Remove player ${i + 1}`);
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
      names.push(`Player ${names.length + 1}`);
      saveNames();
      renderPlayers(names.length - 1);
    });

    targetSel.addEventListener('change', () => store.set('tt.party.target', Number(targetSel.value)));

    $('[data-start]').addEventListener('click', () => {
      const target = Number(targetSel.value);
      startGame(target, names.map((n, i) => ({ name: n.trim() || `Player ${i + 1}`, color: PLAYER_COLORS[i] })));
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
      $('[data-pass-progress]').textContent = `Player ${game.idx + 1} of ${game.players.length}`;
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
      hint.textContent = 'Tap to start';
      show('turn');
    });

    const timer = new HiddenTimer(zone, {
      repeat: false,
      onStart() {
        zone.dataset.state = 'running';
        display.set('-.--');
        hint.textContent = 'Tap to stop';
      },
      onStop(elapsed) {
        game.results.push({ ...current(), elapsed, diff: elapsed - game.target });
        zone.dataset.state = 'locked';
        hint.textContent = 'Locked in ✓';
        const last = game.idx === game.players.length - 1;
        nextBtn.textContent = last ? 'Reveal results' : `Pass to ${game.players[game.idx + 1].name}`;
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
        ? `It's a tie: ${winners.map((w) => w.name).join(' & ')}!`
        : `${winners[0].name} wins!`;
      $('[data-results-goal]').textContent = fmt(game.target);
      const best = winners[0];
      const off = Math.abs(best.diff).toFixed(2);
      share.show({
        headline: winners.length > 1 ? "It's a tie!" : `${best.name} wins!`,
        time: best.elapsed,
        target: game.target,
        line: `${game.players.length} PLAYERS · ${isExact(best.diff) ? 'DEAD ON!' : `${off}s OFF`}`,
        text: winners.length > 1
          ? `🏆 Tie on Tap Timer party mode! ${winners.map((w) => w.name).join(' & ')} stopped the hidden clock ${off}s from ${fmt(game.target)}s. Think your group can do better?`
          : `🏆 ${best.name} won Tap Timer party mode with ${fmt(best.elapsed)}s on a hidden ${fmt(game.target)}s clock, ${off}s off. Think your group can do better?`,
        url: `${location.origin}/party`,
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
  // Ads: only initialise units that are actually visible at this viewport.
  // Hidden units (e.g. the desktop sidebar on a phone) are removed so AdSense
  // never tries to fill a zero-width slot.
  // ---------------------------------------------------------------------------
  function initAds() {
    const units = document.querySelectorAll('ins.adsbygoogle:not([data-adsbygoogle-status])');
    units.forEach((ins) => {
      if (ins.offsetWidth === 0) {
        ins.closest('.ad')?.remove();
        return;
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
  new Particles(ambient, { density: 14000, max: 80 });

  document.querySelectorAll('[data-mini-display]').forEach((el) => {
    const text = el.dataset.miniDisplay;
    createDisplay(el, text.replace(/[^.]/g, '#')).set(text);
  });
  document.querySelectorAll('[data-solo]').forEach(initSolo);
  document.querySelectorAll('[data-party]').forEach(initParty);
  initAds();

  document.addEventListener('click', (e) => {
    const cta = e.target.closest('[data-cta]');
    if (cta) track('app_store_click', { source: cta.dataset.cta });
  });
})();
