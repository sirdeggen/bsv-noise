// Ambient "haystack" backdrop: a field of slowly drifting straws. Each new
// payment replaces a random straw with a glowing needle that cools back into
// the hay — a visual for real commerce disappearing into background traffic.

interface Straw {
  x: number;
  y: number;
  len: number;
  angle: number;
  vx: number;
  vy: number;
  va: number;
  heat: number;
}

const HAY = "rgba(232, 222, 205, 0.09)";
const NEEDLE = [214, 122, 82] as const;
const COOL_PER_SECOND = 0.22;

export interface Field {
  needle(): void;
}

export function createField(canvas: HTMLCanvasElement): Field {
  const ctx = canvas.getContext("2d")!;
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  let width = 0;
  let height = 0;
  let straws: Straw[] = [];

  function makeStraw(): Straw {
    const drift = reduced ? 0 : 1;
    return {
      x: Math.random() * width,
      y: Math.random() * height,
      len: 14 + Math.random() * 46,
      angle: Math.random() * Math.PI,
      vx: (Math.random() - 0.5) * 6 * drift,
      vy: (Math.random() - 0.5) * 6 * drift,
      va: (Math.random() - 0.5) * 0.08 * drift,
      heat: 0,
    };
  }

  function resize(): void {
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    width = window.innerWidth;
    height = window.innerHeight;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const target = Math.max(140, Math.min(420, Math.round((width * height) / 7000)));
    while (straws.length < target) straws.push(makeStraw());
    straws.length = target;
  }

  function line(s: Straw): void {
    const dx = (Math.cos(s.angle) * s.len) / 2;
    const dy = (Math.sin(s.angle) * s.len) / 2;
    ctx.moveTo(s.x - dx, s.y - dy);
    ctx.lineTo(s.x + dx, s.y + dy);
  }

  let last = performance.now();
  function frame(now: number): void {
    const dt = Math.min(0.1, (now - last) / 1000);
    last = now;
    ctx.clearRect(0, 0, width, height);

    ctx.lineWidth = 1;
    ctx.lineCap = "round";
    ctx.strokeStyle = HAY;
    ctx.beginPath();
    const hot: Straw[] = [];
    for (const s of straws) {
      s.x += s.vx * dt;
      s.y += s.vy * dt;
      s.angle += s.va * dt;
      if (s.x < -40) s.x += width + 80;
      else if (s.x > width + 40) s.x -= width + 80;
      if (s.y < -40) s.y += height + 80;
      else if (s.y > height + 40) s.y -= height + 80;
      if (s.heat > 0.01) {
        s.heat = Math.max(0, s.heat - COOL_PER_SECOND * dt * (0.4 + s.heat));
        hot.push(s);
      } else {
        line(s);
      }
    }
    ctx.stroke();

    for (const s of hot) {
      const [r, g, b] = NEEDLE;
      ctx.strokeStyle = `rgba(${r}, ${g}, ${b}, ${0.09 + s.heat * 0.85})`;
      ctx.lineWidth = 1 + s.heat * 0.8;
      ctx.shadowColor = `rgba(${r}, ${g}, ${b}, ${s.heat * 0.7})`;
      ctx.shadowBlur = 14 * s.heat;
      ctx.beginPath();
      line(s);
      ctx.stroke();
    }
    ctx.shadowBlur = 0;
    requestAnimationFrame(frame);
  }

  window.addEventListener("resize", resize);
  resize();
  requestAnimationFrame(frame);

  return {
    needle(): void {
      if (straws.length === 0) return;
      const s = makeStraw();
      s.heat = 1;
      s.len = 36 + Math.random() * 30;
      straws[Math.floor(Math.random() * straws.length)] = s;
    },
  };
}
