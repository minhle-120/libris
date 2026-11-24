// Whether Libris can answer a question yet, in one place.
//
// Three facts decide it and every one of them can change while the page is open: the engine
// may still be downloading, a model may arrive or be discarded from another window, the router
// may die. So this is derived from what the server says on each poll rather than remembered
// (ADR 0020), and the setup screen and the app's own gate read the same rule rather than each
// keeping a version of it.
import type { DeviceReport, EngineView, ModelEntry, ModelsView } from './types';

export type Step = 'engine' | 'device' | 'models';

export type Setup = {
  /** Nothing is missing: the router libris runs is up, holding the two models a turn needs. */
  ready: boolean;
  /** The step to show. Everything after it is still listed, as waiting. */
  step: Step;
  engine: boolean;
  device: boolean;
  models: boolean;
  serving: boolean;
  chat?: ModelEntry;
  embed?: ModelEntry;
};

const complete = (e?: ModelEntry): boolean => !!e && e.complete;

export function readSetup(
  engine: EngineView | null,
  models: ModelsView | null,
  devices: DeviceReport | null,
): Setup {
  // External mode has nothing here to install, run, or pick a device for: the three URLs are
  // the whole configuration, and libris cannot tell whether somebody else's servers are up.
  // Saying "nothing to set up" is truer than a wizard whose steps describe absent things.
  if (engine && engine.mode !== 'managed') {
    return { ready: true, step: 'models', engine: true, device: true, models: true, serving: true };
  }

  const entries = models?.entries ?? [];
  const chat = entries.find((e) => e.role === 'chat');
  const embed = entries.find((e) => e.role === 'embed');

  const engineOk = !!engine?.installed;
  // A finished device step means the machine answered that it has one. A blank `engineDevice`
  // is not an unfinished choice — a blank means libris picks — so this is about the report,
  // never about whether somebody pressed a button (ADR 0019: the engine's own --list-devices
  // is the only device probe there is).
  const deviceOk = engineOk && (devices?.devices.length ?? 0) > 0;
  // A turn needs the chat model and the embedder both: without one, nothing indexes; without
  // the other, nothing answers.
  const modelsOk = complete(chat) && complete(embed);
  const serving = !!engine?.serve?.running;

  const step: Step = !engineOk ? 'engine' : !deviceOk ? 'device' : 'models';

  return {
    ready: engineOk && deviceOk && modelsOk && serving,
    step,
    engine: engineOk,
    device: deviceOk,
    models: modelsOk,
    serving,
    chat,
    embed,
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
