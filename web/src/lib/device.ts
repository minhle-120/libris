import * as api from "./api";
import type { EngineView } from "./types";

/**
 * What confirming the device row would record right now, taken from the endpoint that
 * computes it.
 *
 * The key is the engine's, not the page's: it names this engine build on this machine, and
 * it exists only once the engine has been asked what it runs on. An engine view can be a
 * poll too early — installed, slots on screen to click, and no key in it yet — and a choice
 * saved with a blank key records nothing at all, because the server reads a blank key as
 * "nobody has been asked yet" rather than as an answer. That is a click which returns 200
 * and leaves the wizard on the same step forever.
 *
 * So the click asks for the key instead of trusting what the screen is holding, and says so
 * when the engine cannot produce one yet. The refreshed view comes back with it so the
 * caller stops rendering from the stale one.
 */
export async function ackForConfirm(
  current?: EngineView,
): Promise<{ ack: string; engine: EngineView }> {
  if (current?.device_ack_now) {
    return { ack: current.device_ack_now, engine: current };
  }

  const again = await api.getEngine();
  const got = again.device_ack_now ?? "";
  if (!got) {
    throw new Error(
      "The engine has not reported what it can run on yet. Try again in a moment.",
    );
  }
  return { ack: got, engine: again };
}
