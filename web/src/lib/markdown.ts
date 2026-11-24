import DOMPurify from 'dompurify';
import hljs from 'highlight.js/lib/core';
import katex from 'katex';
import { Marked, type TokenizerAndRendererExtension } from 'marked';

import bash from 'highlight.js/lib/languages/bash';
import diff from 'highlight.js/lib/languages/diff';
import go from 'highlight.js/lib/languages/go';
import ini from 'highlight.js/lib/languages/ini';
import json from 'highlight.js/lib/languages/json';
import python from 'highlight.js/lib/languages/python';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';

/**
 * An answer body becomes HTML here, and nowhere else.
 *
 * Three stages, in an order that is itself the decision:
 *
 *   marked (GFM + our math lexer) → DOMPurify → hydrate (KaTeX, hljs, links, images)
 *
 * Math is tokenized rather than regexed over the finished string, which is what makes a
 * `$` inside a code span stay a `$`; the token emits an empty `<span data-math>` with the
 * TeX encoded into an attribute, so no TeX reaches the sanitizer, and KaTeX fills the slot
 * afterwards. The sanitizer sees marked's output only — the one HTML in this pipeline that
 * a model wrote.
 *
 * The citation grammar is not here. Chips are attached by `lib/chunks.ts` walking the
 * finished DOM, so it is blind inside `<code>` and `.katex` by construction.
 */

// Curated, because the whole point of a local binary is that nothing arrives later. A
// fence whose language is not here is plain monospace: a confidently wrong language is
// worse than none, so there is no autodetection either.
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('diff', diff);
hljs.registerLanguage('go', go);
hljs.registerLanguage('ini', ini);
hljs.registerLanguage('json', json);
hljs.registerLanguage('python', python);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('yaml', yaml);

/** What a fence may call itself and still get colour. `js` and `ts` alias to typescript. */
const ALIASES: Record<string, string> = {
  sh: 'bash',
  shell: 'bash',
  zsh: 'bash',
  console: 'bash',
  js: 'typescript',
  jsx: 'typescript',
  ts: 'typescript',
  tsx: 'typescript',
  toml: 'ini',
  env: 'ini',
  html: 'xml',
  yml: 'yaml'
};

/**
 * Everything an answer is allowed to be, spelled out rather than inherited from a
 * profile: the default profile would also accept MathML and SVG, which is a way for an
 * ingested document to describe itself as a drawing.
 */
const TAGS = [
  'p', 'br', 'hr', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'strong', 'em', 'del', 'blockquote', 'ul', 'ol', 'li',
  'code', 'pre', 'a', 'img',
  'table', 'thead', 'tbody', 'tr', 'th', 'td',
  'sup', 'sub', 'div', 'span'
];

const ATTRS = ['href', 'title', 'class', 'align', 'colspan', 'rowspan', 'src', 'alt'];

/** A link may go somewhere a person chose. `javascript:` and friends may go nowhere. */
const URI = /^(?:https?:|mailto:|tel:|#|\/|\.\/|\.\.\/)/i;

/**
 * The three allowlisted attributes whose values are not addresses.
 *
 * `ALLOWED_URI_REGEXP` is not only a link filter: DOMPurify runs it over the value of
 * every allowed attribute that is not in its own URI-safe list, and an attribute that
 * fails it is dropped without a word. `class`, `title` and `alt` are on that list, so a
 * fence keeps its `language-go`; `align` is not, so every `|---:|` in the corpus arrived
 * left-aligned and the alignment a model asked for was gone before CSS could see it.
 * `colspan` and `rowspan` are named for the same reason: they are allowlisted, they carry
 * numbers rather than addresses, and GFM happens not to produce them today (marked leaves
 * `||` as two cells and escapes the raw HTML that would) — which is a fact about marked
 * 18, not a reason to leave the trap armed.
 */
const NOT_ADDRESSES = ['align', 'colspan', 'rowspan'];

/**
 * Parse a string into nodes without ever putting it in the live document.
 *
 * DOMParser rather than `template.innerHTML`: nothing in this string gets a chance to
 * execute or to fetch, and the second call site — highlight.js handing back markup — is a
 * string this file did not sanitize.
 */
function nodes(html: string): Node[] {
  const doc = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html');
  // Here, and not later: an `<img>` starts fetching the moment it belongs to a rendered
  // document, whether or not anything appends it. Adopting one to convert it afterwards
  // is a request the user never made — measured, in `make ui`, by the harness that counts
  // what the page fetched. So nothing that can load ever leaves this parser document.
  linkImages(doc.body);
  return Array.from(doc.body.childNodes).map((n) => document.importNode(n, true));
}

/**
 * TeX inside `$…$`, `\(…\)` or `\[…\]`, inline.
 *
 * Hand-scanned rather than one regex, because the cases that matter are the ones a regex
 * gets wrong in prose: `$5 and $10` is money, not math, and `$ x$` is a typo. The rule
 * kept is KaTeX's own — a delimiter may not touch whitespace, and a closing `$` may not
 * be followed by a digit.
 */
function scanInlineMath(src: string): string | undefined {
  if (src.startsWith('$$')) return undefined; // a block owns this
  if (src.startsWith('\\(') || src.startsWith('\\[')) {
    const close = src.startsWith('\\(') ? '\\)' : '\\]';
    const end = src.indexOf(close, 2);
    return end < 0 ? undefined : src.slice(0, end + close.length);
  }
  if (src[0] !== '$') return undefined;
  for (let i = 1; i < src.length; i++) {
    if (src[i] === '\\') {
      i++;
      continue;
    }
    if (src[i] !== '$') continue;
    const body = src.slice(1, i);
    if (!body || /\s/.test(body[0]) || /\s/.test(body[body.length - 1])) return undefined;
    if (/\d/.test(src[i + 1] ?? '')) return undefined;
    return src.slice(0, i + 1);
  }
  return undefined;
}

/** The one place TeX is written into markup, so the one place it is encoded. */
function slot(tex: string, display: boolean): string {
  const data = `data-math="${encodeURIComponent(tex)}"`;
  return display ? `<div class="math" ${data}></div>` : `<span class="math" ${data}></span>`;
}

/** `\begin{aligned}…\end{aligned}`, `$$…$$`, `\[...\]` on their own. */
function scanMathBlock(src: string): string | undefined {
  if (!src.startsWith('$$')) return undefined;
  const end = src.indexOf('$$', 2);
  return end < 0 ? undefined : src.slice(0, end + 2);
}

function body(hit: string): string {
  if (hit.startsWith('$$')) return hit.slice(2, -2).replace(/^\n+|\n+$/g, '');
  if (hit.startsWith('\\[') || hit.startsWith('\\(')) return hit.slice(2, -2);
  return hit.slice(1, -1);
}

const isDisplay = (hit: string): boolean => hit.startsWith('$$') || hit.startsWith('\\[');

type MathToken = { type: string; raw: string; text: string };

const mathBlock: TokenizerAndRendererExtension = {
  name: 'mathBlock',
  level: 'block',
  start: (src) => src.indexOf('$$'),
  tokenizer(src) {
    const hit = scanMathBlock(src);
    if (!hit) return undefined;
    const token: MathToken = { type: 'mathBlock', raw: hit, text: body(hit) };
    return token;
  },
  renderer: (token) => slot((token as MathToken).text, true)
};

const mathInline: TokenizerAndRendererExtension = {
  name: 'mathInline',
  level: 'inline',
  start: (src) => {
    const dollar = src.indexOf('$');
    const backslash = src.indexOf('\\');
    if (dollar < 0) return backslash;
    if (backslash < 0) return dollar;
    return Math.min(dollar, backslash);
  },
  tokenizer(src) {
    const hit = scanInlineMath(src);
    if (!hit) return undefined;
    const token: MathToken = { type: 'mathInline', raw: hit, text: body(hit) };
    return token;
  },
  renderer: (token) => {
    const t = token as MathToken;
    return slot(t.text, isDisplay(t.raw));
  }
};

const md = new Marked({ gfm: true, breaks: true }).use({
  extensions: [mathBlock, mathInline]
});

/**
 * Markdown to a fragment ready to mount: sanitized, math drawn, code coloured, images
 * refused.
 *
 * A fragment rather than a string so there is no `{@html}` step and no frame in which the
 * math is an empty box waiting for an effect to notice it.
 */
export function renderRich(source: string): DocumentFragment {
  const html = DOMPurify.sanitize(md.parse(source, { async: false }) as string, {
    ALLOWED_TAGS: TAGS,
    ALLOWED_ATTR: ATTRS,
    ALLOWED_URI_REGEXP: URI,
    ADD_URI_SAFE_ATTR: NOT_ADDRESSES,
    KEEP_CONTENT: true
  });

  const fragment = document.createDocumentFragment();
  fragment.append(...nodes(html));
  hydrate(fragment);
  return fragment;
}

/**
 * Everything that cannot be done to a string. Each of these is a pass over the sanitized
 * fragment, in the order they have to happen: tables get their scroller before math exists
 * (so a KaTeX `array` is not mistaken for one), links are set last (so a link that
 * replaced an image is a link too).
 */
function hydrate(root: DocumentFragment): void {
  wrapTables(root);
  drawMath(root);
  colourCode(root);
  openLinks(root); // last, so the links that replaced images are links too
}

function wrapTables(root: ParentNode): void {
  for (const table of Array.from(root.querySelectorAll<HTMLTableElement>('table'))) {
    // A wide table scrolls inside the reading column instead of widening it, and the only
    // element that can hold that scroll is one we add: `display: block` on the table
    // itself would collapse every width rule it has.
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    table.replaceWith(wrap);
    wrap.append(table);
  }
}

function drawMath(root: ParentNode): void {
  for (const el of Array.from(root.querySelectorAll<HTMLElement>('[data-math]'))) {
    const tex = decodeURIComponent(el.dataset.math ?? '');
    delete el.dataset.math;
    try {
      katex.render(tex, el, {
        displayMode: el.tagName === 'DIV',
        // A streamed answer holds `$$\frac{1}{2` for a moment. Blanking the answer over a
        // half-written expression is worse than drawing the failing source in red.
        throwOnError: false,
        errorColor: '#d1242f',
        strict: 'ignore',
        trust: false,
        maxExpand: 100,
        maxSize: 50
      });
    } catch {
      // KaTeX threw despite throwOnError: the TeX survives as text, so the sentence it
      // was inside is still readable.
      el.textContent = tex;
    }
  }
}

function colourCode(root: ParentNode): void {
  for (const block of Array.from(root.querySelectorAll<HTMLElement>('pre code'))) {
    const info = Array.from(block.classList)
      .map((c) => (c.startsWith('language-') ? c.slice(9).toLowerCase() : ''))
      .filter(Boolean)[0];
    const language = info ? (ALIASES[info] ?? info) : '';
    if (!language || !hljs.getLanguage(language)) continue;
    const code = block.textContent ?? '';
    // hljs escapes what it is given and adds only `<span class="hljs-*">`, and the text
    // came out of an already-sanitized node — but it is still markup this file did not
    // sanitize, so it is parsed the same way as the model's: through DOMParser.
    block.replaceChildren(...nodes(hljs.highlight(code, { language, ignoreIllegals: true }).value));
    block.dataset.lang = language;
  }

}

function linkImages(root: ParentNode): void {
  for (const img of Array.from(root.querySelectorAll<HTMLImageElement>('img'))) {
    // An image is a request the user did not make. Keep the sentence around it and the
    // address it would have gone to, and load nothing. The anchor is made in the document
    // this was parsed into, because that is the one where nothing can load.
    const link = img.ownerDocument.createElement('a');
    link.className = 'image';
    const src = img.getAttribute('src') ?? '';
    link.href = src;
    link.textContent = img.getAttribute('alt') || src;
    img.replaceWith(link);
  }
}

function openLinks(root: ParentNode): void {
  for (const a of Array.from(root.querySelectorAll<HTMLAnchorElement>('a[href]'))) {
    // A link out of a local app leaves localhost, so it asks to be seen leaving: a new
    // tab, and no `opener` handed to whoever is on the other end.
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
  }
}
