# 0019 — The engine is the only device probe

Date: 2026-11-23
Status: accepted, implemented (`internal/deviceinfo`, asked by `app.Host.Devices`, rendered by
`web/src/lib/EnginePanel.svelte`). It narrows what ADR 0018 assumed — "The build is chosen
from a description of the machine, collected once and cached overnight" — by deciding who is
allowed to give that description.

## Context

`internal/deviceinfo` began with six collectors and a merge between them: sysfs for the PCI
bus and its vendor ids, `lspci` for names a person would recognise, `nvidia-smi` and
`rocm-smi` for memory, `Win32_VideoController` on Windows, `system_profiler` on macOS — and
`llama-server --list-devices` last, merged into whatever the others had found.

Every one of the OS probes knows a fact the others do not, which is what made the merge
necessary and what made it wrong:

- `lspci` prints "NVIDIA Corporation GA104 [GeForce RTX 3070]" and **no size**; `nvidia-smi`
  prints "NVIDIA GeForce RTX 3070", a size and a driver version. Two names for one card, so
  one row had to be recognised as one card — with a rule that had already almost been
  "one name contains the other", which merges an RTX 3060 and an RTX 3060 Ti and silently
  deletes a GPU from the report.
- `Win32_VideoController.AdapterRAM` is a `uint32` and **caps at 4 GiB**: an 8 GB card
  reports its ceiling.
- Neither `lspci` nor sysfs has any idea which of them is usable by the binary libris is
  about to start, and none of them knows what is **free right now** — only a live ggml init
  sees what another program is holding.
- `rocm-smi` is slow or hanging on machines without it, so it is run only behind a vendor-id
  test on rows that came from somewhere else.

So the report described the machine in a way the program about to run on it would immediately
contradict, and the interesting engineering in the package was agreement between sources
rather than the answer.

## Decision

`llama-server --list-devices` is the only device source.

- **Deleted**: the sysfs, `lspci`, `nvidia-smi`, `rocm-smi`, WMI and `system_profiler`
  collectors, their parsers, and the merge between sources they needed. `Device` loses
  `driver`, `bus_id` and `bus_vendor` — nothing can fill them any more, and a field no
  source sets is a field a reader will be shown as a blank.
- **The CPU row is the one row not taken from the engine**, because `--list-devices` skips
  CPU devices (`common/arg.cpp:1157`) and so cannot say that a CPU exists. It is a constant
  of the running binary — `runtime.GOARCH` — not a probe: nothing is read or run. It carries
  no model name, because reading `/proc/cpuinfo` for one is exactly the kind of second
  source this ADR is about.
- **One merge survives**, and it is inside a single source: a build that loads two backends
  lists the same card twice (`CUDA0:` and `Vulkan0:` with the same description and size).
  Those become one row naming both slots, because a slot name is what `-dev` takes and the
  count of *cards* is what the warning about choosing must get right.
- **The cache key becomes this boot plus this engine build.** Enumerating the PCI bus to
  catch a card moving slot was keying on a source the report no longer consults. Installing
  and removing an engine already invalidate the cache where they happen, and the age
  (`deviceCacheAge`) still covers what neither can see.
- **With nothing installed, the report says it has not asked.** "Nothing has been downloaded
  yet, so this machine has not been asked what it can run on" — not "no graphics adapter was
  reported", which is a sentence about hardware nobody looked at. The two are different
  answers, they lead to different downloads, and both are asserted.

## Consequences

- **A first run cannot see its devices until the engine lands.** This is the cost, and it was
  accepted deliberately: the question a device report answers is "can *this program* run
  here", and the only witness to that is the program. The settings page's machine section
  says so where the devices would be, and the row refetches the moment a download changes the
  engine tag.
- **The report is now about a build.** A CUDA-only engine on a box that also has Vulkan
  reports CUDA, and an engine update re-asks. That is the accuracy the OS probes could not
  give, bought with a cache that must — and does — treat the build as part of the machine.
- **A driver problem is harder to name and no harder to see.** There is no driver version
  string in the report; there is the card's own description, and when the engine is present
  and fails, its complaint is carried into the warning (on one line, capped — `--list-devices`
  can print forty lines of ggml's startup log to stderr).
- **One subprocess instead of up to four**, and none at all on a machine with nothing
  installed. Measured 1.7 s for the call on the development laptop; the 30 s ceiling means
  what it always meant — a hang there is a broken driver.
- `internal/engine` no longer carries a device probe. Its `AskDevices` was deleted: never
  wired to anything, because the Linux collector ran the binary itself.
- The flag is now load-bearing, so its contract is written down with the other ones in
  `docs/llamacpp.md`, including the trap in it — a description that contains its own
  parentheses, which makes "the last parenthesised group" the wrong parse — and
  `TestDetectAgainstARealEngine` runs it against a real binary when `LIBRIS_ENGINE_BIN`
  points at one.
