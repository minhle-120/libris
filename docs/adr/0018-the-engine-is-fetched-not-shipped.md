# 0018 — The engine is fetched, not shipped

Date: 2026-11-23
Status: accepted, implemented for discovery, download and removal (`internal/engine`, the
Engine section of the settings page, `docs/llamacpp.md`). **Not** implemented: starting the
downloaded server, which is the next piece and the reason this is not called done.

## Context

Everything else in this app talks to a base URL. That was ADR 0006's bargain, and ADR 0011
took the next step: capacity is not a setting, because a setting implies an ability the app
does not have. It follows that libris cannot legitimately *tune* a model server — but it can
stop making the user find, download and launch one. Three ways to get there were on the
table:

- **Ship the binaries in the release.** A Linux/amd64 Vulkan build is 37 MB compressed and
  100 MB unpacked; there are operating systems and GPU backends besides those, and every one
  of them would be in the download for the person who has exactly one of them. It also pins
  libris's release cadence to llama.cpp's.
- **Require llama.cpp to be pre-installed**, which is what the app does today: paste three
  base URLs and have a server already running. Honest, and the reason the app has never
  answered a question on someone else's machine.
- **Fetch upstream's prebuilt build on demand**, into the data directory, when the user asks.

The choice is constrained further by a fact no option escapes: which build is right depends
on the machine. Vulkan on an iGPU is a worse answer than CPU; CUDA needs the matching CUDA
runtime. So a device report is a prerequisite for the button, not a niceness — and it is not
a setting either, because nobody configures what their graphics card is.

## Decision

libris downloads llama.cpp's own prebuilt release into `<data>/bin/<tag>/` when asked, and
never ships one.

- **Nothing is pinned.** The newest `b<digits>` release with a build for this platform is the
  answer, and an installed engine that is older than it is offered as an update. Pinning was
  considered and rejected: a pin is a decision made in advance about a project that releases
  daily, and libris has no way to test one.
- **The build is chosen from a description of the machine**, collected once and cached
  overnight (`internal/deviceinfo`, `<data>/devices.json`), not from a device setting. The
  user may override the *backend* — the one thing that is a preference rather than a fact.
- **A download is verified against GitHub's own digest** and may be resumed; a stopped
  download is stopped, not discarded, and "remove what is installed" and "discard the bytes"
  are separate acts, because they have different answers about money and about time.
- **No host endpoint waits on GitHub.** The view answers from the local record and the lookup
  runs behind it, saying `checking`. Seven seconds of somebody else's API is not allowed to
  own a settings page.

## Consequences

- The first answer needs the network. A machine that is offline can still be told which build
  was newest the last time it looked, and can still be pointed at a tag by hand; it cannot be
  installed for.
- GitHub's release API is now a dependency of the setup path. It is unauthenticated and
  hourly-limited on this network, which is why the answer is ETag-cached and the page polls
  rather than refreshes.
- Disk: about 100 MB per tag, kept until someone removes it. Nothing garbage-collects an old
  tag automatically, because the thing a person fears losing here is the thing that is
  working right now.
- Upstream can regress a nightly and libris will have adopted it by the next check. That is
  the cost of not pinning, paid deliberately: the alternative is being stuck on a build that
  was already broken, with no way to know.
- The run step is not built, so today the section ends at "installed" and the base URLs are
  still typed by hand. Until that changes, the honest description of this feature is
  "it fetches the thing", not "it runs the models locally for you".
