// Live fractal-flame screensaver in WebGL2.
//
// ~1M points live in a GPU buffer. Each frame, a vertex shader advances every
// point one step of the chaos game (transform feedback writes the new state)
// and plots it additively into a half-float histogram that decays slowly.
// A final pass log-density tone-maps the histogram to the screen.
//
// The genome morphs into a mutated child every GENERATION_MS. Each payment
// flares one transform in a contrasting colour and nudges the next child.

import { breed, hash, interpolate, MAX_XFORMS, nudge, NV, rng, type Genome } from "./genome";

const GENERATION_MS = 4 * 60 * 1000;
const WILD_EVERY = 5; // every Nth generation is a fresh genome, not a child
const FLARE_MS = 2600;
const MAX_POINTS = 1 << 20;
const DECAY = 0.965;

export interface Visual {
  /** A payment arrived. `amount` in satoshis. */
  pulse(id: string, amount: number): void;
  /** PNG of the visual alone (no UI), captured on the next rendered frame. */
  snapshot(): Promise<Blob | null>;
}

const UPDATE_VS = `#version 300 es
precision highp float;
#define MAXX ${MAX_XFORMS}
#define PI 3.14159265
layout(location = 0) in vec4 aState;   // x, y, color, age
out vec4 vState;
out vec3 vColor;

uniform int uCount;
uniform float uCum[MAXX];
uniform vec3 uRow0[MAXX];
uniform vec3 uRow1[MAXX];
uniform vec4 uVa[MAXX];
uniform vec4 uVb[MAXX];
uniform float uColor[MAXX];
uniform float uFlare[MAXX];
uniform vec3 uFlareColor[MAXX];
uniform int uSym;
uniform uint uSeed;
uniform vec4 uView;     // cx, cy, zoom, rot
uniform float uAspect;
uniform vec3 uPal[4];

uint state;
float rand() {
  state = state * 747796405u + 2891336453u;
  uint w = ((state >> ((state >> 28u) + 4u)) ^ state) * 277803737u;
  return float((w >> 22u) ^ w) / 4294967296.0;
}

vec2 vary(vec2 p, vec4 a, vec4 b) {
  float r2 = dot(p, p) + 1e-9;
  float r = sqrt(r2);
  float th = atan(p.x, p.y);
  vec2 o = a.x * p;
  if (a.y != 0.0) o += a.y * sin(p);
  if (a.z != 0.0) o += a.z * p / r2;
  if (a.w != 0.0) { float s = sin(r2), c = cos(r2); o += a.w * vec2(p.x * s - p.y * c, p.x * c + p.y * s); }
  if (b.x != 0.0) o += b.x * vec2((p.x - p.y) * (p.x + p.y), 2.0 * p.x * p.y) / r;
  if (b.y != 0.0) o += b.y * vec2(th / PI, r - 1.0);
  if (b.z != 0.0) o += b.z * (th / PI) * vec2(sin(PI * r), cos(PI * r));
  if (b.w != 0.0) o += b.w * r * vec2(sin(th * r), -cos(th * r));
  return o;
}

vec3 palette(float t) {
  return clamp(uPal[0] + uPal[1] * cos(6.2831853 * (uPal[2] * t + uPal[3])), 0.0, 1.0);
}

void main() {
  state = uint(gl_VertexID) * 2654435761u ^ uSeed;
  rand();
  vec2 p = aState.xy;
  float c = aState.z;
  float age = aState.w;

  float u = rand();
  int k = 0;
  for (int j = 0; j < MAXX - 1; j++) {
    if (j < uCount - 1 && u > uCum[j]) k = j + 1;
  }
  vec2 q = vec2(dot(uRow0[k], vec3(p, 1.0)), dot(uRow1[k], vec3(p, 1.0)));
  p = vary(q, uVa[k], uVb[k]);
  c = 0.5 * (c + uColor[k]);
  if (uSym > 1) {
    float ang = 6.2831853 * floor(rand() * float(uSym)) / float(uSym);
    float cs = cos(ang), sn = sin(ang);
    p = vec2(p.x * cs - p.y * sn, p.x * sn + p.y * cs);
  }
  if (any(isnan(p)) || any(isinf(p)) || dot(p, p) > 1e8) {
    p = vec2(rand(), rand()) * 2.0 - 1.0;
    age = 0.0;
  } else {
    age = min(age + 1.0, 100.0);
  }
  // Occasionally respawn a point so none get stuck on a fixed point.
  if (rand() < 0.002) { p = vec2(rand(), rand()) * 2.0 - 1.0; age = 0.0; }
  vState = vec4(p, c, age);

  vec3 col = palette(c);
  float fl = uFlare[k];
  vColor = mix(col, uFlareColor[k], fl) * (1.0 + 2.5 * fl);

  vec2 s = p - uView.xy;
  float cr = cos(uView.w), sr = sin(uView.w);
  s = vec2(s.x * cr - s.y * sr, s.x * sr + s.y * cr) * uView.z;
  s.x /= uAspect;
  gl_Position = age < 12.0 ? vec4(2.0, 2.0, 0.0, 1.0) : vec4(s, 0.0, 1.0);
  gl_PointSize = 1.0;
}`;

const UPDATE_FS = `#version 300 es
precision highp float;
in vec3 vColor;
out vec4 outColor;
void main() { outColor = vec4(vColor, 1.0); }`;

const QUAD_VS = `#version 300 es
precision highp float;
out vec2 vUv;
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  vUv = p;
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}`;

const DECAY_FS = `#version 300 es
precision highp float;
uniform float uDecay;
out vec4 outColor;
void main() { outColor = vec4(0.0, 0.0, 0.0, uDecay); }`;

const SHOW_FS = `#version 300 es
precision highp float;
in vec2 vUv;
uniform sampler2D uAccum;
uniform float uMean;
uniform float uGain;
out vec4 outColor;
void main() {
  vec4 h = texture(uAccum, vUv);
  float d = h.a;
  vec3 avg = h.rgb / max(d, 1e-6);
  // Log-density tone map, relative to the mean density of lit pixels.
  float x = d / max(uMean, 1e-6);
  float a = clamp(log(1.0 + x * uGain) / log(1.0 + 24.0 * uGain), 0.0, 1.0);
  vec3 col = pow(avg, vec3(0.85)) * pow(a, 0.5) * 1.15;
  // Soft vignette.
  vec2 v = vUv - 0.5;
  col *= 1.0 - 0.35 * dot(v, v) * 2.0;
  outColor = vec4(col, 1.0);
}`;

function compile(gl: WebGL2RenderingContext, type: number, src: string): WebGLShader {
  const s = gl.createShader(type)!;
  gl.shaderSource(s, src);
  gl.compileShader(s);
  if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) ?? "shader compile failed");
  return s;
}

function program(gl: WebGL2RenderingContext, vs: string, fs: string, feedback?: string[]): WebGLProgram {
  const p = gl.createProgram()!;
  gl.attachShader(p, compile(gl, gl.VERTEX_SHADER, vs));
  gl.attachShader(p, compile(gl, gl.FRAGMENT_SHADER, fs));
  if (feedback) gl.transformFeedbackVaryings(p, feedback, gl.SEPARATE_ATTRIBS);
  gl.linkProgram(p);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p) ?? "program link failed");
  return p;
}

/** Returns null when WebGL2 + float render targets are unavailable. */
export interface FlameOptions {
  /** Fixes the evolution so the same seed gives the same flames (used for the OG image). */
  seed?: number;
}

export function createFlame(canvas: HTMLCanvasElement, options: FlameOptions = {}): Visual | null {
  const gl = canvas.getContext("webgl2", { antialias: false, alpha: false, preserveDrawingBuffer: false });
  if (!gl || !gl.getExtension("EXT_color_buffer_float")) return null;

  let update: WebGLProgram, decay: WebGLProgram, show: WebGLProgram;
  try {
    update = program(gl, UPDATE_VS, UPDATE_FS, ["vState"]);
    decay = program(gl, QUAD_VS, DECAY_FS);
    show = program(gl, QUAD_VS, SHOW_FS);
  } catch (err) {
    console.warn("flame: falling back", err);
    return null;
  }
  const U = (p: WebGLProgram, n: string) => gl.getUniformLocation(p, n);
  const u = {
    count: U(update, "uCount"), cum: U(update, "uCum"), row0: U(update, "uRow0"), row1: U(update, "uRow1"),
    va: U(update, "uVa"), vb: U(update, "uVb"), color: U(update, "uColor"), flare: U(update, "uFlare"),
    flareColor: U(update, "uFlareColor"), sym: U(update, "uSym"), seed: U(update, "uSeed"),
    view: U(update, "uView"), aspect: U(update, "uAspect"), pal: U(update, "uPal"),
    decay: U(decay, "uDecay"), accum: U(show, "uAccum"), mean: U(show, "uMean"), gain: U(show, "uGain"),
  };

  // --- Point state: two buffers, ping-ponged through transform feedback.
  const init = new Float32Array(MAX_POINTS * 4);
  for (let i = 0; i < MAX_POINTS; i++) {
    init[i * 4] = Math.random() * 2 - 1;
    init[i * 4 + 1] = Math.random() * 2 - 1;
    init[i * 4 + 2] = Math.random();
    init[i * 4 + 3] = 0;
  }
  const bufs = [gl.createBuffer()!, gl.createBuffer()!];
  const vaos = bufs.map((b) => {
    gl.bindBuffer(gl.ARRAY_BUFFER, b);
    gl.bufferData(gl.ARRAY_BUFFER, init, gl.DYNAMIC_COPY);
    const vao = gl.createVertexArray()!;
    gl.bindVertexArray(vao);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 4, gl.FLOAT, false, 0, 0);
    return vao;
  });
  gl.bindVertexArray(null);
  // A buffer may not sit on another binding point while used for feedback.
  gl.bindBuffer(gl.ARRAY_BUFFER, null);
  const tf = gl.createTransformFeedback()!;
  const emptyVao = gl.createVertexArray()!;

  // --- Histogram target (RGBA16F, mipmapped for the mean-density read).
  let accum: WebGLTexture | null = null;
  let fbo: WebGLFramebuffer | null = null;
  let width = 0, height = 0, aspect = 1;
  function resize(): void {
    const dpr = Math.min(window.devicePixelRatio || 1, 1.5);
    width = Math.max(1, Math.round(window.innerWidth * dpr));
    height = Math.max(1, Math.round(window.innerHeight * dpr));
    aspect = window.innerWidth / Math.max(1, window.innerHeight);
    canvas.width = width;
    canvas.height = height;
    if (accum) gl!.deleteTexture(accum);
    if (fbo) gl!.deleteFramebuffer(fbo);
    accum = gl!.createTexture();
    gl!.bindTexture(gl!.TEXTURE_2D, accum);
    const levels = Math.floor(Math.log2(Math.max(width, height))) + 1;
    gl!.texStorage2D(gl!.TEXTURE_2D, levels, gl!.RGBA16F, width, height);
    gl!.texParameteri(gl!.TEXTURE_2D, gl!.TEXTURE_MIN_FILTER, gl!.LINEAR_MIPMAP_LINEAR);
    gl!.texParameteri(gl!.TEXTURE_2D, gl!.TEXTURE_MAG_FILTER, gl!.LINEAR);
    fbo = gl!.createFramebuffer();
    gl!.bindFramebuffer(gl!.FRAMEBUFFER, fbo);
    gl!.framebufferTexture2D(gl!.FRAMEBUFFER, gl!.COLOR_ATTACHMENT0, gl!.TEXTURE_2D, accum, 0);
    gl!.clearColor(0, 0, 0, 0);
    gl!.clear(gl!.COLOR_BUFFER_BIT);
    gl!.bindFramebuffer(gl!.FRAMEBUFFER, null);
  }
  window.addEventListener("resize", resize);
  resize();

  // --- Evolution state.
  const evo = rng(options.seed ?? (Math.random() * 2 ** 32) >>> 0);
  let generation = 0;
  let from: Genome = breed(null, aspect, evo, true);
  let to: Genome = breed(from, aspect, evo, false);
  let genStart = performance.now();
  const flares = new Array<{ until: number; color: [number, number, number]; strength: number }>(MAX_XFORMS)
    .fill({ until: 0, color: [1, 1, 1], strength: 0 });
  let points = 1 << 19;
  let frameAvg = 16;
  let src = 0;
  let seed = 1;

  const cum = new Float32Array(MAX_XFORMS);
  const row0 = new Float32Array(MAX_XFORMS * 3);
  const row1 = new Float32Array(MAX_XFORMS * 3);
  const va = new Float32Array(MAX_XFORMS * 4);
  const vb = new Float32Array(MAX_XFORMS * 4);
  const colors = new Float32Array(MAX_XFORMS);
  const flare = new Float32Array(MAX_XFORMS);
  const flareColor = new Float32Array(MAX_XFORMS * 3);
  const pal = new Float32Array(12);
  const meanPixel = new Float32Array(4);
  let meanDensity = 1;
  let meanFrame = 0;

  const smooth = (t: number) => t * t * (3 - 2 * t);

  function uploadGenome(g: Genome, now: number, seconds: number): void {
    let total = 0;
    const weights = g.xforms.map((x, i) => {
      const f = flares[i];
      const fl = now < f.until ? f.strength * (1 - (now - (f.until - FLARE_MS)) / FLARE_MS) : 0;
      flare[i] = Math.max(0, fl);
      flareColor.set(f.color, i * 3);
      const w = x.weight * (1 + 1.5 * flare[i]);
      total += w;
      return w;
    });
    let acc = 0;
    g.xforms.forEach((x, i) => {
      acc += weights[i];
      cum[i] = total > 0 ? acc / total : 1;
      // Animate: slowly rotate the linear part of spinning transforms.
      const ang = x.spin * seconds;
      const c = Math.cos(ang), s = Math.sin(ang);
      row0.set([x.a * c - x.d * s, x.b * c - x.e * s, x.c], i * 3);
      row1.set([x.a * s + x.d * c, x.b * s + x.e * c, x.f], i * 3);
      for (let k = 0; k < 4; k++) {
        va[i * 4 + k] = x.v[k] ?? 0;
        vb[i * 4 + k] = x.v[k + 4] ?? 0;
      }
      colors[i] = x.color;
    });
    g.pal.forEach((row, i) => pal.set(row, i * 3));
    gl!.uniform1i(u.count, MAX_XFORMS);
    gl!.uniform1fv(u.cum, cum);
    gl!.uniform3fv(u.row0, row0);
    gl!.uniform3fv(u.row1, row1);
    gl!.uniform4fv(u.va, va);
    gl!.uniform4fv(u.vb, vb);
    gl!.uniform1fv(u.color, colors);
    gl!.uniform1fv(u.flare, flare);
    gl!.uniform3fv(u.flareColor, flareColor);
    gl!.uniform1i(u.sym, Math.max(1, Math.round(g.symmetry)));
    gl!.uniform4f(u.view, g.cx, g.cy, g.zoom, g.rot + seconds * 0.01);
    gl!.uniform1f(u.aspect, aspect);
    gl!.uniform3fv(u.pal, pal);
  }

  const snapshotWaiters: ((blob: Blob | null) => void)[] = [];

  let last = performance.now();
  function tick(now: number): void {
    const dt = now - last;
    last = now;
    frameAvg = frameAvg * 0.95 + dt * 0.05;
    // Adapt the point budget to the machine (checked about once a second).
    if ((meanFrame++ & 63) === 0) {
      if (frameAvg > 24 && points > 1 << 16) points >>= 1;
      else if (frameAvg < 14 && points < MAX_POINTS) points <<= 1;
    }

    // Advance the generation.
    let t = (now - genStart) / GENERATION_MS;
    if (t >= 1) {
      generation++;
      from = to;
      to = breed(from, aspect, evo, generation % WILD_EVERY === 0);
      genStart = now;
      t = 0;
    }
    const g = interpolate(from, to, smooth(t));

    gl!.bindFramebuffer(gl!.FRAMEBUFFER, fbo);
    gl!.viewport(0, 0, width, height);
    gl!.enable(gl!.BLEND);

    // 1. Fade the histogram.
    gl!.useProgram(decay);
    gl!.uniform1f(u.decay, DECAY);
    gl!.blendFuncSeparate(gl!.ZERO, gl!.SRC_ALPHA, gl!.ZERO, gl!.SRC_ALPHA);
    gl!.bindVertexArray(emptyVao);
    gl!.drawArrays(gl!.TRIANGLES, 0, 3);

    // 2. Step every point and splat it (transform feedback + rasterize).
    gl!.useProgram(update);
    uploadGenome(g, now, now / 1000);
    gl!.uniform1ui(u.seed, (seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0));
    gl!.blendFunc(gl!.ONE, gl!.ONE);
    const dst = 1 - src;
    gl!.bindVertexArray(vaos[src]);
    gl!.bindTransformFeedback(gl!.TRANSFORM_FEEDBACK, tf);
    gl!.bindBufferBase(gl!.TRANSFORM_FEEDBACK_BUFFER, 0, bufs[dst]);
    gl!.beginTransformFeedback(gl!.POINTS);
    gl!.drawArrays(gl!.POINTS, 0, points);
    gl!.endTransformFeedback();
    gl!.bindBufferBase(gl!.TRANSFORM_FEEDBACK_BUFFER, 0, null);
    gl!.bindTransformFeedback(gl!.TRANSFORM_FEEDBACK, null);
    src = dst;

    // 3. Mean density (top mip level), read back a few times a second.
    gl!.bindTexture(gl!.TEXTURE_2D, accum);
    gl!.generateMipmap(gl!.TEXTURE_2D);
    if ((meanFrame & 15) === 0) {
      // A 1×1 read of the last mip level via a temporary framebuffer.
      const level = Math.floor(Math.log2(Math.max(width, height)));
      const tmp = gl!.createFramebuffer();
      gl!.bindFramebuffer(gl!.FRAMEBUFFER, tmp);
      gl!.framebufferTexture2D(gl!.FRAMEBUFFER, gl!.COLOR_ATTACHMENT0, gl!.TEXTURE_2D, accum, level);
      gl!.readPixels(0, 0, 1, 1, gl!.RGBA, gl!.FLOAT, meanPixel);
      gl!.deleteFramebuffer(tmp);
      // Assume lit pixels are ~20% of the frame; tone map is relative to them.
      if (meanPixel[3] > 0) meanDensity = meanDensity * 0.7 + (meanPixel[3] / 0.2) * 0.3;
    }

    // 4. Tone map to the screen.
    gl!.bindFramebuffer(gl!.FRAMEBUFFER, null);
    gl!.viewport(0, 0, width, height);
    gl!.disable(gl!.BLEND);
    gl!.useProgram(show);
    gl!.activeTexture(gl!.TEXTURE0);
    gl!.bindTexture(gl!.TEXTURE_2D, accum);
    gl!.uniform1i(u.accum, 0);
    gl!.uniform1f(u.mean, meanDensity);
    gl!.uniform1f(u.gain, 2.0);
    gl!.bindVertexArray(emptyVao);
    gl!.drawArrays(gl!.TRIANGLES, 0, 3);
    gl!.bindVertexArray(null);

    // The drawing buffer is only valid until this task ends, so capture here.
    if (snapshotWaiters.length > 0) {
      const waiters = snapshotWaiters.splice(0);
      canvas.toBlob((blob) => waiters.forEach((resolve) => resolve(blob)), "image/png");
    }

    requestAnimationFrame(tick);
  }
  requestAnimationFrame(tick);

  return {
    snapshot(): Promise<Blob | null> {
      return new Promise((resolve) => snapshotWaiters.push(resolve));
    },
    pulse(id: string, amount: number): void {
      const h = hash(id);
      const now = performance.now();
      const active = to.xforms.map((x, i) => (x.weight > 0 || from.xforms[i].weight > 0 ? i : -1)).filter((i) => i >= 0);
      if (active.length === 0) return;
      const k = active[h % active.length];
      // Strength grows with amount (log scale, 100 sats → ~0.6, 1M sats → 1).
      const strength = Math.min(1, 0.45 + Math.log10(Math.max(1, amount)) / 12);
      // Flare in a hue roughly opposite the transform's own palette colour.
      const hue = (to.xforms[k].color + 0.5 + ((h >>> 8) % 100) / 400) % 1;
      const rgb = [0, 1 / 3, 2 / 3].map((o) => 0.55 + 0.45 * Math.cos(2 * Math.PI * (hue + o))) as [number, number, number];
      flares[k] = { until: now + FLARE_MS, color: rgb, strength };
      nudge(to, h, strength);
    },
  };
}
