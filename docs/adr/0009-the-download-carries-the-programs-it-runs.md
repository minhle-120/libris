# The download carries the programs it runs, and says where a program came from

A Windows user uploaded a PDF. Nothing appeared. The row said the file was unsupported, and the
finding under it said `pdftotext is not installed here. Install it, then run this file again.`
Nothing was wrong with their PDF and nothing was missing from libris's own build — what was
missing were two programs libris shells out to and had never shipped. The advice was true in the
abstract and useless in particular: a person who downloaded a Windows binary has no idea how to
"install pdftotext", and should not need to.

The binary had been built `CGO_ENABLED=0`, and a comment beside that said it made the download
self-sufficient. It made *libris* self-sufficient. Reading a PDF was never libris's own work.

## Decision

A release is an archive, not a bare executable: `libris` and a `tools` folder beside it holding
`pdftotext`, `pdftoppm`, the library closure those two resolve to, and — on Linux — a folder of
fonts with a fontconfig file naming it. Unpack and run. `internal/tools` resolves a program by
name against those two places, and every program answer carries **where it came from**: `bundled`,
`path`, or `missing`.

Which place is checked **first** is not a preference and not shared between the platforms:

- **Windows takes the bundle first.** A `pdftotext` found through PATH there came from MSYS2 or
  Git for Windows. It cannot read the `C:\…` path libris writes as an argument, and it will not
  start outside the DLL folder it was built beside. "The machine has poppler" is so often false in
  practice that checking it first would break a working download.
- **Linux takes the machine's own first**, because poppler's data folder — the CMaps and CID tables
  a Chinese or Japanese text layer is decoded with — is `POPPLER_DATADIR`, a path **compiled into**
  the library at build time (`/usr/share/poppler`). On Windows the same path is computed at runtime
  from where `libpoppler` sits. So a copied library on Linux can never be made to find the data
  shipped beside it, and the system library always finds its own.

That asymmetry is a fact about somebody else's library, discovered by reading
`poppler/GlobalParams.cc` rather than remembered, and it is the reason the two bundles differ and
the two lookups disagree. It is also a real cost, stated rather than hidden: **on a Linux machine
with no poppler installed, a CJK text layer extracts to blanks** while the page still rasterises
and is still searchable by image. Closing that means building poppler from source with a data path
libris can point at, which is deliberately not part of this pipeline yet. No unreachable data is
shipped to pretend otherwise.

A bundled program is run with an environment of its own — `LD_LIBRARY_PATH` at the bundle's `lib/`,
`FONTCONFIG_FILE` at the bundle's `fonts.conf` — because a program that did not arrive by that
machine's package manager cannot be assumed to find what it needs. A program from PATH is run with
the environment it already has. The merge replaces a key rather than appending a duplicate: with a
key present twice, which copy the child reads is a property of libc, not of libris.

Because the install kind is now a fact, the finding's sentence can be the right one. `internal/tools`
answers `BundledHere()`, `/api/host/tools` carries it, and the source row says *install it, then run
it again* on a machine that uses a package manager and *the tools folder beside libris is missing; if
libris was moved, move the folder with it* on one whose programs were never its owner's to install.
This amends the parenthetical in ADR 0008 — `tool_missing` no longer means "not on PATH", it means
"in neither place", and only the place decides which sentence the code renders.

Resolution happens **per job**, not once at boot: a Processor is built per upload, and the resolver
is asked again there. In-process `PATH` changes are still invisible to a running process, so "install
it, then run this file again" stays the sentence it was — but the folder beside the executable is
re-read, which is the case a person can actually fix without restarting.

## Considered Alternatives

- **Instruct, don't ship.** A README line and a finding that says which package to install. Rejected:
  the request was explicit, and it is the correct call — a person who downloaded a binary should not
  be sent to a package manager they may not have, and on Windows the package manager answer is
  "install MSYS2", which is a bigger install than libris.
- **Static musl builds of poppler, so there is no library closure at all.** Rejected after checking:
  Alpine ships no static poppler (`poppler-static` does not exist in the community repo), so the
  alternative is a from-source build of poppler *and* its eleven dependencies in CI. That is the same
  argument as the CJK data above, and it is worth having when the pipeline can afford a compile that
  takes minutes. A release pipeline that mostly ships a Go binary should not start by depending on a
  C toolchain.
- **AppImage on Linux, which solves relocation by mounting at a fixed path.** Rejected: the fixed path
  (`/app`) is what makes the data-dir problem go away, and it only exists inside an AppImage runtime —
  so the fix is smuggled in through a mount nobody can inspect, and a `.AppImage` is not "unpack this
  anywhere" for a user who just wants a folder.
- **PATH first everywhere, one rule.** Rejected on the Windows facts above; and the reverse (bundle
  first everywhere) is rejected on the CJK facts, since it would throw away a system poppler that
  works better than the bundle does.
- **Let the source row word its own finding.** Rejected: the row cannot see the disk, and ADR 0008 put
  sentences in the UI and facts in the backend. The new fact is `bundled`, and it arrives over
  `/api/host/tools` beside the per-program origins.
- **Ship poppler-data on Linux anyway.** Rejected as dishonest padding: an unreachable folder reads as
  support. What Linux gives up is written above instead.

## Consequences

`make build` and `go run` produce a libris with no `tools` folder, so a source checkout resolves
through PATH, exactly as before. `GET /api/host/tools` is new and reads the disk on every call —
the answer changes without libris doing anything. The release assets are now
`libris-<tag>-<os>-<arch>.tar.gz` / `.zip`, so a script that downloaded the bare binary breaks: the
checksum file and the release page name the archive. And a Windows user who moves `libris.exe` out of
its folder gets a sentence that names the folder, because that is now the most likely way to break a
working install.
