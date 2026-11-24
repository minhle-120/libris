// Whether Libris can answer a question yet, in one place.
//
// Three facts decide it and every one of them can change while the page is open: the engine
// may still be downloading, a model may arrive or be discarded from another window, the router
// may die. So this is derived from what the server says on each poll rather than remembered,
// and the setup screen and the app's own gate read the same rule rather than each
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
  /** Whether the device step has been answered at all, as opposed to merely answerable. */
  deviceAsked: boolean;
  models: boolean;
  serving: boolean;
  chat?: ModelEntry;
  embed?: ModelEntry;
};

const complete = (e?: ModelEntry): boolean => !!e && e.complete;

/** Where a device choice is remembered. Blank means libris picks, so the absence of a name is
 * not evidence that anybody was asked — the confirmation of libris's own pick is recorded here
 * instead, beside the other per-user view state (§7). */
export const DEVICE_ACK = 'libris.device-ack';

/** What the browser records for "this machine's device was looked at", keyed to the machine and
 *  the engine build rather than being a bare yes. The wizard is a per-installation thing and
 *  localStorage is per-origin, so a plain flag from one test silently answered the question for
 *  every data directory opened in that browser afterwards — which is how the step came to be
 *  skipped "again" on a machine that had never been asked. An upgraded engine has a different
 *  device list, and that is worth looking at once more too. */
export function ackKey(engine: EngineView | null, devices: DeviceReport | null): string {
  return `${engine?.installed?.tag ?? 'no-engine'}:${devices?.fingerprint ?? devices?.os ?? 'unknown'}`;
}

/** Has the device step been answered? A stored slot name means yes; a blank one means either
 *  that libris picks or that nobody has been asked yet, and only the browser knows which. */
export function deviceAsked(engineDevice: string, expected: string): boolean {
  if (engineDevice.trim() !== '') return true;
  try {
    return localStorage.getItem(DEVICE_ACK) === expected;
  } catch {
    return false;
  }
}

export function readSetup(
  engine: EngineView | null,
  models: ModelsView | null,
  devices: DeviceReport | null,
  /** `engineDevice` as the settings have it, which is the durable half of the answer. */
  engineDevice = '',
): Setup {
  // External mode has nothing here to install, run, or pick a device for: the three URLs are
  // the whole configuration, and libris cannot tell whether somebody else's servers are up.
  // Saying "nothing to set up" is truer than a wizard whose steps describe absent things.
  if (engine && engine.mode !== 'managed') {
    return {
      ready: true,
      step: 'models',
      engine: true,
      device: true,
      deviceAsked: true,
      models: true,
      serving: true,
    };
  }

  const entries = models?.entries ?? [];
  const chat = entries.find((e) => e.role === 'chat');
  const embed = entries.find((e) => e.role === 'embed');

  const engineOk = !!engine?.installed;
  // The step is finished when it has been answered, not when it became answerable. That was the
  // bug: `--list-devices` answers the instant an engine is on disk, so a rule of "the machine
  // reported a device" was true before anyone saw the row, and the choice was skipped every
  // single time. A blank `engineDevice` is genuinely ambiguous — libris picks, or nobody was
  // asked — so the confirmation lives in the browser rather than pretending to be a setting.
  // An empty report is NOT a reason to pass. The cache is written the first time anything asks,
  // which on a fresh install is before the engine exists — so "the engine reported no
  // accelerator" is the normal first answer, not proof that nobody needs picking. The engine is
  // the only probe, which does not mean the probe has run yet. The step shows, says what
  // is true, and offers the way forward.
  const asked = deviceAsked(engineDevice, ackKey(engine, devices));
  const deviceOk = engineOk && asked;
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
    deviceAsked: asked,
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
