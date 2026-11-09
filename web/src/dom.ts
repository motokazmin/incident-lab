import DOMPurify from "dompurify";
import { marked } from "marked";

type Attrs = Record<string, string | boolean | ((e: Event) => void)>;

/** Создаёт элемент. Текст вставляется только как текст: данные из песочницы не станут разметкой. */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K, attrs: Attrs = {}, ...kids: (Node | string | null)[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (typeof v === "function") el.addEventListener(k, v);
    else if (v === true) el.setAttribute(k, "");
    else if (v !== false) el.setAttribute(k, v);
  }
  el.append(...kids.filter((k): k is Node | string => k !== null));
  return el;
}

export function inline(text: string): (Node | string)[] {
  return text.split(/(`[^`]+`|\*\*[^*]+\*\*)/).filter(Boolean).map((part) => {
    if (part.startsWith("`")) return h("code", {}, part.slice(1, -1));
    if (part.startsWith("**")) return h("strong", {}, part.slice(2, -2));
    return part;
  });
}

/** Markdown текстов сюжета: брифинг, подсказки, разбор. Тексты из репозитория, а не из песочницы;
 *  HTML всё равно проходит через DOMPurify. Таблицы обёрнуты ради горизонтальной прокрутки. */
export function markdown(src: string): HTMLElement {
  const root = h("div", { class: "prose" });
  root.innerHTML = DOMPurify.sanitize(marked.parse(src, { async: false }));
  root.querySelectorAll("table").forEach((t) => {
    const wrap = h("div", { class: "table" });
    t.replaceWith(wrap);
    wrap.append(t);
  });
  return root;
}
