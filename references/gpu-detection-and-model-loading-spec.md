# GPU Detection & Model Loading Specification

Narrow scope. Two questions only:

1. **How does the app discover which compute devices the machine has?**
2. **How is the chosen device used when the model is loaded?**

Everything else about backend provisioning (engine download, model bundles, HTTP contracts,
settings persistence, the research loop) is out of scope. Only the glue needed to make the two
answers coherent is included.

Source of truth: `lib/core/services/backend_downloader.dart` (`listAvailableDevices`,
`startServer`, `_findServerBinary`, `generateDefaultConfig`),
`lib/src/features/chat/presentation/controllers/settings_controller.dart` (`fetchDevices`,
`setSelectedDevice`, `startServer`, `_loadSettings`),
`lib/src/features/setup/presentation/views/internal_config_screen.dart`,
`lib/src/features/chat/presentation/views/local_engine_settings_screen.dart`,
`android/app/src/main/kotlin/com/sift/sift/{ModelManager,EmbeddingManager}.kt`,
`android/app/src/main/AndroidManifest.xml`.

---

## 1. The shape of the design

Two decoupled stages, with **no introspection between them**:

```
STAGE 1 — DETECT (one-shot, user- or boot-triggered)
    spawn  <engine binary> --list-devices        # exits immediately, loads nothing
    parse stdout            → [{id: "Vulkan0", name: "<driver adapter string>"}, …]
    prepend synthetic       {id: "cpu", name: "Auto (not recommended)"}
    user picks one          → persisted as an opaque id string

STAGE 2 — BIND (each server start)
    spawn  <engine binary> --port 8080 --models-max 1 --models-preset <ini>
                           --sleep-idle-seconds 300 [--device <chosen id>]
    the engine, not the app, decides how weights land on that device
    policy lives entirely in the preset INI (§4)
```

Consequences worth preserving (and worth knowing before you copy it):

- **Detection requires the engine binary to already exist.** The app doesn't query the OS or a
  driver API; it asks the engine to enumerate its own backends. Hence the setup wizard order:
  engine install → hardware pick → model download.
- **The app never learns anything quantitative about the device.** No VRAM total/free, no
  compute capability, no device memory probing, no "will this fit" check. `--list-devices`
  yields ids and human-readable names; that's the full dataset.
- **Model placement is delegated to llama.cpp.** The app chooses a device and writes a static
  INI. All real decisions (layer offload, KV sizing, partial loading) happen inside the server.
- **The device id is opaque and passed verbatim.** The app never re-validates it at launch.

---

## 2. Stage 1 — device enumeration

```
listAvailableDevices(engineName) -> {devices: DeviceInfo[], rawOutput: string}:

  devices = [ DeviceInfo(id: "cpu", name: "Auto (not recommended)", isGpu: false) ]   // always

  serverPath = findServerBinary(<enginesDir>/<basenameWithoutExtension(engineName)>)
  if serverPath == null:
      return {devices, rawOutput: "Engine binary not found in <path>"}      // never throws

  result = run(serverPath, ["--list-devices"],
               env = isLinux ? { LD_LIBRARY_PATH: "<dir(serverPath)>:<dir(dir(serverPath))>" }
                             : null)
  rawOutput = result.stdout + result.stderr            // both merged; ordering not guaranteed

  inAvailableDevices = false
  for line in rawOutput.split("\n"):
      if line contains "Available devices:": inAvailableDevices = true; continue
      if inAvailableDevices and line.trim() != "":
          m = /^\s*(Vulkan\d+):\s*([^(\n]+)/.match(line)
          if m: devices.addIfAbsent(id = m[1], name = m[2].trim(), isGpu = true)   // dedupe by id

  on exception: rawOutput = "Hardware audit failed: $e"    // devices stays [cpu]
  return {devices, rawOutput}
```

`findServerBinary(dir)` resolves the binary at `<dir>/llama-server[.exe]` or, failing that,
`<dir>/<any subdir>/llama-server[.exe]` — one level deep only, because upstream archives
occasionally wrap contents in a folder.

### 2.1 What "GPU" means here

The parser only accepts ids matching `Vulkan\d+`. So the app's entire concept of "what GPUs
this machine has" is **the Vulkan devices that llama.cpp reports**:

- A dedicated NVIDIA/AMD card and an integrated GPU both surface, as `Vulkan0`, `Vulkan1`, …
  with the adapter name taken from the driver's device string.
- CUDA, Metal, and ROCm-native devices are **not** separately enumerated — which is consistent
  with the app only ever installing Vulkan engine builds. A machine whose only accelerator is
  exposed through a non-Vulkan path audits to `cpu` alone.
- Under a Vulkan-on-CPU software implementation (lavapipe/swiftshader), that device appears as
  just another `VulkanN`. Nothing marks it as software, so a user can be offered a very slow
  "GPU".
- The name capture stops at the first `(`, deliberately trimming driver suffixes.

If you want real capability data, `--list-devices` output already prints per-device memory and
limits lines — parse those instead of only the id/name prefix, and keep `rawOutput` as the
fallback for display (§6).

### 2.2 `LD_LIBRARY_PATH` during the audit

The same library-path override used at launch is needed for the audit, and for the same
reason: `libggml*.so` and the Vulkan backend live either beside the binary or one level up,
depending on whether the archive wrapper survived extraction. Without it the binary dies in the
dynamic loader and the audit returns the bare `cpu` entry — indistinguishable from "this
machine has no GPU". That makes the failure mode of a broken install look identical to a
hardware limitation; log `rawOutput` so it's diagnosable.

Windows relies on DLLs beside the exe; macOS on rpaths. No override is set on either.

### 2.3 When the audit runs

| Trigger | Where |
|---|---|
| Boot, if an engine is verified installed and a `selectedEngine` is recorded | `_loadSettings` |
| Immediately after a successful engine download/extract | controller's `downloadEngine` |
| User presses **Detect Hardware** (wizard, shown only when the list is empty) | `internal_config_screen` |
| User presses **Refresh Hardware List** (settings, icon button next to the dropdown) | `local_engine_settings_screen` |

There is no cache beyond the in-memory `availableDevices` list, no automatic re-audit on
hardware change, and no background monitoring. Each audit is a fresh process spawn; it is
asynchronous and never blocks the UI thread, and never throws.

---

## 3. Selection, persistence, recovery

```
state.selectedDeviceId : string?            // opaque, "cpu" or e.g. "Vulkan0"

load:  selectedDeviceId = prefs.getString("selectedDeviceId") ?? "cpu"

setSelectedDevice(id):  prefs.setString("selectedDeviceId", id);  state.selectedDeviceId = id

fetchDevices():
  devices = listAvailableDevices(state.selectedEngine).devices
  state.availableDevices = devices
  if not devices.any((d) => d.id == state.selectedDeviceId): setSelectedDevice("cpu")
```

Recovery rule: **a persisted id that the current audit doesn't report is silently demoted to
`cpu`.** This is the path that handles a removed eGPU, a dock unplug, a driver update changing
enumeration order, or switching to an engine build without Vulkan support.

Audit raw output is appended to the app's server log (`--- Hardware Device Audit ---` + raw)
for support.

### 3.1 Known dead weight

`gpuDeviceIndex` (int, default 0) is declared in state, has a setter, is read from prefs at
load, and is **never read anywhere else**. It's a leftover from an index-based device picker,
superseded by the id string. Don't port it.

### 3.2 Wizard gating quirk

Step 2 ("Hardware") is considered complete when `selectedDeviceId != null`. But load defaults
the value to `"cpu"`, so the step reports complete before the user has ever chosen anything —
the wizard advances on the user's behalf to the model step. Either default to `null` and let
the first audit populate the choice, or gate on "user explicitly chose" rather than
"`!= null`".

### 3.3 UI affordances to reproduce

- **Wizard:** vertical `RadioListTile` list over `availableDevices`, labelled by `name` only
  (the id is hidden); a **Detect Hardware** button replaces the list while it's empty.
- **Settings:** a `DropdownButtonFormField` over the same list, plus a refresh icon button. The
  dropdown is given a `Key("${selectedDeviceId}_${availableDevices.length}")` specifically to
  force a widget rebuild when the list is replaced — the value it displays is otherwise stale.
  Its `initialValue` falls back to `"cpu"` when the persisted id isn't in the list (a second,
  UI-side instance of the recovery rule).
- **Status header:** resolves the selected id back to its display name for a summary card,
  `'None'` when nothing is selected.
- A spinner is shown when an engine is selected but the list is still empty (audit in flight).

### 3.4 Changing the device does not restart anything

`setSelectedDevice` only persists and updates state. There is **no server restart, and no
"restart required" notice** — a running server keeps serving on the device it was launched
with until the user stops and starts it. This is the single most surprising behaviour of the
current design. Fix: mark the setting dirty and offer/auto-restart when the server is running.

---

## 4. Stage 2 — binding the device and loading models onto it

### 4.1 Launch arguments

```
args = ["--port", "8080",
        "--models-max", "1",
        "--models-preset", <configPath>,
        "--sleep-idle-seconds", "300"]

if selectedDeviceId != "cpu":
    args += ["--device", selectedDeviceId]        // id passed verbatim, unvalidated
```

- **`cpu` means "omit the flag"** and let llama.cpp auto-select — which is exactly what the
  label "Auto (not recommended)" advertises. There is no explicit CPU device id.
- **The value is not checked against `availableDevices` at launch.** A stale id (persisted
  before a driver change, hand-edited settings, copied install) reaches the server, which exits
  at startup. The only signal is a line in the log: `--- Server exited with code N ---`.
- POSIX launch wraps the exec so the server becomes its own process-group leader:
  `setsid /bin/sh -c "exec '<abs serverPath>' '<arg>' …"`, with
  `LD_LIBRARY_PATH = <dir(server)>:<dir(dir(server)>)` on Linux. The library path is again what
  makes the Vulkan backend loadable at all.
- `--models-max 1` + `--models-preset` mean one server serves three models with **at most one
  resident at a time, on the single selected device** — the models contend for that device's
  memory rather than being spread across devices. See §4.3.

### 4.2 The preset INI — where loading policy actually lives

Structure and values verbatim; `${…}` resolved at generation time. Model identities below are
**placeholders** — substitute your own trio (one instruct model, one embedding model, one
reranker). What matters is the section/`model` id pairing in §4.2.1 and the placement knobs in
§4.3.

```ini
[*]
flash-attn = auto
no-warmup = true
parallel = 1
mlock = false
no-mmap = false
cache-ram = 1024
fit = on

[${modelTitle}]
model = ${instructPath}
fit-ctx = 16384
chat-template-kwargs = {"enable_thinking": false}
cache-type-k = q8_0
cache-type-v = q8_0
temp = 0.7
top-p = 0.8
top-k = 20
min-p = 0.0
presence-penalty = 1.5
repeat-penalty = 1.0

[EmbeddingModel]
model = ${embeddingPath}
fit = off
embedding = true
ctx-size = 4096
ubatch-size = 4096
batch-size = 4096
n-gpu-layers = 99

[RerankerModel]
model = ${rerankerPath}
fit = off
reranking = true
ctx-size = 4096
ubatch-size = 4096
batch-size = 4096
n-gpu-layers = 99
```

#### 4.2.1 Section names are the routing keys

`modelTitle` is not a free-form label: it is **derived from the instruct model's filename** and
then reused as the `model` id the app sends in every request. The reference's rule is a
substring test on the path — a "small variant" marker in the filename selects the small-variant
section title, otherwise the large-variant title — and the same two titles are hardcoded in the
settings-sync path that writes `chatModel`/`embeddingModel`/`rerankModel`. So three places must
agree:

```
INI section name  ==  value written into chatModel/embeddingModel/rerankModel  ==  `model` field sent at runtime
```

Pick section names that are stable and unlikely to collide with a file path, since the reference
also lets `chatModel` hold an **absolute GGUF path** in one code path (after a bundle download)
and the section name in another (after a size change or backend switch). Standardize on the
section name when you port.

`chat-template-kwargs` is the one line above that is **not portable**: the key inside it belongs
to the pinned model's chat template. Its purpose is to suppress reasoning/thinking output —
which matters here because thinking tokens would consume the 16384 context budget and produce
assistant turns that fail a `tool_choice=required` exchange. When you swap models, find that
model template's equivalent switch (or confirm none is needed) rather than copying the key.

Retune `presence-penalty = 1.5` (aggressive) and the sampler block per model too; they were
tuned for one specific instruct model.

### 4.3 How each model reaches the chosen device

Two different mechanisms coexist in one file, and the asymmetry is intentional:

| Model | Placement control | Behaviour |
|---|---|---|
| Chat (`fit = on`, `fit-ctx = 16384`) | **memory-fit sizing** | The loader measures free memory on the `--device` target and sizes weights + KV cache to it, shrinking context from the 16384 ceiling until it fits. Same config therefore behaves correctly on a 4 GB iGPU and a 24 GB dGPU with no per-hardware config. |
| Embedding / Reranker (`fit = off`, `n-gpu-layers = 99`) | **explicit full offload** | `99` = "all layers" (an intentionally absurd count, since the real number is model-dependent), pinned onto the `--device` target with a fixed 4096 context. Safe because the specialist models are deliberately chosen small enough to always fit. |

Device-relevant knobs in the `[*]` defaults:

- `flash-attn = auto` — enabled only where the backend/device supports it.
- `cache-type-k = q8_0` / `cache-type-v = q8_0` — 8-bit KV cache, roughly halving KV memory;
  the main lever that makes a chat model fit on a small-VRAM device.
- `cache-ram = 1024` — caps RAM cache so cached data can't crowd out an offloaded model.
- `mlock = false`, `no-mmap = false` — memory-mapped loading stays **on**, which is what allows
  a model larger than the device's memory to be partially offloaded rather than failing.
- `parallel = 1` — one slot; the server's device memory budget isn't divided across slots.
- `no-warmup = true` — skips a warmup pass, so first-token latency after a device swap or model
  swap is worse by design.
- `--sleep-idle-seconds 300` — an unused model is unloaded from the device after 5 minutes, so
  the next model can occupy that device memory. Because only one model can be resident
  (`--models-max 1`), this timer is what balances "hold onto the chat model" against "free VRAM
  for the embedding pass".

**Per-section device overrides are unused.** llama.cpp's preset format accepts a per-section
device key, so it would be possible to pin the two small specialists to CPU and the chat model to
the GPU. The app does not use it: `--device` applies to all three. Add it if you want to keep
the embedding path resident while a large chat model owns VRAM — with `--models-max 1` it won't
help unless you also raise that limit.

### 4.4 Nothing in the app checks that the model fits the device

The wizard advertises "Standard — at least 4G VRAM" and "Fast — at least 2G VRAM", but those
strings are copy. The chosen bundle size is never compared against device capability (which the
app cannot obtain anyway, per §1). Practical failure modes, none of which the app detects:

- Model too large for the device → `fit` shrinks context until the requested prompt no longer
  fits, or the server exits at load. Visible only in the log.
- Context silently much smaller than 16384 on a small device, degrading the research loop's
  multi-iteration context budget.
- Quant choice is fixed by the bundle toggle; there is no "pick a quant for my GPU" path.

### 4.5 What tells you the device choice failed

The launch path surfaces nothing structured:

- `startServer` checks preconditions (engine verified, models present, config exists/generate)
  but not the device id.
- stdout/stderr are streamed line-by-line into a **1000-line ring buffer** rendered in a log
  pane, stderr prefixed `[stderr]`.
- `process.exitCode.then(...)` is the only crash detector: it logs
  `--- Server exited with code N ---` and sets `isServerRunning = false`. No restart, no backoff,
  no error state, no parsing of the exit reason.
- After a successful spawn, `fetchModels()` is scheduled after a boot grace (5 s auto-start,
  2 s manual) — so "the port never came up" presents as a permanently empty model list.

If you port this, the cheap wins are: validate `selectedDeviceId ∈ availableDevices` at launch,
and detect the "server exited within N seconds of start" case into a real error state that
offers "try CPU instead".

---

## 5. Mobile: no detection, one manual toggle

Android/iOS internal mode runs in-process (no server, no `--device`, no enumeration).

```
state.mobileUseGpu : bool, default false        // persisted
initializeMobileEngine():   native initialize(modelPath, useGpu: mobileUseGpu)
initializeMobileEmbedder(): native initializeEmbedding(modelPath,
                            tokenizerPath, useGpu: false)      // hardcoded CPU
```

- **Native mapping:** `EngineConfig(modelPath, backend = useGpu ? Backend.GPU : Backend.CPU,
  visionBackend = null, audioBackend = null, maxNumTokens = 4096, cacheDir = null)`. The device
  choice is exactly this enum.
- **Applying a change** requires re-initialization, and the app does that itself: flipping
  `mobileUseGpu` calls `initializeMobileEngine()` again when the engine is already initialized,
  because `initialize` closes the previous conversation + engine and rebuilds both.
- **The embedder is forced to CPU regardless of the toggle** (the GPU delegate was unreliable
  for it). So a phone can be running generation on GPU and embedding on CPU simultaneously.
- **No fallback:** if `Backend.GPU` fails to initialize, `initialize` throws, the channel
  returns an error, Dart returns `false`, and the UI shows "Failed to initialize LiteRT
  generation engine". It does **not** retry on CPU. Users must toggle back manually.
- **Prerequisites for the GPU path**, in the manifest: `<uses-native-library
  android:name="libOpenCL.so" android:required="false"/>` and `libvndksupport.so` likewise
  (`required="false"` keeps Play from hiding the app on devices without them, at the cost of
  runtime failure), plus `android:largeHeap="true"` for in-process weights.
- Native resources are released in `onDestroy` (close conversation, then engine).
- There is no NPU/GPU delegate selection beyond CPU|GPU, no memory headroom check, and no
  device-tier model gating.

---

## 6. Failure modes at a glance

| Situation | What happens | User signal |
|---|---|---|
| Engine not installed, audit requested | returns `[cpu]` + `"Engine binary not found in …"` | empty-ish list, raw text in log |
| No Vulkan device on the machine | list is `[cpu]` only | wizard offers only "Auto (not recommended)" |
| Only a software Vulkan (lavapipe/swiftshader) present | shown as a normal `VulkanN` entry with its driver name | indistinguishable from real hardware |
| Backend libs fail to load on Linux (bad extraction) | audit dies in the loader → `[cpu]` | looks like "no GPU"; raw output in log is the only clue |
| Upstream changes `--list-devices` wording | regex finds nothing → `[cpu]` only | silent; raw log line reveals it |
| `--list-devices` throws / binary crashes | caught, `rawOutput = "Hardware audit failed: …"` | no list; log |
| Persisted device no longer exists | demoted to `cpu` on next audit | none (silent, arguably correct) |
| Device changed while server running | no effect until manual restart | none |
| Stale/hand-edited `--device` id | server exits at startup | log line + `isServerRunning = false` only |
| Model too big for chosen device | `fit` shrinks context, or server exits | log only; model list stays empty |
| Mobile `Backend.GPU` init fails | error surfaced; **no** CPU retry | `Failed to initialize LiteRT generation engine` |

---

## 7. Porting checklist

1. Detection = run the engine binary with `--list-devices` and parse stdout; there is no
   in-process device API in play. It therefore requires an installed engine and runs as an
   async, never-throwing subprocess.
2. Always prepend a synthetic `cpu` entry that means "omit `--device`".
3. Mirror `LD_LIBRARY_PATH` (binary dir + parent) for **both** the audit and the launch, or the
   audit reports "no GPU" on a correctly installed Linux machine.
4. Keep `rawOutput` end-to-end (return value → log → UI) — it is the only diagnostic when
   parsing fails, and it is worth promoting to the UI rather than the log.
5. Persist the device as an **opaque id string**, not an index, and dedupe parsed entries by id.
6. Enforce "selected id must exist in the latest audit, else fall back to `cpu`" in both the
   controller and the control that renders it.
7. Pass the id verbatim as `--device <id>`; omit the flag for `cpu`. Better: validate before
   launching and refuse with a message.
8. Model placement policy belongs in the preset INI, not in app code: memory-fit (`fit` +
   `fit-ctx`) for the big model, explicit `n-gpu-layers = 99` + `fit = off` for small pinned
   models. `q8_0` KV cache + `cache-ram` + mmap defaults are what make small-VRAM operation
   work.
9. Remember `--models-max 1` means all models contend for **one** device's memory; a device
   override per section is the lever for splitting them, and it requires raising that limit.
10. Re-initialize (mobile) or restart (server) when the device choice changes, and say so in
    the UI — the reference does neither.
11. Mobile: the GPU option is a single CPU|GPU enum applied at engine init, re-init on toggle,
    embedder pinned to CPU, no automatic fallback on GPU failure, and the two
    `uses-native-library` entries must exist with `required="false"`.

---

## 8. Out of scope

Engine download/verification, model bundle download, settings/`PortableSettings` layout, the
HTTP endpoints (`/v1/chat/completions`, `/v1/embeddings`, `/v1/rerank`, `/health`), document
ingest and embedding batches, the research loop and retrieval (`docs/research-loop-spec.md`),
the mobile platform-channel surface except where it carries the device choice, and every UI
detail except the device pickers.
