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
  /** Whether the panel is open, and the way to change it. The edge is the panel's only control,
   *  so the handle that resizes it is also the one that opens and folds it. */
  open: () => boolean;
  setOpen: (open: boolean) => void;
  /** True while a drag has gone far enough past the floor that letting go would fold the panel.
   *  The view fades the column on the strength of it, so the panel says it is about to go. */
  pending?: (armed: boolean) => void;
};

/**
 * Both are handle travel, not width: the column never renders narrower than `min`.
 *
 * `CLOSE_SLOP` is how much further the pointer may travel once the floor is reached, before the
 * panel folds away. A shrink that simply stopped at `min` left folding a separate gesture from
 * resizing, executed by a button nobody found on the border; over-drag makes it the same gesture
 * taken further, so one edge does the whole job.
 *
 * `OPEN_SLOP` is the dead zone at the start of a pull out of the rail. From there on the edge is
 * under the cursor one for one, growing out of the rail's own width; the dead zone is only there
 * so a two-pixel wobble on the seam does not summon a panel nobody meant to open.
 */
export const CLOSE_SLOP = 56;
export const OPEN_SLOP = 20;

/** The width a drag may put on screen *right now* — down to the rail, because a panel being
 *  pulled out of it passes through widths nobody would leave it at. `clampW` is the clamp for a
 *  panel at rest, and that one runs on release instead.
 *
 *  Which is also why the setter the handle is given must not clamp: a pointer 120px out of the
 *  rail is not asking for `min`, it is asking for 120px. */
export const dragW = (spec: PanelSpec, n: number) =>
  Math.min(spec.max, Math.max(spec.rail, Math.round(n)));

/**
 * What one pointer move means, given where the drag started. Pulled out so the floor, the rail
 * and the two slops make one rule that can be read — and tested — apart from the listeners.
 *
 * `baseW` is the width the drag started from and is never rebased mid-drag, which is what keeps
 * the edge under the cursor. A drag out of the rail is measured from the rail, and appears at the
 * rail; a drag that started on a resting panel is measured from that width.
 */
export type DragStep =
  | { kind: 'rail' }
  | { kind: 'midair'; n: number }
  | { kind: 'floor'; armed: boolean }
  | { kind: 'width'; n: number };

export function dragStep(
  spec: PanelSpec,
  s: { fromRail: boolean; baseW: number; pull: number }
): DragStep {
  if (s.fromRail) {
    if (s.pull < OPEN_SLOP) return { kind: 'rail' };
    const n = dragW(spec, spec.rail + (s.pull - OPEN_SLOP));
    return n < spec.min ? { kind: 'midair', n } : { kind: 'width', n };
  }
  const want = s.baseW + s.pull;
  if (want >= spec.min) return { kind: 'width', n: dragW(spec, want) };
  // Held at the floor while the handle keeps travelling, so an over-drag reads as *about to go*
  // rather than as a panel collapsing into slivers.
  return { kind: 'floor', armed: spec.min - want >= CLOSE_SLOP };
}

/**
 * A drag handle on a panel edge: pointer drag that also folds the panel when over-dragged and
 * brings it back when pulled out of the rail, double-click to reset (or to open a folded rail),
 * arrows to nudge, Enter to fold and open, and the width stored only when the interaction ends
 * so a drag does not write to localStorage on every frame.
 *
 * Arrows mean *wider* and *narrower* on both sides, even though the edges face each
 * other. An arrow that meant "move this border right" would mean opposite things on
 * opposite panels, and nobody would predict which was which.
 */
export function dragHandle(node: HTMLElement, opts: HandleOptions) {
  const { spec } = opts;
  const sign = spec.edge === 'right' ? -1 : 1;

  // The resting setter: keyboard, double-click and the end of a drag all land on a width worth
  // leaving a panel at, so they go through the floor.
  const commit = (n: number) => {
    const rest = clampW(spec, n);
    opts.set(rest);
    writeW(spec, rest);
  };

  const onDown = (e: PointerEvent) => {
    // Measured from where the pointer went down and never rebased: that is what makes the edge
    // follow the cursor instead of jumping to meet it.
    const baseX = e.clientX;
    const fromRail = !opts.open();
    // Out of the rail `get` is the width the panel had before it folded, which outlives the fold.
    const baseW = opts.get();
    let armed = false;

    const arm = (next: boolean) => {
      if (next === armed) return;
      armed = next;
      opts.pending?.(armed);
    };

    /** Faded means one thing on both sides of the floor: let go here and you get the rail. */
    const show = (n: number, faded: boolean) => {
      if (!opts.open()) opts.setOpen(true);
      opts.set(n);
      arm(faded);
    };

    const move = (ev: PointerEvent) => {
      const step = dragStep(spec, { fromRail, baseW, pull: sign * (ev.clientX - baseX) });
      if (step.kind === 'rail') {
        // Only a drag that already opened the panel has anything to push back; a drag that never
        // crossed the dead zone must not touch the width the folded rail is remembering.
        if (opts.open()) show(spec.rail, true);
      } else if (step.kind === 'floor') {
        show(spec.min, step.armed);
      } else {
        show(step.n, step.kind === 'midair');
      }
    };

    const stop = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', stop);
      document.documentElement.classList.remove('resizing');
      // A panel rests at the floor or above it, or folded. Under the floor is mid-air — whether
      // arriving or leaving — so letting go there ends at the rail. `armed` is the same promise
      // made explicit on the way in, where the column cannot shrink to show it.
      // Read it before `arm(false)`: that call is what un-fades the column, and it must not also
      // talk the fold out of the release.
      const live = opts.get();
      const fold = armed || live < spec.min;
      arm(false);
      // Folding is hiding, not resizing. Whatever the panel was before the drag is what it
      // returns at, so that — and not the width it was dragged through — is what gets stored.
      if (fold) {
        opts.set(baseW);
        writeW(spec, baseW);
        opts.setOpen(false);
      } else {
        commit(live);
      }
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', stop);
    document.documentElement.classList.add('resizing');
    e.preventDefault();
  };

  const onKey = (e: KeyboardEvent) => seamKeys(spec, opts, commit, e);

  const onDouble = () => (opts.open() ? commit(spec.defaultW) : opts.setOpen(true));

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

/** The keyboard half of the seam, and the only keyboard path there is: `Enter` folds and opens,
 *  arrows are widths, `Home` resets. Out of the rail an arrow means "come back" — it cannot mean
 *  "nudge a panel nobody can see", and a control whose keys mean something else depending on a
 *  state the screen is not showing is a control that gets used wrong.
 *
 *  The same rule the pointer obeys: arrows commit resting widths, so they never leave the panel
 *  under the floor.
 */
function seamKeys(
  spec: PanelSpec,
  opts: HandleOptions,
  commit: (n: number) => void,
  e: KeyboardEvent
) {
  if (e.key === 'Enter' || e.key === ' ') {
    opts.setOpen(!opts.open());
    e.preventDefault();
    return;
  }
  if (!opts.open()) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    opts.setOpen(true);
    e.preventDefault();
    return;
  }
  if (e.key === 'ArrowLeft') commit(opts.get() - 16);
  else if (e.key === 'ArrowRight') commit(opts.get() + 16);
  else if (e.key === 'Home') commit(spec.defaultW);
  else return;
  e.preventDefault();
}
