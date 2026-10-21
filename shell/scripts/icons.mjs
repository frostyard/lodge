// Draws lodge's home-screen icons as PNGs from the shapes of
// public/icon.svg (ADR-0008: 192 and 512 pixels, and a maskable one), with
// no dependency: a scanline of 4×4 samples per pixel, deflated by zlib.
// Run in the nsl machine: pnpm run icons.
import { writeFileSync } from "node:fs";
import { deflateSync } from "node:zlib";

// Lodge-ui's accent and window background, in the light scheme
// (docs/specs/shell.md, Colours; the manifest names the same colours).
const ACCENT = [0x00, 0x7a, 0xff];
const BACKGROUND = [0xff, 0xff, 0xff];
const HOUSE = [
  [256, 104],
  [72, 264],
  [120, 264],
  [120, 408],
  [392, 408],
  [392, 264],
  [440, 264],
];
const DOOR = [216, 312, 296, 408]; // x0 y0 x1 y1

function inPolygon(x, y, poly) {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const [xi, yi] = poly[i];
    const [xj, yj] = poly[j];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

function inRoundedSquare(x, y, r) {
  const cx = Math.min(Math.max(x, r), 512 - r);
  const cy = Math.min(Math.max(y, r), 512 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r && x >= 0 && x <= 512 && y >= 0 && y <= 512;
}

// sample returns [r, g, b, a] at a point of the 512 × 512 design.
function sample(x, y, maskable) {
  if (!maskable && !inRoundedSquare(x, y, 112)) return [0, 0, 0, 0];
  // A maskable icon fills the square and keeps the drawing in the middle 80 %.
  const [u, v] = maskable ? [(x - 256) / 0.8 + 256, (y - 256) / 0.8 + 256] : [x, y];
  const door = u >= DOOR[0] && u <= DOOR[2] && v >= DOOR[1] && v <= DOOR[3];
  if (!door && inPolygon(u, v, HOUSE)) return [...BACKGROUND, 255];
  return [...ACCENT, 255];
}

function render(size, maskable) {
  const n = 4;
  const rows = [];
  for (let py = 0; py < size; py++) {
    const row = Buffer.alloc(1 + size * 4); // filter byte 0, then RGBA
    for (let px = 0; px < size; px++) {
      const acc = [0, 0, 0, 0];
      for (let sy = 0; sy < n; sy++) {
        for (let sx = 0; sx < n; sx++) {
          const x = ((px + (sx + 0.5) / n) * 512) / size;
          const y = ((py + (sy + 0.5) / n) * 512) / size;
          const [r, g, b, a] = sample(x, y, maskable);
          acc[0] += r * a;
          acc[1] += g * a;
          acc[2] += b * a;
          acc[3] += a;
        }
      }
      const a = acc[3] / (n * n);
      const o = 1 + px * 4;
      if (acc[3] > 0) {
        row[o] = Math.round(acc[0] / acc[3]);
        row[o + 1] = Math.round(acc[1] / acc[3]);
        row[o + 2] = Math.round(acc[2] / acc[3]);
      }
      row[o + 3] = Math.round(a);
    }
    rows.push(row);
  }
  return png(size, size, Buffer.concat(rows));
}

const CRC = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
}

function png(w, h, raw) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const out = new URL("../public/", import.meta.url);
writeFileSync(new URL("icon-192.png", out), render(192, false));
writeFileSync(new URL("icon-512.png", out), render(512, false));
writeFileSync(new URL("icon-maskable-512.png", out), render(512, true));
console.log("wrote public/icon-192.png, icon-512.png, icon-maskable-512.png");
