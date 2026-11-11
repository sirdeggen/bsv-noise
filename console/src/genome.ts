// Fractal-flame genomes, after Scott Draves' flame algorithm (the idea behind
// Electric Sheep / infinidream). Written from the published algorithm; no code
// is taken from those projects.
//
// A genome is a small iterated function system: up to MAX_XFORMS transforms,
// each an affine map followed by a weighted blend of non-linear "variations".
// Every genome uses all slots; inactive ones have weight 0, so any two genomes
// can be interpolated smoothly and transforms fade in and out.

export const MAX_XFORMS = 6;
export const VARIATIONS = ["linear", "sinusoidal", "spherical", "swirl", "horseshoe", "polar", "disc", "heart"] as const;
export const NV = VARIATIONS.length;

export interface Xform {
  weight: number;
  /** x' = a·x + b·y + c ; y' = d·x + e·y + f */
  a: number; b: number; c: number;
  d: number; e: number; f: number;
  /** Variation weights, length NV. */
  v: number[];
  /** Position on the palette, 0..1. */
  color: number;
  /** Slow rotation of the linear part, radians per second (animation). */
  spin: number;
}

export interface Genome {
  xforms: Xform[];
  /** Rotational symmetry order; 1 = none. Interpolated, then rounded. */
  symmetry: number;
  /** Cosine palette: color(t) = a + b·cos(2π(c·t + d)). */
  pal: [number, number, number][];
  /** Camera: centre, zoom (screen half-heights per unit), rotation. */
  cx: number; cy: number; zoom: number; rot: number;
}

export type Rng = () => number;

/** Small fast seeded PRNG (mulberry32). */
export function rng(seed: number): Rng {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Stable 32-bit hash of a string (FNV-1a). */
export function hash(text: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

const range = (r: Rng, lo: number, hi: number) => lo + (hi - lo) * r();

function emptyXform(): Xform {
  return { weight: 0, a: 1, b: 0, c: 0, d: 0, e: 1, f: 0, v: [1, 0, 0, 0, 0, 0, 0, 0], color: 0, spin: 0 };
}

function randomAffine(r: Rng, x: Xform): void {
  const t1 = range(r, 0, Math.PI * 2);
  const t2 = range(r, 0, Math.PI * 2);
  const s1 = range(r, 0.35, 0.95) * (r() < 0.2 ? -1 : 1);
  const s2 = range(r, 0.25, 0.9);
  // R(t1) · diag(s1, s2) · R(t2)
  const c1 = Math.cos(t1), n1 = Math.sin(t1), c2 = Math.cos(t2), n2 = Math.sin(t2);
  x.a = c1 * s1 * c2 - n1 * s2 * n2;
  x.b = -c1 * s1 * n2 - n1 * s2 * c2;
  x.d = n1 * s1 * c2 + c1 * s2 * n2;
  x.e = -n1 * s1 * n2 + c1 * s2 * c2;
  x.c = range(r, -1, 1);
  x.f = range(r, -1, 1);
}

function randomVariations(r: Rng): number[] {
  const v = new Array<number>(NV).fill(0);
  const picks = r() < 0.55 ? 1 : 2;
  for (let i = 0; i < picks; i++) {
    const k = r() < 0.25 ? 0 : Math.floor(r() * NV);
    v[k] += range(r, 0.4, 1);
  }
  const sum = v.reduce((s, x) => s + x, 0);
  return v.map((x) => x / sum);
}

function randomPalette(r: Rng): Genome["pal"] {
  return [
    [range(r, 0.4, 0.6), range(r, 0.4, 0.6), range(r, 0.4, 0.6)],
    [range(r, 0.35, 0.55), range(r, 0.35, 0.55), range(r, 0.35, 0.55)],
    [range(r, 0.5, 1.6), range(r, 0.5, 1.6), range(r, 0.5, 1.6)],
    [r(), r(), r()],
  ];
}

export function randomGenome(r: Rng): Genome {
  const n = 2 + Math.floor(r() * 3) + (r() < 0.3 ? 1 : 0); // 2..5
  const xforms: Xform[] = [];
  for (let i = 0; i < MAX_XFORMS; i++) {
    const x = emptyXform();
    if (i < n) {
      x.weight = range(r, 0.25, 1);
      randomAffine(r, x);
      x.v = randomVariations(r);
      x.color = r();
      x.spin = r() < 0.4 ? range(r, -0.06, 0.06) : 0;
    }
    xforms.push(x);
  }
  const symRoll = r();
  const symmetry = symRoll < 0.45 ? 1 : symRoll < 0.6 ? 2 : Math.floor(range(r, 3, 7));
  return { xforms, symmetry, pal: randomPalette(r), cx: 0, cy: 0, zoom: 0.5, rot: 0 };
}

export function cloneGenome(g: Genome): Genome {
  return {
    ...g,
    xforms: g.xforms.map((x) => ({ ...x, v: [...x.v] })),
    pal: g.pal.map((p) => [...p] as [number, number, number]),
  };
}

/** A child of `g`: small structural and colour changes, sometimes bigger ones. */
export function mutate(g: Genome, r: Rng): Genome {
  const out = cloneGenome(g);
  const active = out.xforms.filter((x) => x.weight > 0);
  const roll = r();
  if (roll < 0.35) {
    // Re-shape one transform.
    const x = active[Math.floor(r() * active.length)];
    const fresh = emptyXform();
    randomAffine(r, fresh);
    const t = range(r, 0.3, 0.7);
    for (const k of ["a", "b", "c", "d", "e", "f"] as const) x[k] = x[k] * (1 - t) + fresh[k] * t;
  } else if (roll < 0.55) {
    // Swap the variation blend on one transform.
    active[Math.floor(r() * active.length)].v = randomVariations(r);
  } else if (roll < 0.7 && active.length < MAX_XFORMS) {
    // Grow a transform into an empty slot.
    const slot = out.xforms.find((x) => x.weight === 0)!;
    slot.weight = range(r, 0.2, 0.7);
    randomAffine(r, slot);
    slot.v = randomVariations(r);
    slot.color = r();
  } else if (roll < 0.8 && active.length > 2) {
    // Retire a transform.
    active[Math.floor(r() * active.length)].weight = 0;
  } else if (roll < 0.9) {
    out.symmetry = r() < 0.4 ? 1 : Math.floor(range(r, 2, 7));
  }
  // Always drift the palette and nudge every active transform a little.
  out.pal = out.pal.map((row, i) =>
    row.map((v) => (i === 3 ? (v + range(r, -0.25, 0.25) + 1) % 1 : v + range(r, -0.08, 0.08))) as [number, number, number],
  );
  for (const x of out.xforms) {
    if (x.weight === 0) continue;
    x.c += range(r, -0.1, 0.1);
    x.f += range(r, -0.1, 0.1);
    x.color = (x.color + range(r, -0.15, 0.15) + 1) % 1;
    x.spin = r() < 0.3 ? range(r, -0.06, 0.06) : x.spin;
  }
  return out;
}

/** A small, deterministic nudge from one payment. */
export function nudge(g: Genome, seed: number, strength: number): void {
  const r = rng(seed);
  const active = g.xforms.filter((x) => x.weight > 0);
  if (active.length === 0) return;
  const x = active[Math.floor(r() * active.length)];
  const s = 0.04 * strength;
  x.a += range(r, -s, s);
  x.b += range(r, -s, s);
  x.d += range(r, -s, s);
  x.e += range(r, -s, s);
  x.color = (x.color + range(r, -0.05, 0.05) + 1) % 1;
  g.pal[3] = g.pal[3].map((v) => (v + range(r, -0.02, 0.02) * strength + 1) % 1) as [number, number, number];
}

const lerp = (a: number, b: number, t: number) => a + (b - a) * t;

/** Palette phase wraps; take the short way round. */
const lerpWrap = (a: number, b: number, t: number) => {
  let d = b - a;
  if (d > 0.5) d -= 1;
  if (d < -0.5) d += 1;
  return (a + d * t + 1) % 1;
};

export function interpolate(a: Genome, b: Genome, t: number): Genome {
  return {
    xforms: a.xforms.map((xa, i) => {
      const xb = b.xforms[i];
      // A transform appearing or disappearing keeps the other side's shape.
      const sa = xa.weight > 0 ? xa : xb;
      const sb = xb.weight > 0 ? xb : xa;
      return {
        weight: lerp(xa.weight, xb.weight, t),
        a: lerp(sa.a, sb.a, t), b: lerp(sa.b, sb.b, t), c: lerp(sa.c, sb.c, t),
        d: lerp(sa.d, sb.d, t), e: lerp(sa.e, sb.e, t), f: lerp(sa.f, sb.f, t),
        v: sa.v.map((v, k) => lerp(v, sb.v[k], t)),
        color: lerpWrap(sa.color, sb.color, t),
        spin: lerp(sa.spin, sb.spin, t),
      };
    }),
    symmetry: lerp(a.symmetry, b.symmetry, t),
    pal: a.pal.map((row, i) => row.map((v, k) => (i === 3 ? lerpWrap(v, b.pal[i][k], t) : lerp(v, b.pal[i][k], t))) as [number, number, number]),
    cx: lerp(a.cx, b.cx, t),
    cy: lerp(a.cy, b.cy, t),
    zoom: Math.exp(lerp(Math.log(a.zoom), Math.log(b.zoom), t)),
    rot: lerp(a.rot, b.rot, t),
  };
}

// --- CPU evaluation (mirrors the GLSL in flame.ts) ----------------------------

export function variation(k: number, x: number, y: number): [number, number] {
  const r2 = x * x + y * y + 1e-9;
  const r = Math.sqrt(r2);
  const theta = Math.atan2(x, y);
  switch (k) {
    case 0: return [x, y];
    case 1: return [Math.sin(x), Math.sin(y)];
    case 2: return [x / r2, y / r2];
    case 3: { const s = Math.sin(r2), c = Math.cos(r2); return [x * s - y * c, x * c + y * s]; }
    case 4: return [((x - y) * (x + y)) / r, (2 * x * y) / r];
    case 5: return [theta / Math.PI, r - 1];
    case 6: { const t = theta / Math.PI; return [t * Math.sin(Math.PI * r), t * Math.cos(Math.PI * r)]; }
    default: return [r * Math.sin(theta * r), -r * Math.cos(theta * r)];
  }
}

/**
 * Runs the chaos game on the CPU. Returns null for degenerate genomes
 * (collapsed to a point/line, or escaping); otherwise sets the camera so the
 * attractor fills the screen and returns the genome.
 */
export function frame(g: Genome, aspect: number, r: Rng): Genome | null {
  const xs = g.xforms;
  const total = xs.reduce((s, x) => s + x.weight, 0);
  if (total <= 0) return null;
  const sym = Math.max(1, Math.round(g.symmetry));
  const N = 12000;
  const px: number[] = [];
  const py: number[] = [];
  let x = r() * 2 - 1, y = r() * 2 - 1;
  let escaped = 0;
  for (let i = 0; i < N + 20; i++) {
    let u = r() * total;
    let k = 0;
    while (k < xs.length - 1 && u > xs[k].weight) u -= xs[k++].weight;
    const t = xs[k];
    const ax = t.a * x + t.b * y + t.c;
    const ay = t.d * x + t.e * y + t.f;
    let nx = 0, ny = 0;
    for (let v = 0; v < NV; v++) {
      if (t.v[v] === 0) continue;
      const [vx, vy] = variation(v, ax, ay);
      nx += t.v[v] * vx;
      ny += t.v[v] * vy;
    }
    if (sym > 1) {
      const ang = (2 * Math.PI * Math.floor(r() * sym)) / sym;
      const c = Math.cos(ang), s = Math.sin(ang);
      [nx, ny] = [nx * c - ny * s, nx * s + ny * c];
    }
    if (!Number.isFinite(nx) || !Number.isFinite(ny) || Math.abs(nx) > 1e4 || Math.abs(ny) > 1e4) {
      escaped++;
      x = r() * 2 - 1;
      y = r() * 2 - 1;
      continue;
    }
    x = nx;
    y = ny;
    if (i >= 20) {
      px.push(x);
      py.push(y);
    }
  }
  if (escaped > N * 0.05 || px.length < N * 0.9) return null;

  const sx = [...px].sort((p, q) => p - q);
  const sy = [...py].sort((p, q) => p - q);
  const lo = Math.floor(sx.length * 0.02), hi = Math.floor(sx.length * 0.98);
  const x0 = sx[lo], x1 = sx[hi], y0 = sy[lo], y1 = sy[hi];
  const w = x1 - x0, h = y1 - y0;
  if (!(w > 1e-3 && h > 1e-3) || w > 1e3 || h > 1e3) return null;

  // Coverage: how much of the bounding box the attractor actually fills.
  const G = 48;
  const cells = new Uint8Array(G * G);
  for (let i = 0; i < px.length; i++) {
    const gx = Math.floor(((px[i] - x0) / w) * G);
    const gy = Math.floor(((py[i] - y0) / h) * G);
    if (gx >= 0 && gx < G && gy >= 0 && gy < G) cells[gy * G + gx] = 1;
  }
  const coverage = cells.reduce((s, c) => s + c, 0) / (G * G);
  if (coverage < 0.06) return null;

  g.cx = (x0 + x1) / 2;
  g.cy = (y0 + y1) / 2;
  // Fit with a margin: zoom is clip-space units per world unit on the y axis.
  g.zoom = 1.7 / Math.max(h, w / aspect);
  return g;
}

/** Tries random children (or fresh genomes) until one survives framing. */
export function breed(parent: Genome | null, aspect: number, r: Rng, wild: boolean): Genome {
  for (let i = 0; i < 60; i++) {
    const child = parent && !wild ? mutate(parent, r) : randomGenome(r);
    if (frame(child, aspect, r)) {
      child.rot = parent ? parent.rot + range(r, -0.6, 0.6) : r() * Math.PI * 2;
      return child;
    }
  }
  // Fall back to a known-good shape: a Sierpinski-like triangle.
  const g = randomGenome(r);
  g.symmetry = 1;
  g.xforms.forEach((x, i) => {
    Object.assign(x, emptyXform());
    if (i < 3) {
      x.weight = 1;
      x.a = 0.5; x.e = 0.5;
      x.c = [0, 0.5, 0.25][i]; x.f = [0, 0, 0.45][i];
      x.color = i / 3;
    }
  });
  frame(g, aspect, r);
  return g;
}
