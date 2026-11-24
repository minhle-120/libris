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

**Segment**:
One embeddable piece of a source — a chunk of page text, or one page's image. The unit a
vector is stored against and the unit a citation names.
_Avoid_: Chunk, slice, block

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
Somebody saw what this engine build reports about this machine, and chose a device or told
libris to pick. Not inferable from the device setting, which is blank both when libris picks
and when nobody was asked; and not permanent, since a new build reports different devices and a
copied data directory sits on different hardware.
_Avoid_: Acknowledged, accepted, dismissed
