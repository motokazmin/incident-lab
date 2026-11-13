// Сборка: блоки из промежуточных файлов в mp4, склейка черновика из всех блоков в папке,
// кадры-превью каждые 4 секунды с именем блока и временем внутри него.
import { execFileSync } from "node:child_process";
import { mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const STEP = 4; // секунд между кадрами-превью

const ff = (...args) => execFileSync("ffmpeg", ["-y", "-loglevel", "error", ...args], { stdio: "inherit" });
const duration = (file) => Number(execFileSync("ffprobe", ["-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", file], { encoding: "utf8" }));

/** @param {{ name: string, file: string }[]} recorded только что записанные блоки */
export async function assemble(out, recorded) {
  const blocksDir = join(out, "blocks");
  const framesDir = join(out, "frames");
  mkdirSync(blocksDir, { recursive: true });
  mkdirSync(framesDir, { recursive: true });

  for (const b of recorded) {
    const mp4 = join(blocksDir, `${b.name}.mp4`);
    console.log(`сборка ${b.name}.mp4`);
    ff("-i", b.file, "-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-r", "30", "-movflags", "+faststart", mp4);
    for (const f of readdirSync(framesDir)) if (f.startsWith(`${b.name}_`)) rmSync(join(framesDir, f));
    const len = duration(mp4);
    const marks = [];
    for (let t = 0.5; t < len - 0.5; t += STEP) marks.push(t);
    marks.push(Math.max(0, len - 0.2)); // последний кадр блока — то, на чём он кончается
    for (const t of marks) {
      ff("-ss", t.toFixed(2), "-i", mp4, "-frames:v", "1", "-q:v", "3",
        join(framesDir, `${b.name}_${String(Math.round(t)).padStart(3, "0")}s.jpg`));
    }
  }

  // Черновик — все блоки в папке по порядку номеров: перезапись одного блока не теряет остальные.
  const all = readdirSync(blocksDir).filter((f) => f.endsWith(".mp4")).sort();
  const list = join(out, "parts", "concat.txt");
  writeFileSync(list, all.map((f) => `file '${join(blocksDir, f)}'`).join("\n") + "\n");
  const draft = join(out, "draft.mp4");
  ff("-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-movflags", "+faststart", draft);

  let total = 0;
  for (const f of all) {
    const d = duration(join(blocksDir, f));
    console.log(`${f.padEnd(28)} ${d.toFixed(1)} с  (с ${total.toFixed(1)} с черновика)`);
    total += d;
  }
  console.log(`черновик ${draft}: ${total.toFixed(1)} с`);
}
