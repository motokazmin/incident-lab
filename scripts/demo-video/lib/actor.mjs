// «Актёр»: всё, что зритель видит как действия человека, — курсор, клики, набор, прокрутка.
// В headless курсора нет: рисуем его накладкой на странице и ведём плавно к цели перед кликом.

const CURSOR = `
(() => {
  const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="22" height="30" viewBox="0 0 22 30">'
    + '<path d="M1.5 1.5 L1.5 24 L7 18.8 L10.6 27.5 L14.4 25.9 L10.8 17.4 L18.5 17.4 Z" fill="#fff" stroke="#000" stroke-width="1.6" stroke-linejoin="round"/></svg>';
  const install = () => {
    if (document.getElementById("demo-cursor")) return;
    const style = document.createElement("style");
    style.textContent = "#demo-cursor{position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;width:22px;height:30px;"
      + "filter:drop-shadow(0 1px 2px rgba(0,0,0,.6));will-change:transform}"
      + "#demo-ring{position:fixed;z-index:2147483646;pointer-events:none;width:28px;height:28px;margin:-14px 0 0 -14px;"
      + "border-radius:50%;border:2px solid rgba(255,255,255,.9);opacity:0}"
      + "#demo-ring.on{animation:demo-ring .45s ease-out}"
      + "@keyframes demo-ring{0%{opacity:.9;transform:scale(.4)}100%{opacity:0;transform:scale(1.4)}}";
    const el = document.createElement("div");
    el.id = "demo-cursor";
    el.innerHTML = SVG;
    const ring = document.createElement("div");
    ring.id = "demo-ring";
    document.documentElement.append(style, ring, el);
    let pos = { x: 640, y: 360 };
    try { pos = JSON.parse(sessionStorage.getItem("demo-cursor")) || pos; } catch {}
    const put = (x, y) => {
      pos = { x, y };
      el.style.transform = "translate(" + x + "px," + y + "px)";
      try { sessionStorage.setItem("demo-cursor", JSON.stringify(pos)); } catch {}
    };
    put(pos.x, pos.y);
    window.__demoCursor = {
      get: () => pos,
      move: (x, y, ms) => new Promise((done) => {
        const from = { ...pos }, start = performance.now();
        const ease = (t) => (t < .5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
        const step = (now) => {
          const k = Math.min(1, (now - start) / ms), e = ease(k);
          put(from.x + (x - from.x) * e, from.y + (y - from.y) * e);
          if (k < 1) requestAnimationFrame(step); else done();
        };
        requestAnimationFrame(step);
      }),
      ring: () => {
        ring.style.left = pos.x + "px"; ring.style.top = pos.y + "px";
        ring.classList.remove("on"); void ring.offsetWidth; ring.classList.add("on");
      },
    };
  };
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", install); else install();
})();
`;

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const rand = (a, b) => a + Math.random() * (b - a);

export class Actor {
  /** @param {import("puppeteer-core").Page} page */
  constructor(page) { this.page = page; }

  async install() {
    await this.page.evaluateOnNewDocument(CURSOR);
    await this.page.evaluate(CURSOR).catch(() => {});
  }

  hold(sec) { return sleep(sec * 1000); }

  /** Центр элемента (или точка внутри него по долям ширины и высоты) в CSS-пикселях. */
  async point(selector, { fx = 0.5, fy = 0.5, text } = {}) {
    const handle = typeof selector !== "string" ? selector
      : text ? await this.byText(selector, text) : await this.page.waitForSelector(selector, { visible: true, timeout: 30000 });
    const box = await handle.boundingBox();
    if (!box) throw new Error(`элемент без размеров: ${selector}`);
    return { x: box.x + box.width * fx, y: box.y + box.height * fy };
  }

  /** Элемент по селектору и видимому тексту. */
  async byText(selector, text) {
    const handle = await this.page.waitForFunction((sel, t) =>
      [...document.querySelectorAll(sel)].find((e) => e.offsetParent !== null && e.textContent.includes(t)),
    { timeout: 30000 }, selector, text);
    return handle.asElement();
  }

  /** Плавно ведёт курсор к точке или элементу. Время — от расстояния, как у руки. */
  async moveTo(target, opts = {}) {
    const p = typeof target.x === "number" ? target : await this.point(target, opts);
    const from = await this.page.evaluate(() => window.__demoCursor.get());
    const dist = Math.hypot(p.x - from.x, p.y - from.y);
    const ms = opts.ms ?? Math.min(1300, Math.max(350, 250 + dist * 1.1));
    await this.page.evaluate((x, y, t) => window.__demoCursor.move(x, y, t), p.x, p.y, ms);
    await this.page.mouse.move(p.x, p.y);
    return p;
  }

  async click(target, opts = {}) {
    const p = await this.moveTo(target, opts);
    await sleep(rand(180, 280));
    await this.page.evaluate(() => window.__demoCursor.ring());
    await this.page.mouse.click(p.x, p.y);
    await sleep(250);
  }

  /** Набор с человеческой скоростью: неровный темп, при медленном наборе паузы на пробелах.
   *  Темп держится по часам: задержка доставки клавиши в браузер не замедляет набор. */
  async type(text, { cps = 11 } = {}) {
    const base = 1000 / cps;
    const start = Date.now();
    let at = 0;
    for (const ch of text) {
      if (ch === "\n") await this.page.keyboard.press("Enter");
      else await this.page.keyboard.type(ch);
      at += rand(base * 0.6, base * 1.4) + (ch === " " && cps < 15 ? rand(40, 140) : 0);
      await sleep(Math.max(0, start + at - Date.now()));
    }
  }

  async press(key, times = 1, gap = 110) {
    for (let i = 0; i < times; i++) {
      await this.page.keyboard.press(key);
      await sleep(rand(gap * 0.7, gap * 1.3));
    }
  }

  async chord(mod, key) {
    await this.page.keyboard.down(mod);
    await sleep(60);
    await this.page.keyboard.press(key);
    await sleep(60);
    await this.page.keyboard.up(mod);
  }

  /** Плавно прокручивает контейнер так, чтобы элемент оказался на доле высоты at от верха. */
  async scrollTo(container, target, { at = 0.08, ms = 1400, text } = {}) {
    const el = typeof target !== "string" ? target : text ? await this.byText(target, text) : await this.page.waitForSelector(target);
    await this.page.evaluate((sel, node, at, ms) => new Promise((done) => {
      const box = document.querySelector(sel);
      const top = node.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop - box.clientHeight * at;
      const to = Math.max(0, Math.min(top, box.scrollHeight - box.clientHeight));
      const from = box.scrollTop, start = performance.now();
      const ease = (t) => (t < .5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);
      const step = (now) => {
        const k = Math.min(1, (now - start) / ms);
        box.scrollTop = from + (to - from) * ease(k);
        if (k < 1) requestAnimationFrame(step); else done();
      };
      requestAnimationFrame(step);
    }), container, el, at, ms);
  }

  /** Плавная прокрутка контейнера на dy пикселей. */
  async scrollBy(container, dy, { ms = 1400 } = {}) {
    await this.page.evaluate((sel, dy, ms) => new Promise((done) => {
      const box = document.querySelector(sel);
      const from = box.scrollTop, to = Math.max(0, Math.min(from + dy, box.scrollHeight - box.clientHeight));
      const start = performance.now();
      const ease = (t) => (t < .5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);
      const step = (now) => {
        const k = Math.min(1, (now - start) / ms);
        box.scrollTop = from + (to - from) * ease(k);
        if (k < 1) requestAnimationFrame(step); else done();
      };
      requestAnimationFrame(step);
    }), container, dy, ms);
  }
}
