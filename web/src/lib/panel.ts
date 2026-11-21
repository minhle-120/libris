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
