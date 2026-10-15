// Tap Timer Studio: Record. Opens a full-screen stage that shows just you
// (front camera, rounded square), an optional hook line, and the website's
// Solo timer, and records it as a 1080×1920 vertical video with your
// microphone. Only the canvas is recorded; the icon toolbar below it (hook,
// camera, record, close, collapse) is HTML and stays out of the video.
//
// A web page can't record its own HTML, so the timer is drawn here to look
// like the website's Solo screen on a phone (app.css, scaled 2.5×), with the
// same rules, ratings and stats as app.js. Tapping a goal chip picks the goal;
// tapping the timer card plays. The video stays on the phone until you post it.
(() => {
  "use strict";
  const root = document.getElementById("record");
  if (!root) return;

  const W = 1080;
  const H = 1920;
  const K = 2.5; // canvas px per CSS px of a 430px-wide phone
  const FPS = 30;
  const MAX_RECORD_SECONDS = 170; // Shorts must be under 3 minutes
  const MAX_SECONDS = 99.99;
  const GOALS = [3, 5, 10, 15, 30]; // TARGETS in app.js

  // app.css tokens.
  const C = {
    bg: "#040e08", surface: "#0a1610", surface2: "#102018", border: "#1c3326",
    screenTop: "#0b1a10", screen: "#020805", text: "#ebf3ee", muted: "#93a89b",
    wordmark: "#eef1f7", seg: "#30fc60", glow: "rgba(48, 252, 96, 0.45)", onAccent: "#03140a",
    label: "#22b044", // color-mix(--seg-on 68%, --bg): "GOAL 5.00s", "TAP TO START"
  };
  const PIXEL = '"Press Start 2P", monospace';
  const SANS = '-apple-system, BlinkMacSystemFont, "SF Pro Display", "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif';

  // Same geometry, ratings and formatting as app.js.
  const SEG = 12;
  const SEGMENTS = {
    a: [16, 2, 32, SEG], g: [16, 48, 32, SEG], d: [16, 94, 32, SEG],
    f: [2, 15, SEG, 32], b: [50, 15, SEG, 32],
    e: [2, 61, SEG, 32], c: [50, 61, SEG, 32],
  };
  const GLYPHS = {
    0: "abcdef", 1: "bc", 2: "abdeg", 3: "abcdg", 4: "bcfg", 5: "acdfg", 6: "acdefg",
    7: "abc", 8: "abcdefg", 9: "abcdfg", "-": "g", " ": "",
  };
  const RATINGS = [
    [0.005, "PERFECT!", "perfect"],
    [0.05, "INCREDIBLE", "great"],
    [0.1, "SHARP", "great"],
    [0.25, "CLOSE", "good"],
    [0.5, "NOT BAD", "ok"],
    [1, "WARM", "ok"],
    [Infinity, "WAY OFF", "bad"],
  ];
  // .pixel .result__rating colours: green, dimmer for worse tiers.
  const TIER_COLOR = { perfect: C.seg, great: C.seg, good: C.seg, ok: "#5ad87a", bad: "#5c9a6a" };
  const rate = (diff) => RATINGS.find(([max]) => Math.abs(diff) < max);
  const fmt = (s) => Math.min(Math.max(s, 0), MAX_SECONDS).toFixed(2);
  const isExact = (diff) => Math.abs(diff) < 0.005;
  const fmtDiff = (diff) => (isExact(diff) ? "±" : diff > 0 ? "+" : "-") + Math.abs(diff).toFixed(2) + "s";

  const $ = (sel) => root.querySelector(sel);
  const stage = $("[data-stage]");
  const canvas = $("canvas");
  canvas.width = W;
  canvas.height = H;
  const ctx = canvas.getContext("2d");
  const openBtn = $("[data-open]");
  const closeBtn = $("[data-close]");
  const ov = $("[data-ov]");
  const hookInput = $("[data-hook]");
  const hookPanel = $("[data-hook-panel]");
  const hookBtn = $("[data-hook-btn]");
  const camBtn = $("[data-camera]");
  const collapseBtn = $("[data-collapse]");
  const recBtn = $("[data-rec]");
  const recTime = $("[data-rec-time]");
  const stageMsg = $("[data-stage-msg]");
  const say = (msg) => ($("[data-rec-status]").textContent = msg);
  const after = document.getElementById("after-record");
  const preview = after.querySelector("video");
  const saveLink = after.querySelector("[data-save]");
  const postLink = after.querySelector("[data-post]");
  const keepError = after.querySelector("[data-keep-error]");
  after.querySelector("[data-again]").addEventListener("click", () => openBtn.click());

  const icon = new Image();
  icon.src = root.dataset.icon;

  // --- layout (on the 1080×1920 canvas) ---
  const L = {
    header: 162, // the site's 65px header
    cam: { x: (W - 600) / 2, y: 222, s: 600, r: 64 }, // smaller while there's a hook: see camRect
    chips: 900, // top of the goal chips
    zone: { x: 40, y: 1040, w: W - 80, h: 560, r: 20 * K },
    result: 1690,
    stats: 1770,
  };

  // --- game state (mirrors HiddenTimer and the Solo stats in app.js) ---
  const game = { target: 5, phase: "idle", t0: 0, elapsed: 0, lockUntil: 0, doneAt: 0 };
  const stats = {}; // per goal: { best, rounds, signed }, for this session
  let chipRects = []; // where drawChips put each goal, for tapping

  function tap(now) {
    if (now < game.lockUntil) return;
    if (game.phase === "running") {
      game.phase = "done";
      game.elapsed = Math.min((now - game.t0) / 1000, MAX_SECONDS);
      game.lockUntil = now + 700;
      game.doneAt = now;
      const diff = game.elapsed - game.target;
      const s = stats[game.target] || (stats[game.target] = { best: null, rounds: 0, signed: 0 });
      s.rounds += 1;
      s.signed += diff;
      if (s.best == null || Math.abs(diff) < s.best) s.best = Math.abs(diff);
    } else {
      game.phase = "running";
      game.t0 = now;
      game.lockUntil = now + 150; // swallow accidental double taps
    }
  }

  canvas.addEventListener("pointerdown", (e) => {
    if (!e.isPrimary) return;
    e.preventDefault();
    const now = e.timeStamp; // when the tap happened, not when this ran
    // Canvas coordinates of the tap (the canvas is scaled to fit the screen).
    const r = canvas.getBoundingClientRect();
    const x = ((e.clientX - r.left) / r.width) * W;
    const y = ((e.clientY - r.top) / r.height) * H;
    if (game.phase !== "running") {
      const chip = chipRects.find((c) => x >= c.x - 12 && x <= c.x + c.w + 12 && y >= c.y - 12 && y <= c.y + c.h + 12);
      if (chip) {
        game.target = chip.goal;
        game.phase = "idle";
        return;
      }
    }
    // Like the website, only the timer card (the tap zone) plays.
    const z = L.zone;
    if (x >= z.x && x <= z.x + z.w && y >= z.y && y <= z.y + z.h) tap(now);
  });
  canvas.addEventListener("contextmenu", (e) => e.preventDefault());

  // --- the stage (full screen) ---
  const cam = document.createElement("video");
  cam.muted = true;
  cam.playsInline = true;
  let stream = null;

  async function startCamera() {
    stageMsg.textContent = "";
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: "user", width: { ideal: 1080 }, height: { ideal: 1080 } },
        audio: { echoCancellation: true, noiseSuppression: true },
      });
      cam.srcObject = stream;
      await cam.play();
    } catch (err) {
      stream = null;
      stageMsg.textContent = err.name === "NotAllowedError"
        ? "Camera blocked: allow camera and microphone for this site in Settings. You can still record without them."
        : `No camera (${err.message}). You can still record without it.`;
    }
    syncCamBtn();
  }

  function stopCamera() {
    if (stream) stream.getTracks().forEach((t) => t.stop()); // camera light off
    stream = null;
    syncCamBtn();
  }

  function syncCamBtn() {
    camBtn.setAttribute("aria-pressed", String(!!stream));
    camBtn.setAttribute("aria-label", stream ? "Turn camera off" : "Turn camera on");
  }

  openBtn.addEventListener("click", () => {
    after.hidden = true;
    stage.hidden = false;
    document.documentElement.classList.add("st-staged"); // no page scroll behind it
    if (!stream) startCamera();
  });

  closeBtn.addEventListener("click", closeStage);

  function closeStage() {
    if (rec) stopRecording(); // keeps what was recorded
    stage.hidden = true;
    document.documentElement.classList.remove("st-staged");
    showHook(false);
    stopCamera();
  }

  // --- the toolbar: camera, hook, collapse ---
  // The camera can't change mid-recording: its microphone is the video's sound.
  camBtn.addEventListener("click", () => (stream ? stopCamera() : startCamera()));

  function showHook(open) {
    hookPanel.hidden = !open;
    hookBtn.setAttribute("aria-expanded", String(open));
    if (open) hookInput.focus();
  }
  hookBtn.addEventListener("click", () => showHook(hookPanel.hidden));
  hookPanel.addEventListener("submit", (e) => {
    e.preventDefault();
    hookInput.blur(); // closes the keyboard
    showHook(false);
  });

  // Collapsed, only the record button and this toggle are left.
  collapseBtn.addEventListener("click", () => {
    const collapsed = ov.classList.toggle("is-collapsed");
    collapseBtn.setAttribute("aria-expanded", String(!collapsed));
    collapseBtn.setAttribute("aria-label", collapsed ? "Show controls" : "Hide controls");
    if (collapsed) showHook(false);
  });

  // --- drawing helpers ---
  function rr(x, y, w, h, r) {
    ctx.beginPath();
    if (ctx.roundRect) ctx.roundRect(x, y, w, h, r);
    else ctx.rect(x, y, w, h);
  }

  // Text with letter spacing, drawn a character at a time (ctx.letterSpacing
  // isn't in every browser).
  function spaced(text, x, y, spacing, align = "center") {
    const chars = [...text];
    const widths = chars.map((ch) => ctx.measureText(ch).width);
    const total = widths.reduce((a, b) => a + b, 0) + spacing * (chars.length - 1);
    let cx = align === "center" ? x - total / 2 : x;
    const prev = ctx.textAlign;
    ctx.textAlign = "left";
    chars.forEach((ch, i) => {
      ctx.fillText(ch, cx, y);
      cx += widths[i] + spacing;
    });
    ctx.textAlign = prev;
  }

  // The site's "pixel" bevelled box (.pixel .chip, .pixel.stats div).
  function pixelBox(x, y, w, h, { fill, edge, hi = "rgba(255,255,255,0.08)", lo = "rgba(0,0,0,0.4)" }) {
    const px = 3 * K;
    ctx.fillStyle = edge;
    ctx.fillRect(x, y - px, w, px);
    ctx.fillRect(x, y + h, w, px);
    ctx.fillRect(x - px, y, px, h);
    ctx.fillRect(x + w, y, px, h);
    ctx.fillStyle = fill;
    ctx.fillRect(x, y, w, h);
    ctx.fillStyle = hi;
    ctx.fillRect(x, y, w, px);
    ctx.fillStyle = lo;
    ctx.fillRect(x, y + h - px, w, px);
  }

  function drawSevenSeg(text, cx, bottom, h, alpha = 1) {
    const k = h / 108;
    const gap = h * 0.09;
    const overlap = h * 0.07;
    const cells = [...text].map((ch) => (ch === "." ? null : GLYPHS[ch] ?? ""));
    let width = 0;
    cells.forEach((g, i) => { width += (g === null ? 22 * k - 2 * overlap : 64 * k) + (i ? gap : 0); });
    let x = cx - width / 2;
    const top = bottom - h;
    ctx.save();
    ctx.globalAlpha = alpha;
    ctx.shadowColor = "rgba(48, 252, 96, 0.6)";
    cells.forEach((g, i) => {
      if (i) x += gap;
      if (g === null) {
        x -= overlap;
        ctx.fillStyle = C.seg;
        ctx.shadowBlur = 30;
        ctx.beginPath();
        ctx.arc(x + 11 * k, top + 100 * k, 7 * k, 0, Math.PI * 2);
        ctx.fill();
        x += 22 * k - overlap;
        return;
      }
      for (const name in SEGMENTS) {
        const [sx, sy, sw, sh] = SEGMENTS[name];
        const on = g.includes(name);
        ctx.fillStyle = on ? C.seg : "rgba(48, 252, 96, 0.09)";
        ctx.shadowBlur = on ? 30 : 0;
        rr(x + sx * k, top + sy * k, sw * k, sh * k, (SEG * k) / 2);
        ctx.fill();
      }
      x += 64 * k;
    });
    ctx.restore();
  }

  // Background motes, drifting slowly (like the site's particle field).
  let seed = 7;
  const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
  const motes = Array.from({ length: 70 }, () => ({
    x: rnd() * W, y: rnd() * H, r: 1.5 + rnd() * 3.5, a: 0.1 + rnd() * 0.35, v: 6 + rnd() * 14,
  }));
  function drawMotes(t) {
    ctx.fillStyle = C.seg;
    for (const m of motes) {
      const y = (((m.y - (t / 1000) * m.v) % H) + H) % H;
      ctx.globalAlpha = m.a;
      ctx.beginPath();
      ctx.arc(m.x, y, m.r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  }

  // The site header: logo and "GET THE APP →" (.get-app).
  function drawHeader() {
    ctx.fillStyle = "rgba(4, 14, 8, 0.85)";
    ctx.fillRect(0, 0, W, L.header);
    ctx.fillStyle = C.border;
    ctx.fillRect(0, L.header - K, W, K);
    const is = 36 * K;
    const iy = (L.header - is) / 2;
    if (icon.complete && icon.naturalWidth) {
      ctx.save();
      rr(16 * K, iy, is, is, 9 * K);
      ctx.clip();
      ctx.drawImage(icon, 16 * K, iy, is, is);
      ctx.restore();
    }
    ctx.textBaseline = "middle";
    ctx.font = `800 ${14 * K}px ${SANS}`;
    ctx.fillStyle = C.wordmark;
    spaced("TAP TIMER", 16 * K + is + 12 * K, L.header / 2 + 2, 14 * K * 0.28, "left");
    ctx.font = `${8 * K}px ${PIXEL}`;
    const label = "GET THE APP";
    const tw = ctx.measureText(label).width + 0.04 * 8 * K * (label.length - 1);
    const bw = tw + 14 * K * 2 + 24 * K;
    const bh = 40 * K;
    const bx = W - 16 * K - bw;
    const by = (L.header - bh) / 2;
    ctx.fillStyle = "rgba(48, 252, 96, 0.08)";
    ctx.fillRect(bx, by, bw, bh);
    ctx.strokeStyle = "rgba(48, 252, 96, 0.55)";
    ctx.lineWidth = 2 * K;
    ctx.strokeRect(bx + K, by + K, bw - 2 * K, bh - 2 * K);
    ctx.fillStyle = C.seg;
    ctx.shadowColor = C.glow;
    ctx.shadowBlur = 8 * K;
    spaced(label, bx + 14 * K, L.header / 2 + 2, 0.04 * 8 * K, "left");
    ctx.font = `700 ${14 * K}px ${SANS}`;
    ctx.fillText("→", bx + bw - 14 * K - ctx.measureText("→").width, L.header / 2 + 2);
    ctx.shadowBlur = 0;
    ctx.textBaseline = "alphabetic";
  }

  const hookText = () => hookInput.value.trim();

  // With a hook, the camera shrinks to make room for it underneath.
  const HOOK_CAM = { x: (W - 480) / 2, y: 206, s: 480, r: 56 };
  const camRect = () => (hookText() ? HOOK_CAM : L.cam);

  // The hook: bold sans under the camera, at most two lines, as big as fits.
  function drawHook() {
    const text = hookText();
    if (!text) return;
    const maxW = W - 120;
    let size = 72;
    let lines;
    for (; size >= 40; size -= 4) {
      ctx.font = `800 ${size}px ${SANS}`;
      lines = wrap(text, maxW);
      if (lines.length <= 2 && lines.every((l) => ctx.measureText(l).width <= maxW)) break;
    }
    const lh = size * 1.1;
    const top = HOOK_CAM.y + HOOK_CAM.s + 26; // under the camera, above the chips
    const room = L.chips - 24 - top;
    let y = top + (room - Math.min(lines.length, 2) * lh) / 2 + size * 0.82;
    ctx.fillStyle = C.text;
    ctx.textAlign = "center";
    ctx.shadowColor = "rgba(0, 0, 0, 0.6)";
    ctx.shadowBlur = 12;
    for (const line of lines.slice(0, 2)) {
      ctx.fillText(line, W / 2, y);
      y += lh;
    }
    ctx.shadowBlur = 0;
  }

  function wrap(text, maxWidth) {
    const lines = [];
    let line = "";
    for (const word of text.split(/\s+/).filter(Boolean)) {
      const next = line ? `${line} ${word}` : word;
      if (line && ctx.measureText(next).width > maxWidth) {
        lines.push(line);
        line = word;
      } else line = next;
    }
    if (line) lines.push(line);
    return lines;
  }

  // You: the front camera, mirrored and centre-cropped into a rounded square.
  function drawCamera() {
    const { x, y, s, r } = camRect();
    ctx.save();
    ctx.shadowColor = "rgba(48, 252, 96, 0.3)";
    ctx.shadowBlur = 40;
    ctx.fillStyle = C.surface;
    rr(x, y, s, s, r);
    ctx.fill();
    ctx.restore();
    if (stream && cam.videoWidth) {
      ctx.save();
      rr(x, y, s, s, r);
      ctx.clip();
      const v = Math.min(cam.videoWidth, cam.videoHeight);
      ctx.translate(x + s, y);
      ctx.scale(-1, 1); // selfie mirror
      ctx.drawImage(cam, (cam.videoWidth - v) / 2, (cam.videoHeight - v) / 2, v, v, 0, 0, s, s);
      ctx.restore();
    } else {
      ctx.font = `600 ${13 * K}px ${SANS}`;
      ctx.fillStyle = C.muted;
      ctx.textAlign = "center";
      spaced("CAMERA OFF", x + s / 2, y + s / 2 + 12, 13 * K * 0.16);
    }
    ctx.lineWidth = 2 * K;
    ctx.strokeStyle = "rgba(48, 252, 96, 0.55)";
    rr(x, y, s, s, r);
    ctx.stroke();
  }

  // Goal presets (.pixel .chip), the selected one filled green.
  function drawChips() {
    ctx.font = `${12 * K}px ${PIXEL}`;
    const pad = 8 * K;
    const h = 40 * K;
    const gap = 6 * K + 2 * 3 * K; // flex gap + each chip's margin
    const widths = GOALS.map((g) => Math.max(ctx.measureText(`${g}s`).width + pad * 2, 52 * K));
    const total = widths.reduce((a, b) => a + b, 0) + gap * (GOALS.length - 1);
    let x = (W - total) / 2;
    chipRects = [];
    GOALS.forEach((g, i) => {
      const on = g === game.target;
      pixelBox(x, L.chips, widths[i], h, on
        ? { fill: C.seg, edge: "#98fdb0", hi: "rgba(255,255,255,0.45)", lo: "rgba(0,0,0,0.3)" }
        : { fill: C.surface2, edge: "#2b7a40" });
      ctx.fillStyle = on ? C.onAccent : "#5fe07f";
      ctx.textAlign = "center";
      ctx.fillText(`${g}s`, x + widths[i] / 2, L.chips + h / 2 + 6 * K);
      chipRects.push({ goal: g, x, y: L.chips, w: widths[i], h });
      x += widths[i] + gap;
    });
  }

  // The tap zone (.tapzone): glowing screen panel with goal, clock and hint.
  function drawZone(t) {
    const { x, y, w, h, r } = L.zone;
    const running = game.phase === "running";
    ctx.save();
    ctx.shadowColor = `rgba(48, 252, 96, ${running ? 0.2 : 0.12})`;
    ctx.shadowBlur = (running ? 36 : 24) * K;
    const lin = ctx.createLinearGradient(0, y, 0, y + h);
    lin.addColorStop(0, C.screenTop);
    lin.addColorStop(1, C.screen);
    ctx.fillStyle = lin;
    rr(x, y, w, h, r);
    ctx.fill();
    ctx.restore();

    ctx.save();
    rr(x, y, w, h, r);
    ctx.clip();
    const rad = ctx.createRadialGradient(x + w / 2, y + h * 0.55, 0, x + w / 2, y + h * 0.55, w * 0.6);
    rad.addColorStop(0, "rgba(48, 252, 96, 0.07)");
    rad.addColorStop(1, "rgba(48, 252, 96, 0)");
    ctx.fillStyle = rad;
    ctx.fillRect(x, y, w, h);
    // Inner glow that slowly pulses (tap-glow: 6s, 3.6s while running).
    const period = running ? 3600 : 6000;
    const pulse = 0.3 + 0.7 * (0.5 - 0.5 * Math.cos((t / period) * Math.PI * 2));
    ctx.globalAlpha = 0.6 * pulse;
    ctx.lineWidth = 56 * K;
    ctx.strokeStyle = "rgba(48, 252, 96, 0.16)";
    ctx.shadowColor = "rgba(48, 252, 96, 0.3)";
    ctx.shadowBlur = 56 * K;
    rr(x - 28 * K, y - 28 * K, w + 56 * K, h + 56 * K, r + 28 * K);
    ctx.stroke();
    ctx.restore();

    const breathe = 0.725 + 0.275 * Math.cos((t / 2400) * Math.PI * 2); // 1 → 0.45 → 1
    const digitH = 236;
    const mid = x + w / 2;
    const clockBottom = y + h / 2 + digitH / 2 + 10;

    ctx.font = `600 ${13 * K}px ${SANS}`;
    ctx.fillStyle = C.label;
    ctx.textAlign = "center";
    spaced(`GOAL ${fmt(game.target)}s`, mid, clockBottom - digitH - 18 * K, 13 * K * 0.16);

    let clock = fmt(0);
    let alpha = 1;
    if (running) {
      clock = "-.--"; // stealth: the clock hides while it runs
      alpha = 0.7 * breathe;
    }
    if (game.phase === "done") clock = fmt(game.elapsed);
    drawSevenSeg(clock, mid, clockBottom, digitH, alpha);

    const hint = running ? "TAP TO STOP" : game.phase === "done" ? "TAP TO GO AGAIN" : "TAP TO START";
    ctx.save();
    if (game.phase === "idle") ctx.globalAlpha = breathe;
    ctx.font = `600 ${13 * K}px ${SANS}`;
    ctx.fillStyle = C.label;
    spaced(hint, mid, clockBottom + 18 * K + 13 * K, 13 * K * 0.16);
    ctx.restore();
  }

  // The result under the zone (.pixel .result__rating / __delta).
  function drawResult(t) {
    if (game.phase !== "done") return;
    const diff = game.elapsed - game.target;
    const [, label, tier] = rate(diff);
    // PERFECT! blinks three times, like the site.
    if (tier === "perfect" && t - game.doneAt < 3600 && Math.floor((t - game.doneAt) / 600) % 2 === 1) return;
    ctx.textAlign = "center";
    ctx.font = `${22 * K}px ${PIXEL}`;
    ctx.fillStyle = "#0c3f18"; // hard drop shadow
    ctx.fillText(label, W / 2 + 3 * K, L.result + 3 * K);
    ctx.fillStyle = TIER_COLOR[tier];
    if (tier === "perfect" || tier === "great") {
      ctx.shadowColor = C.glow;
      ctx.shadowBlur = 18 * K;
    }
    ctx.fillText(label, W / 2, L.result);
    ctx.shadowBlur = 0;
    ctx.font = `${8 * K}px ${PIXEL}`;
    ctx.fillStyle = C.muted;
    const delta = isExact(diff) ? `Dead on ${fmt(game.target)}s!` : `${fmtDiff(diff)} ${diff > 0 ? "over" : "under"} the goal`;
    spaced(delta, W / 2, L.result + 22 * K, 0.06 * 8 * K);
  }

  // Best / Average / Rounds (.stats.pixel) for the current goal.
  function drawStats() {
    const s = stats[game.target];
    const items = [
      ["BEST", s ? s.best.toFixed(2) + "s" : "–"],
      ["AVERAGE", s ? fmtDiff(s.signed / s.rounds) : "–"],
      ["ROUNDS", s ? String(s.rounds) : "0"],
    ];
    const gap = 8 * K + 6 * K;
    const bw = (W - 2 * 16 * K - 2 * gap) / 3;
    const bh = 52 * K;
    items.forEach(([dt, dd], i) => {
      const x = 16 * K + i * (bw + gap);
      pixelBox(x, L.stats, bw, bh, { fill: "#0b1b11", edge: C.border });
      ctx.textAlign = "center";
      ctx.font = `${8 * K}px ${PIXEL}`;
      ctx.fillStyle = C.muted;
      spaced(dt, x + bw / 2, L.stats + 20 * K, 0.06 * 8 * K);
      ctx.font = `700 ${18 * K}px ${SANS}`;
      ctx.fillStyle = C.text;
      ctx.fillText(dd, x + bw / 2, L.stats + 42 * K);
    });
  }

  function draw(t) {
    ctx.textAlign = "center";
    ctx.textBaseline = "alphabetic";
    ctx.fillStyle = C.bg;
    ctx.fillRect(0, 0, W, H);
    drawMotes(t);
    drawHeader();
    drawCamera();
    drawHook();
    drawChips();
    drawZone(t);
    drawResult(t);
    drawStats();
  }

  function loop(t) {
    draw(t);
    requestAnimationFrame(loop);
  }

  // --- recording ---
  let rec = null;
  let recStart = 0;
  let recTick = 0;
  let wakeLock = null; // keeps the screen on while recording (where supported)

  const mimeType = ["video/mp4;codecs=avc1.640028,mp4a.40.2", "video/mp4", "video/webm;codecs=vp9,opus", "video/webm"]
    .find((t) => window.MediaRecorder && MediaRecorder.isTypeSupported(t));

  if (!window.MediaRecorder || !canvas.captureStream || !mimeType) {
    openBtn.disabled = true;
    say("This browser can't record video. Use Safari on iPhone or Chrome on a computer.");
  }

  recBtn.addEventListener("click", () => (rec ? stopRecording() : startRecording()));

  function startRecording() {
    const out = canvas.captureStream(FPS);
    if (stream) stream.getAudioTracks().forEach((t) => out.addTrack(t));
    const chunks = [];
    rec = new MediaRecorder(out, { mimeType, videoBitsPerSecond: 8_000_000, audioBitsPerSecond: 128_000 });
    rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);
    rec.onstop = () => {
      out.getVideoTracks().forEach((t) => t.stop());
      finished(new Blob(chunks, { type: mimeType.split(";")[0] }));
    };
    rec.start(1000);
    navigator.wakeLock?.request("screen").then((l) => (wakeLock = l)).catch(() => {});
    recStart = performance.now();
    recBtn.classList.add("is-recording");
    recBtn.setAttribute("aria-label", "Stop recording");
    camBtn.disabled = true;
    recTime.textContent = "0:00";
    recTick = setInterval(() => {
      const s = Math.floor((performance.now() - recStart) / 1000);
      recTime.textContent = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
      if (s >= MAX_RECORD_SECONDS) stopRecording();
    }, 250);
  }

  function stopRecording() {
    clearInterval(recTick);
    recTime.textContent = "";
    recBtn.classList.remove("is-recording");
    recBtn.setAttribute("aria-label", "Start recording");
    camBtn.disabled = false;
    wakeLock?.release().catch(() => {});
    wakeLock = null;
    const r = rec;
    rec = null;
    r.stop();
  }

  async function finished(blob) {
    closeStage();
    if (preview.src) URL.revokeObjectURL(preview.src);
    const url = URL.createObjectURL(blob);
    preview.src = url;
    const ext = blob.type.includes("mp4") ? "mp4" : "webm";
    saveLink.href = url;
    saveLink.download = `tap-timer-${new Date().toISOString().slice(0, 10)}.${ext}`;
    after.hidden = false;
    after.scrollIntoView({ behavior: "smooth", block: "start" });
    try {
      sessionStorage.setItem("st.hook", hookText()); // New post uses it as the title
    } catch {}
    // The Post page picks it up from the browser's storage (never the server).
    let kept = true;
    try {
      await window.studioRecording.save(blob);
    } catch {
      kept = false;
    }
    postLink.hidden = !kept;
    keepError.hidden = kept;
  }

  // Start drawing once the pixel font is in (capped, so it never blocks).
  Promise.race([
    document.fonts ? document.fonts.load(`32px "Press Start 2P"`) : null,
    new Promise((r) => setTimeout(r, 1500)),
  ]).finally(() => requestAnimationFrame(loop));
})();
