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

/** Has the device step been answered? A stored slot name means yes; a blank one means either
 *  that libris picks or that nobody has been asked yet, and only the browser knows which. */
export function deviceAsked(engineDevice: string, ack = hasAck()): boolean {
  return engineDevice.trim() !== '' || ack;
}

function hasAck(): boolean {
  try {
    return localStorage.getItem(DEVICE_ACK) === '1';
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
  // With nothing reported there is no choice to make and no reason to stop anyone (ADR 0019).
  const reported = (devices?.devices.length ?? 0) > 0;
  const asked = deviceAsked(engineDevice);
  const deviceOk = engineOk && (!reported || asked);
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
