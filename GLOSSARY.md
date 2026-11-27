# libris

A local-first research notebook: a person's own documents, their own machine, and a model
that answers from them with references back to the page.

## Language

**Notebook**:
A set of sources answered together. A question only ever ranges over one notebook's sources.
_Avoid_: Workspace, project, collection

**Source**:
One uploaded file, with the place it came from kept so a citation can open it. The same file
in two notebooks is one source held by both.
_Avoid_: Document, file, upload

**Finding**:
What a run noticed on its way to a source's status: a stage that gave up, a program this machine
does not have. Two kinds, because two actions exist — install something and run the file again, or
accept what the file gave. A finding never changes the status; it says what to do about it.
_Avoid_: Warning, note, diagnostic

**Tool folder**:
The `tools` folder that travels beside the libris executable, holding the programs libris runs but
 did not write — pdftotext, pdftoppm, ffmpeg and ffprobe — with the libraries and fonts those
 programs need. It is
 what makes a download enough on its own. A program found there is **bundled**; one found through
 PATH was installed by the machine's owner. The two are run differently, because only one of them
 arrived carrying its own dependencies.
_Avoid_: Dependencies, runtime, vendored binaries

**Segment**:
One embeddable piece of a source — a stretch of page text, one page's image, or one clip of
something that runs. The unit a vector is stored against and the unit a citation names.
_Avoid_: Chunk, slice, block

**Recording**:
A source whose content is sound rather than pages, or the sound a video carries — either is stored
as one segment per clip window and answered from by ear. Nothing writes out what was said.
_Avoid_: Audio file, voice memo, transcript

**Video**:
A source that runs: pictures over time, and usually a soundtrack beside them. It is stored as two
documents of one source — its frames, and its sound — each with its own segments over the same
moments, because a model shown the video is shown the pictures only.
_Avoid_: Movie, footage, visual, screen recording

**Frames**:
What a video becomes for a model: pictures sampled from it at a fixed rate. The pictures carry what
is visible and nothing that is said.
_Avoid_: Keyframes, thumbnails, stills

**Soundtrack**:
The sound a video carries, kept and searched apart from the pictures it belongs to — the only place
the words spoken in a video exist for a model.
_Avoid_: Audio track, narration, voiceover

**Clip**:
The seconds one media segment covers — a window of a recording, of a video's frames, or of a
soundtrack — written at ingest and playable. The segment is what retrieval finds; the clip is what
a person sees or hears.
_Avoid_: Slice, snippet, excerpt

**Citation**:
A frozen reference to a segment as it was when an answer was given, kept so history still
opens after the corpus changes.
_Avoid_: Reference, footnote, source link

## Serving a model

**Managed mode**:
libris runs the model server itself, and derives where to reach it.
_Avoid_: Local mode, auto mode

**External mode**:
Somebody else runs the model server, and a person says where it is. Nothing is derived.
_Avoid_: Remote mode, BYO server

**Router**:
The one model server libris runs, holding every model it has on disk behind one address.
_Avoid_: Engine, llama-server, backend

**Endpoint**:
The single address the router answers on. One address, not one per model.
_Avoid_: Base URL, host

**Alias**:
The name libris gives a model so it can be asked for by the same word every time, whatever
file it was loaded from.
_Avoid_: Model name, id

**Target**:
What to dial and what to call it, for one set of settings at one moment: derived in managed
mode, taken as written in external.
_Avoid_: Served config, resolved settings, endpoints

**Derived, not stored**:
A fact about the running process rather than the settings — an address, a port, an alias —
worked out when it is needed and never written back as a setting.
_Avoid_: Computed setting, runtime config

**Stopped**:
The router is not answering, so there is no address to dial. Not "unreachable" and not
"failed": nothing was asked.
_Avoid_: Offline, down, unavailable

## Fetching

**Stop**:
Interrupt a running download and keep the bytes, so the next attempt resumes. Not "cancel":
cancelling is what **Discard** means, and the two are separate buttons because a person pressing
one does not usually mean the other.
_Avoid_: Cancel, pause, abort

**Discard**:
Throw away the bytes a stopped download is holding. The act is about the disk, not about the
transfer, which is why it is not written as "delete the file".
_Avoid_: Delete, clear, cancel

## Setting up

**Setup**:
Whether this machine can answer a question yet, and what is in the way: an engine on disk, a
confirmed device, both model roles filled, the router up. One program's rule, which the
settings screen renders and start-up obeys — so the screen cannot call a step unfinished that
the program has already acted on.
_Avoid_: Readiness, onboarding, first run

**Confirmed**:
Somebody saw what this engine build reports about this machine, and chose one of the devices it
names. Not inferable from the device setting, which is blank both when libris picks
and when nobody was asked; and not permanent, since a new build reports different devices and a
copied data directory sits on different hardware.
_Avoid_: Acknowledged, accepted, dismissed
