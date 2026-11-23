# 0021 — The models libris downloads are pinned, and the page offers no choice

Date: 2026-11-23
Status: accepted, and implemented — `internal/models` (catalogue, status, fetcher), `app.Host`
(the reads and verbs), `GET/POST /api/host/models*`, and `ModelPanel.svelte` under the Models
group. It closes the open item in ADR 0020 ("the menu that fetches GGUFs is not built").
It follows ADR 0018 (fetched, verified, never shipped), ADR 0011 (a setting is an ability) and
ADR 0008 (an answer keeps the evidence it was made from).

## Context

ADR 0020 started one `llama-server` in router mode and pointed it at `<data>/models`, then
left that folder empty. ADR 0020 also refused to pick the models, on the grounds that the
choice is about somebody's hardware and about answer quality. Both halves were right, and
together they are the whole design problem: the folder cannot stay empty, and the answer
cannot be a form.

What downloading a model actually differs from, in kind, from downloading the engine:

- The engine is ~35 MB and *interchangeable* — a newer build is better, and nothing in the
  data directory records which build produced anything.
- A GGUF is 0.2–1.5 GB and **load-bearing**. Every vector in the index is a function of the
  embedder that made it. Swap that file and the corpus is silently wrong: recall degrades,
  or worse, stays plausible and gets worse. ADR 0008 went to real trouble so that an answer
  still names the text it came from when the corpus moves; nothing comparable exists for a
  vector corpus whose embedder changed underneath it.
- A model is not one file. The chat model and the embedder in the catalogue each need a
  **projector** beside them (vision for the chat model, image encoding for the embedder), so
  "download the embedder" is two files and one progress bar.

And there is the ordinary web problem: 2.4 GB on a laptop connection, which a closed lid
interrupts. A download that restarts from zero is a download nobody finishes.

## Decision

**libris ships a catalogue, and the catalogue is the choice.** Two entries today, each with
both of its files, pinned in `internal/models/models.go` by repository, commit and digest:

| role | entry | repository @ commit | files | bytes |
|---|---|---|---|---|
| chat | LFM3-VL 2B · Q4_0 (QAD) | `LiquidAI/LFM3-VL-2B-GGUF` @ `1add221a` | `LFM3-VL-2B-QAD-Q4_0.gguf`, `mmproj-LFM3-VL-2B-BF16.gguf` | 1,049,615,648 + 378,996,032 |
| embed | EmbeddingGemma 2 · Q4_K_XL (UD) | `unsloth/embeddinggemma-2-GGUF` @ `031f0d4b` | `embeddinggemma-2-UD-Q4_K_XL.gguf`, `mmproj-BF16.gguf` | 175,673,856 + 982,074,880 |

2,586,360,416 bytes together. The quantisation was picked here, by whoever edits that file,
and **the settings page offers no choice of quantisation, no repository field, and no
"custom GGUF" box.** A dropdown of quants would put a quality decision — the one thing that
changes what an answer is worth — in front of the person least able to measure it, and would
make two installs disagree about what their vectors mean. Changing the catalogue is a commit,
which is the smallest unit here that can carry a measurement. Both entries are
vision-capable, which is what makes ADR 0004's page rasters and the image path worth having.

**No entry fills the rerank role.** That is a fact about the catalogue, not a missing setting:
`models.RolesOf` derives which roles the machine can fill from what is complete, so the
preset libris writes has no rerank section and retrieval falls back to vector order — and
`rerankEnabled` stays honest in external mode, where a reranker is somebody else's server.

**Files live in `<data>/models/<entry-id>/`,** a folder per entry (ADR 0005's data directory
unchanged), because removing an entry then means removing a folder rather than guessing which
loose GGUFs were whose.

**Every byte is hashed on the way in, and a part file keeps its bytes.** The fetcher streams
into `<name>.gguf.libris-part`, hashing as it writes, and on resume folds the saved prefix
back into the digest (`hashPrefix`) before asking Hugging Face for the rest with a `Range`
header. Resume is only real because `resolve/<commit>/<file>` answers `206` with
`accept-ranges: bytes` on the pinned URLs — verified against those exact URLs — and upstream's
`x-linked-etag` for such a URL *is* the file's sha256, which is where the digests in the
catalogue came from rather than from a README. Consequences that follow from hashing
incrementally:

- A digest mismatch deletes the part file. Keeping it would make every later attempt fail the
  same way forever, and the honest options are "start at zero" or "stop".
- **A file already in its final place is never fetched again.** On resume that means a skip,
  checked by a `.libris-sha256` sibling first (no re-hash) and by hashing the file when the
  marker is absent. So a GGUF copied into the folder by hand is adopted and recorded, and
  only a file that turns out to be *different bytes at the same length* is replaced. Without
  this, a stop in the middle re-downloaded the gigabytes a finished part had already placed,
  and overwrote a file whose digest had been checked with whatever upstream served that day.
- The marker is a sibling file, not a database column, for the same reason the engine keeps
  its own record: the data directory has to stay right when it is copied, moved, or emptied
  by hand.
- Progress counts bytes toward **the whole entry**, not the current file, because a bar that
  restarts when the projector begins reads as a failure.

**The card is not a setting, and it lives under the group that names it.** `internal/config`
defines no model-download fields — nothing there is a choice libris can apply at a moment's
notice (§2.2, ADR 0011) — but `ModelPanel.svelte` is mounted inside the Models group, beside
`rerankEnabled` and the probed embedding dimensions, because those three are the same
question asked three different ways, and a download at the bottom of somebody else's section
is a download nobody finds. It renders only in managed mode: in external mode the GGUFs
belong to servers libris does not start.

**The verbs are download · stop · discard · remove, and they mean four things.** Stop keeps
the bytes ("not now"); discard throws them away; remove deletes a finished entry. Remove is
refused while the router is running, because ADR 0013's rule — nothing deletes what something
is using — is about a process as much as a notebook, and the answer is the sentence naming the
model it has open. `needsRestart` is reported rather than inferred: the preset is read when
the server starts, so a model that arrived while it was up is on disk and not loadable, and
the page says so instead of waiting for a failed chat.

## Consequences

- **The catalogue is a Go file, so adding a model is a code change** — and the diff is the
  place a note about *why this quant* belongs. It also means a user cannot reach a model that
  is not here without putting the files in the folder themselves, which works, is hashed, and
  is reported as adopted rather than downloaded.
- **Two pinned commits will go stale**, and unlike the engine there is no "check for updates"
  worth building: a newer quant of the same model is a different embedder, and upgrading it
  silently would be the exact failure this ADR exists to prevent. Updating the catalogue
  belongs with a re-index, which is a decision with a cost the app can state.
- **2.4 GB is the price of a working managed install**, and the chat model alone (1.4 GB) is
  enough to answer questions over text. Nothing here enforces both.
- **Removal is guarded, but nothing re-derives vectors.** Removing an entry a corpus was
  embedded with leaves those vectors in the database and answers about them unreliable; the
  dialog says as much. A per-embedder fingerprint on segments is the thing that would make
  this precise, and is not built.
- **A partial download can hold a gigabyte** in the data directory with nothing to show for
  it. `partialBytes` is reported and the page offers to throw it away; a startup sweep is not
  built, and remains open decision 8's shape of problem.
- **The fetcher talks to huggingface.co directly.** ADR 0020 deleted the download mirror, and
  no replacement was added: a network that cannot reach HF cannot download a model, and the
  remaining path is copying files into `<data>/models/<entry-id>/` by hand.
