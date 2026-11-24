// Whether libris can answer a question yet is the server's rule, not this file's.
//
// It used to be computed here — from three polls, in the browser, in a language the
// program that obeys it cannot see — while Go ran a shorter version at start-up. The
// two disagreed, so libris started a router the wizard still called unfinished, and
// the wizard's comment grew eleven lines explaining its own rule. The rule lives in
// internal/setup now; this file carries the answer to the two places that render it
// and adds the one thing only a page wants: how many bytes are still to come.
import type { EngineView, ModelEntry, ModelsView } from './types';

export type Step = 'engine' | 'device' | 'models';

export type Setup = {
  /** Nothing is missing: the router libris runs is up, holding both models a turn needs. */
  ready: boolean;
  /** The step to show. Everything after it is still listed, as waiting. */
  step: Step;
  engine: boolean;
  device: boolean;
  models: boolean;
  serving: boolean;
  // The two catalogue rows, for the download button and its bar. The server's view says
  // whether the roles are filled; only the rows say how far the download got.
  chat?: ModelEntry;
  embed?: ModelEntry;
};

/**
 * The server's answer, with the two model rows beside it. `engine` is null when the
 * server could not be reached at all — which is not "not ready", it is "unknown", and
 * the caller stands aside rather than painting a wizard full of blanks.
 */
export function readSetup(engine: EngineView | null, models: ModelsView | null): Setup {
  const v = engine?.setup;
  const entries = models?.entries ?? [];
  return {
    ready: v?.ready ?? false,
    // A server too old to answer is not a machine that has finished; 'models' is where a
    // finished-but-stopped machine would be, which is the least wrong place to stand.
    step: (v?.step ?? 'models') as Step,
    engine: v?.engine ?? false,
    device: v?.device ?? false,
    models: v?.models ?? false,
    serving: v?.serving ?? false,
    chat: entries.find((e) => e.role === 'chat'),
    embed: entries.find((e) => e.role === 'embed'),
  };
}

/** Bytes left of the catalogue, for the line that says how long this is going to take. */
export function remaining(setup: Setup): number {
  let bytes = 0;
  for (const e of [setup.chat, setup.embed]) {
    if (e && !e.complete) bytes += Math.max(0, e.bytes - e.partialBytes);
  }
  return bytes;
}
