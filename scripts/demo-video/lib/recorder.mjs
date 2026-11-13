// Запись вкладки в видео: кадры CDP-скринкаста, ровный темп через повтор последнего кадра, ffmpeg.
// Пауза не пишет кадры, но держит последние секунды в памяти: при продолжении их можно вернуть
// в ролик (`resume({ backfill })`), чтобы момент, которого ждали, не выпал из кадра.
import { spawn } from "node:child_process";

export class Recorder {
  /** @param {import("puppeteer-core").Page} page */
  constructor(page, { fps = 30, width = 1920, height = 1080, quality = 92 } = {}) {
    this.page = page;
    this.opts = { fps, width, height, quality };
  }

  /** Начинает файл. Промежуточный формат — mkv с быстрым кодеком, финальное сжатие делает сборка. */
  async start(file) {
    const { fps, width, height, quality } = this.opts;
    this.ff = spawn("ffmpeg", [
      "-y", "-loglevel", "error",
      "-f", "image2pipe", "-c:v", "mjpeg", "-framerate", String(fps), "-i", "-",
      "-vf", `scale=${width}:${height}:flags=lanczos,format=yuv420p`,
      "-c:v", "libx264", "-preset", "ultrafast", "-crf", "14", "-r", String(fps), file,
    ], { stdio: ["pipe", "inherit", "inherit"] });
    this.done = new Promise((resolve, reject) => {
      this.ff.on("error", reject);
      this.ff.on("close", (code) => (code === 0 ? resolve() : reject(new Error(`ffmpeg: код ${code}`))));
    });
    this.ff.stdin.on("error", () => {}); // причина придёт через done
    this.written = 0; // кадров в файле
    this.received = 0; // кадров от браузера: мерило плавности
    this.clock = 0; // секунд ролика к моменту mark
    this.mark = now();
    this.paused = false;
    this.buffer = []; // [{ t, data }] за последние секунды паузы
    this.cdp = await this.page.createCDPSession();
    this.cdp.on("Page.screencastFrame", (f) => {
      this.cdp.send("Page.screencastFrameAck", { sessionId: f.sessionId }).catch(() => {});
      this.frame(Buffer.from(f.data, "base64"));
    });
    // Первый кадр берём снимком: на неподвижной странице скринкаст может молчать.
    const { data } = await this.cdp.send("Page.captureScreenshot", { format: "jpeg", quality });
    this.last = Buffer.from(data, "base64");
    await this.cdp.send("Page.startScreencast", { format: "jpeg", quality, maxWidth: width, maxHeight: height, everyNthFrame: 1 });
  }

  /** Время ролика сейчас, в секундах. */
  time() { return this.clock + (this.paused ? 0 : now() - this.mark); }

  frame(data) {
    this.received++;
    if (this.paused) {
      const t = now();
      this.buffer.push({ t, data });
      while (this.buffer.length && this.buffer[0].t < t - 10) this.buffer.shift();
    } else {
      this.fill(this.time());
    }
    this.last = data;
  }

  /** Дописывает последний кадр до момента t ролика. */
  fill(t) {
    const target = Math.floor(t * this.opts.fps);
    for (; this.written < target; this.written++) this.ff.stdin.write(this.last);
  }

  pause() {
    if (this.paused) return;
    this.fill(this.time());
    this.clock = this.time();
    this.paused = true;
    this.buffer = [];
  }

  /** Продолжает запись. backfill — сколько секунд перед продолжением вернуть в ролик из памяти. */
  resume({ backfill = 0 } = {}) {
    if (!this.paused) return;
    const t = now();
    const from = t - backfill;
    const frames = this.buffer.filter((f) => f.t >= from);
    if (frames.length) {
      // Кадр, который был на экране в момент from, — последний до него.
      const before = this.buffer.filter((f) => f.t < from).pop();
      if (before) this.last = before.data;
      for (const f of frames) {
        this.fill(this.clock + (f.t - from));
        this.last = f.data;
      }
      this.clock += t - from;
    }
    this.buffer = [];
    this.mark = now();
    this.paused = false;
  }

  /** Закрывает файл. Возвращает длительность в секундах. */
  async stop() {
    if (!this.paused) this.fill(this.time());
    await this.cdp.send("Page.stopScreencast").catch(() => {});
    await this.cdp.detach().catch(() => {});
    this.ff.stdin.end();
    await this.done;
    return this.written / this.opts.fps;
  }
}

const now = () => performance.now() / 1000;
