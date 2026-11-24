# 0022 — The model server's configuration is a file libris constructs

Date: 2026-11-23
Status: accepted, implemented (`engine.Preset`, `engine.ReadPreset`, `engine.Supervisor.Start`;
written to `<data>/presets/libris.ini` — the location below was moved by ADR 0023 — read back on
every start, described in `docs/llamacpp.md`). It settles the half ADR 0020 left open: router mode needs a preset, and
someone has to own what is in it.

## Context

Managed mode had already moved capacity and sampling out of the request (ADR 0006) and out of
the settings (ADR 0011), into a preset file libris wrote next to the binary it runs. That was
correct in direction and unfinished in fact. The file was assembled from constants in
`internal/engine`, and then **rewritten on every start** — so it was not a configuration, it
was a build artefact with a `.ini` suffix, and two things followed from that:

- A person with a 4 GiB card and a 16 GiB one could not do the obvious thing. llama.cpp's own
  tuning knobs — `cache-ram`, `ctx-size`, a quantisation to try against the pinned one — are
  arguments to `llama-server`, and libris offered exactly one of them (`-fit` left on) while
  silently holding the rest.
- Every number in that file was unmeasured. The preset said `ctx-size = 8192` on the chat model
  because 8192 was a plausible number, and `-fit` does not adjust an argument that was set, so
  that guess was load-bearing rather than advisory.

Meanwhile the device question arrived with its own evidence: `--list-devices` on the build
libris downloads prints `Vulkan0: AMD Radeon 660M …` and `Vulkan1: NVIDIA GeForce RTX 3050 Ti …`,
and passing it `--device CUDA0` — the name on the same laptop's self-built CUDA engine — is
`error while handling argument "--device": invalid device: CUDA0` followed by exit. A device is
therefore not a preference to store and hope with; it is a name from a list only the installed
build can print, and the wrong one is a server that does not start.

## Decision

The file is the configuration. libris constructs it, and afterwards reads it.

- **It lives at `<data>/bin/presets/libris.ini`** — beside the binary that reads it, as decided
  here; ADR 0023 moved it to `<data>/presets/libris.ini`, since Remove deletes the binary's
  directory and must not delete this. Either way it is the only place libris's own model
  server's arguments exist. Not flags assembled at a call
  site, not a settings group, not on the wire.
- **`engine` constructs and reads; it does not overwrite.** Before each start the supervisor
  reads the file and compares it against the three things libris derives: which models are
  complete, which file each one is, and which device to offload to. If those agree, the file is
  left byte-for-byte alone — every other key in it, written by libris or by hand, is passed to
  llama.cpp untouched. If they do not, libris writes it again.
- **What libris writes is what it has measured.** `ctx-size` 32768 on the chat model (confirmed
  to load: `GET /models` reports `n_ctx` 32768 for `libris-chat`) and 4096 on the embedder (its
  training limit); `cache-ram` 2048 for the chat model and 0 for the two small ones; the five
  sampling keys on the chat model, because ADR 0006 says sampling belongs to the server and
  this file *is* the server; `pooling` left absent, because the model file already answers that
  and measuring it said so.
- **The device is derived, not guessed.** `deviceinfo.Report.DefaultDevice` is the rule — the
  first Vulkan device the installed engine reported, else `none`, which is llama.cpp's word for
  "do not offload". `engineDevice` is a setting because libris writes it (ADR 0011), and it is
  a string rather than an enum because the legal values are that report; the control is a
  picker over the report, beside the report, and the setting is marked `noview` so the generated
  form does not also offer a text field for it.
- **The running process reports the file's device**, not the setting's (`Serving.device`),
  because a blank setting means libris picked and a hand-edited file means the file picked.

## Consequences

- A hand edit survives until something derived moves, and then it is lost without ceremony.
  That is the trade the file exists to make: libris cannot both honour an edit to a KV cache
  size and guarantee the preset matches the models on disk. The header of the file says the
  rule in the four lines a person reads before editing.
- Editing a `model =` path is not a protected edit — paths are derived, so a re-pin
  (ADR 0021) or a re-download rewrites the section. Anyone wanting to run different bytes from
  the pinned ones should know that, and the honest answer today is that they are outside the
  catalogue and outside its guarantees.
- A key llama.cpp does not recognise, or a device name it does not have, is a server that exits.
  It surfaces as 409 with the last line of the server's own log (`ErrEngineStart`), not as a
  silent retry and not as a 500. `ErrEngineRunning` is a separate sentinel because
  autostart makes "already running" the ordinary case, and answering it with the ingest queue's
  sentence ("ingest queue is full") told people to wait for something that was never running.
- The settings page can now disagree with the process, and says so: the picker shows what the
  file is holding once the server is up ("Running on Vulkan0"), so the difference between what
  was asked for and what is answering is on the screen rather than in a log.
- Numbers here are the measured ones, and the page that records why is `docs/llamacpp.md`. A
  future change to one of them is expected to arrive with a measurement, not with a preference —
  which is also why none of them is a setting: a knob whose correct value has not been
  established is a way to be wrong more expressively.
