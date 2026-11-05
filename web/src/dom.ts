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

/** Абзацы, списки, блоки кода, `код` и **жирный**. Хватает для брифинга, разбора и подсказок. */
export function markdown(src: string): HTMLElement {
  const root = h("div", { class: "prose" });
  for (const block of src.trim().split(/\n\n+/)) {
    const lines = block.split("\n");
    if (lines[0] === "```" && lines[lines.length - 1] === "```") {
      root.append(h("pre", {}, h("code", {}, lines.slice(1, -1).join("\n"))));
    } else if (lines.every((l) => l.startsWith("- "))) {
      root.append(h("ul", {}, ...lines.map((l) => h("li", {}, ...inline(l.slice(2))))));
    } else {
      root.append(h("p", {}, ...inline(block)));
    }
  }
  return root;
}
