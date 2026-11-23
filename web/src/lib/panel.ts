/**
 * Side-panel geometry, shared by the notebook list on the left and the source panel
 * on the right. Two panels that drag differently teach the user twice, so the bounds,
 * the storage and the drag live in one place.
 */
export type PanelSpec = {
  /** localStorage namespace: `<key>.width` and `<key>.open`. */
  key: string;
  min: number;
  max: number;
  defaultW: number;
  /** Width when collapsed to a rail of icons. */
  rail: number;
  /** Which edge the handle sits on. The right panel grows leftwards. */
  edge: 'left' | 'right';
};

/** The two panels of the shell. Both live here so the bounds and the storage keys
 * have one home: a panel defined twice is a panel that drifts. */
export const NOTEBOOKS: PanelSpec = {
  key: 'libris.sidebar',
  min: 200,
  max: 480,
  defaultW: 288,
  rail: 44,
  edge: 'left'
};

export const SIDE: PanelSpec = {
  key: 'libris.sidepanel',
  min: 260,
  max: 560,
  defaultW: 340,
  rail: 44,
  edge: 'right'
};

/**
 * How much of the window the reading column is owed. Below this the chat is a stack of
 * short lines and tables cannot fit; the reading measure itself wants 42rem (672px), and
 * this is the floor under it rather than the ideal.
 */
export const MIN_CHAT = 480;

/**
 * The narrow rule: two fixed columns and a reading column do not fit in every window, and
 * the middle one dies first. Below MIN_CHAT of chat, the right panel is in the way.
 *
 * Two numbers, not one, because a single threshold oscillates: the panel's own width is
 * what makes the chat narrow, so closing it makes room and reopening it takes the room
 * back. `tooNarrow` is measured with the panel as it stands now — including a panel the
 * user dragged wide — and `hasRoom` is measured against the narrowest panel worth opening
 * (min, not the remembered width), so the way in and the way out are different widths.
 *
 * Deliberately stateless. The caller decides when to act, and acts only when the answer
 * *changes*: a rule that runs on every resize event takes the panel away again a second
 * after the user put it back.
 */
export function narrowRule(
  innerWidth: number,
  sidebar: { width: number; open: boolean },
  side: { width: number; open: boolean }
): { tooNarrow: boolean; hasRoom: boolean } {
  const left = sidebar.open ? sidebar.width : NOTEBOOKS.rail;
  const chat = innerWidth - left - (side.open ? side.width : SIDE.rail);
  return {
    tooNarrow: chat < MIN_CHAT,
    hasRoom: innerWidth - left - MIN_CHAT >= SIDE.min,
  };
}

export const clampW = (spec: PanelSpec, n: number) =>
  Math.min(spec.max, Math.max(spec.min, Math.round(n)));

export function readW(spec: PanelSpec): number {
  const n = Number(localStorage.getItem(`${spec.key}.width`));
  return n ? clampW(spec, n) : spec.defaultW;
}

export function writeW(spec: PanelSpec, w: number) {
  localStorage.setItem(`${spec.key}.width`, String(w));
}

export function readOpen(spec: PanelSpec): boolean {
  return localStorage.getItem(`${spec.key}.open`) !== '0';
}

export function writeOpen(spec: PanelSpec, open: boolean) {
  localStorage.setItem(`${spec.key}.open`, open ? '1' : '0');
}

export type HandleOptions = {
  spec: PanelSpec;
  get: () => number;
  set: (n: number) => void;
};

/**
 * A drag handle on a panel edge: pointer drag, double-click to reset, arrows to
 * nudge, and the width stored only when the interaction ends so a drag does not
 * write to localStorage on every frame.
 *
 * Arrows mean *wider* and *narrower* on both sides, even though the edges face each
 * other. An arrow that meant "move this border right" would mean opposite things on
 * opposite panels, and nobody would predict which was which.
 */
export function dragHandle(node: HTMLElement, opts: HandleOptions) {
  const { spec } = opts;
  const sign = spec.edge === 'right' ? -1 : 1;

  const commit = (n: number) => {
    opts.set(n);
    writeW(spec, n);
  };

  const onDown = (e: PointerEvent) => {
    const fromX = e.clientX;
    const fromW = opts.get();
    const move = (ev: PointerEvent) => opts.set(clampW(spec, fromW + sign * (ev.clientX - fromX)));
    const stop = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', stop);
      document.documentElement.classList.remove('resizing');
      writeW(spec, opts.get());
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', stop);
    document.documentElement.classList.add('resizing');
    e.preventDefault();
  };

  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'ArrowLeft') commit(clampW(spec, opts.get() - 16));
    else if (e.key === 'ArrowRight') commit(clampW(spec, opts.get() + 16));
    else if (e.key === 'Home') commit(spec.defaultW);
    else return;
    e.preventDefault();
  };

  const onDouble = () => commit(spec.defaultW);

  node.addEventListener('pointerdown', onDown);
  node.addEventListener('keydown', onKey);
  node.addEventListener('dblclick', onDouble);

  return {
    destroy() {
      node.removeEventListener('pointerdown', onDown);
      node.removeEventListener('keydown', onKey);
      node.removeEventListener('dblclick', onDouble);
    }
  };
}
