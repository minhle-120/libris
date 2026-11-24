# 0023 — The preset lives in the data directory, not beside the engine

Date: 2026-11-24
Status: accepted, implemented (`engine.PresetsDir`, `engine.PresetPath`, `engine.RouterID`;
`app.Host` is handed the path as `HostOptions.PresetPath` and passes it as
`engine.ServeOptions.Preset`, asserted by `TestPresetIsWrittenWhereItWasToldAndNotBesideTheEngine`
and `TestPresetPathIsUnderTheDataDirectory`). It moves one bullet of ADR 0022 and changes
nothing else in it: the file is still the model server's configuration, `engine` still
constructs and re-reads it, and it is still not a settings group.

## Context

ADR 0022 put the preset at `<data>/bin/presets/libris.ini`, beside the `llama-server` that
reads it, because "where is the file the process reads" has one answer that way.

The engine directory is not stable. It holds the build libris fetched (`b12651/`), and the
settings page has a **Remove** button whose whole job is to delete it — an engine you did not
choose is a binary you should be able to throw away, and ADR 0018 says it is fetched on demand
rather than shipped, so it is the one thing in the data directory that is genuinely
disposable. The `<data>/bin/` tree is also replaced wholesale on an update.

The preset is the opposite of disposable. ADR 0022's own reasoning is that the file *is* the
model server's configuration — the only place `cache-ram`, `ctx-size`, `flash-attn` and the
rest exist on this machine, and the one place a person can read and change llama.cpp's
behaviour without touching a single setting. So pressing Remove to change which *binary* runs
also deleted the record of *how* it was running, and the next start silently reconstructed
defaults over what a person had tuned. That is the failure ADR 0011 is about in a new costume:
a fact the app still believes, held somewhere the app can lose.

## Decision

The preset lives at **`<data>/presets/libris.ini`**.

- `engine.PresetsDir(dataDir)` is `<data>/presets`; `engine.PresetPath(dataDir, id)` is
  `<data>/presets/<id>.ini`; `engine.RouterID` is `libris`, so the process, the file and the
  log say the same thing about the same thing.
- `app` is *handed* the path (`HostOptions.PresetPath`, computed once in `cmd/libris` from
  `-data`) and passes it through as `ServeOptions.Preset`. `app` does not know the file name,
  the extension, or that they are INI at all — the format and the name stay `engine`'s, and
  the data directory stays `cmd`'s. This is the same shape `ModelsDir` already has.
- The log stays at `<data>/bin/logs/server.log` and the supervisor's record at
  `<data>/bin/serve.json`. Both are *consequences* of running that build: a log about a binary
  you removed is history of something that no longer exists, and the pid record is meaningless
  once the process is gone. Only the configuration is meant to outlive the engine, so only it
  moves.
- An existing `<data>/bin/presets/libris.ini` is not migrated. libris finds no preset at the
  new path, writes a fresh one from what it derives, and the old file sits there harmlessly
  until its directory is deleted by Remove. Hand-tuned numbers are worth keeping across a
  binary update, which the new location now guarantees; they are not worth a migration that
  has to decide what to do when both files exist.

## Consequences

- `Remove` and an engine update are safe in the way they always looked: they delete the engine
  and nothing a person wrote. The data directory's contents now split cleanly — `presets/`,
  `models/`, `devices.json`, `settings.json`, `libris.db` are libris's and the user's; `bin/`
  is llama.cpp's.
- "Where are libris's files" is one answer (`-data`) and `bin/` is the only exception, which
  is easy to state because it is the only thing that can be re-fetched.
- A preset copied from another machine is still a server that will not start if it names a
  device this build does not have (ADR 0022, measured). Moving the file changes the guarantee
  about *keeping* it, not the one about *trusting* it — the picker is still a picker over what
  `--list-devices` prints, not a text field.
