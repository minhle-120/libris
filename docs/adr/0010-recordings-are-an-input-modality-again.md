# Recordings are an input modality again, and arrive as sound

ADR 0003 removed the recording: libris could slice one, embed the slices and rank them, but a hit
on a recording could never be quoted, so "an answer leaning on it cited nothing". Its closing rule
was that anything putting recordings back must *arrive with words* — a transcription producing
citable text, "not another set of vectors behind a 'searchable by embedding only' disclaimer".

Recordings come back anyway, without words, and the rule is dropped. A recording is sliced into
overlapping windows, each window is embedded whole and stored as a segment with an asset and no
text, and the answering model is handed the sound itself and says what it heard. Nothing anywhere
stores a transcript.

## Why the objection no longer lands

0003 rejected this twice over, for two reasons that have both gone.

**It priced transcription.** A fourth model role, a whisper-class model in the pinned catalogue, a
fourth section in the generated preset, and `T0/T1` becoming character offsets so a citation could
point at a sentence rather than a moment. None of that is needed now, because the two multimodal
projectors the app already ships carry native audio encoders on disk — `clip.has_audio_encoder` is
true in both, the embedding side's audio tower projects into the same 512 dimensions as its vision
tower, and `GET /props` on both running instances answers `audio: true`. There is no fourth model,
no download, no router section, no schema change. What 0003 refused was a feature's cost, and the
cost is already paid and sitting in `models/`.

**It said the words would never come.** They come, inside the answer. A clip delivered in a
`role:"tool"` result is heard and quoted verbatim by the shipped answer model; two clips in one
result, two clips across two results, and a clip beside a page raster in the same result all work,
in both part orders. The honest half of that sentence is preserved here rather than smoothed over:
the **row** is still unquotable — open a recording's segment and there is no sentence to read, only
a player — while the **answer** is quotable, because the model that writes it listened. A person
checking an answer by clicking through to the row still gets less than a text hit gives them.

## What was measured before deciding

Every number came from a bare `llama-server` on this machine, running the app's own preset flags
against the two GGUFs already on disk. The measurements came first and the build after them, so
nothing was claimed that libris itself had not done: the path now runs end to end against the real
server (`make e2e`: a recording uploaded over HTTP is sliced by the machine's ffmpeg, embedded as
`input_audio`, and its clip served back as `audio/wav`), and over a corpus of thirty minutes of real
recordings (`LIBRIS_AUDIO_CORPUS=... go test ./internal/ingest/ -run RealRecordings`: 231 clips, no
part of any recording outside a clip, every neighbouring pair sharing the overlap).

Audio costs **25.4 tokens per second** on both models. A 15 s clip embeds in 0.14 s warm; 86 s of
audio in 0.67 s. An hour of a recording is roughly 240 clips and 35 seconds of embedding.

The window length is the finding that changed the design. Same 60 s recording, cut four ways, same
queries, cosine of a text query against each clip's vector:

| window | exact quote from the clip | paraphrase, different words | a different line, same window |
|---|---|---|---|
| 15 s | **0.845** correct | **0.797** correct | **0.789** correct |
| 30 s | 0.816 | 0.788 | 0.737 |
| 60 s | 0.744 | 0.727 | 0.737 |

Pooling averages the whole clip, so one sentence is diluted by the length of window around it: at
60 s the right answer lost to a clip of silence. 15 s is the shortest window that still holds a
whole sentence, and it won every query. Neighbouring windows overlap by half, for the same reason
text chunks do: a sentence straddling a boundary survives inside at least one clip.

Two smaller facts, both of which became code:

- A silent clip is not inert. One second of silence scored 0.780 against an unrelated content query
  and **beat the correct answer**, so a window whose loudest sample sits under a hundredth of full
  scale is not stored. The test is a pass over the samples in Go rather than ffmpeg's own silence
  filter: it runs on the bytes already in hand, one decode pass instead of two, and it drops the
  clip's row as well as its file — which a filter upstream cannot do, since the segment is built
  downstream of the decoder either way.
- A chunk's name must sit in the text directly above its own media part. Lumping four chunk names
  into one text blob alongside four clips mis-assigned one of four, and tripled the output tokens.

The old implementation's own comment corroborates the batch-size half of this. It chose 30 s because
*"EmbeddingGemma 2 costs roughly 25 tokens per second of audio, and llama.cpp must fit one embedding
input in a single ubatch — about 1022 usable tokens at the recommended `-ub 1024`. 30 s lands near
750 tokens. Longer slices need a larger -ub."* The preset in this tree sets no `-ub` at all, so the
batch is llama.cpp's 512 default and even 30 s fails outright. That is why the preset change ships
with this, and why it fixes something page images already hit.

## Considered options

- **Transcribe, as 0003 preferred** — still rejected, and now for the smaller reason: it wants a
  download, a router section and a schema change, and it buys citable rows at the cost of putting a
  lossy step between the recording and the answer. The objection that made it necessary — no words,
  no citation — no longer applies.
- **Leave recordings refused** — rejected: the capability is already on disk, the unquotability that
  justified the removal is answered, and a person dropping a recording into a notebook today is told
  the one thing their machine is fully equipped to handle is unsupported.
- **Search the sound, but keep it out of the answer** — rejected: searchable-and-unheard is the
  half-modality 0003 removed, re-added with extra steps.
- **Reserve retrieval slots for audio** — rejected, and this contradicts nothing but is worth naming:
  audio vectors measured 0.05–0.10 below the equivalent text chunk, which looked like a reason to
  guarantee clips a place in the evidence set. It would have overridden ADR 0002 (retrieval is
  ordered by the embedding model, and by nothing else) on the strength of **two hand-made clips
  scored against a synthetic transcript** — a sample too small to be evidence. On real clips, in
  real queries, clips won their own queries outright. If a real notebook later shows recordings never
  surfacing, that is a measurement and a new ADR, not a rule added now.

## Consequences

`KindAudio` exists again, `Provenance.T0/T1` exist again (seconds, `float64`, the columns having
stayed in the schema unread since 0003), `input_audio` exists on the wire again, and the `m:ss`
ranges return to the UI. Retrieval is unchanged in its ordering — one ranking, the model's order.

Three costs, stated:

- **The rows the old code wrote are deleted**, once, at startup: `store/sqlite/purge.go`, run by
  `app.Service.Start` before anything can read the library, marked in `schema_migrations` so it is
  never run again. A migration file could not have done it — "is this row legacy" is a question about
  content, and once this build defines `audio` again the rows it matches are new ones, so a versioned
  migration and a marker are the same thing and only one of them can be the truth. 0003 left those
  rows in the database as undefined-kind rows, "recallable" but unreadable; adding the kind back
  would wake them as 30-second windows that score worse than new ones and sit at the batch limit the
  old preset was written around. This is the one place the change removes a person's data, and it is
  narrow: segments whose `kind` is `audio` written by a build that no longer exists, plus any source
  made of nothing else, with the files unlinked only after the rows that cited them are gone.
- **`ffmpeg` becomes a bundled program** under ADR 0009 — the same resolver, the same
  `bundled`/`path`/`missing` origin, the same finding that names where the program should have come
  from — and it is the only new one. A build that cut recordings into fixed windows had to ask how
  long one was before reading any of it; this one decodes once and learns the length from the samples
  as they arrive, so `ffprobe` has no question left to be asked for, and a second program would be a
  second process per upload and a second answer that can disagree with the first.
  `scripts/dist/tools-{linux,windows}.sh` take it from the same package manager the PDF tools come
  from — apt inside the release container, MSYS2's pacman on Windows — rather than from a static
  build downloaded off a third party, because that keeps one promise (the container's glibc is the
  oldest one the result starts on) and adds no dependency on somebody else's archive staying where it
  was. What that costs is named where it is paid: a distribution builds ffmpeg with every codec it
  ships support for, so the copied closure is tens of megabytes of encoders libris never runs, and
  cutting a build down to the five decoders a recording actually needs is the way to shrink it.
- **`ubatch-size = 4096` goes in the preset**, which is not only for audio. Page rasters are
  currently capped to 256 tokens by the 512 default — the server says so in a log line and carries
  on — so this raises what a page image contributes to an answer today, unasked. Worth knowing
  because it costs memory on small cards.
- **A model hears at most three clips per call.** A clip costs 25.4 prompt tokens a second and the
  server spends about 0.14 s decoding each fifteen-second one, so a full evidence set of ten clips
  would be four thousand tokens of audio and a second and a half of decoding before the model had
  read a word. What is dropped is the tail of retrieval's own ranking, and its label stays in the
  prompt with a sentence saying it was not played — the model learns the recording goes on instead
  of concluding the three clips it was given are the whole of it, and cannot quote a window it never
  heard. Retrieval's ordering is still untouched; this is a delivery limit on one call, in the same
  place that already refuses to send pixels a model was not started to see.

The old code is deleted at `692c7c7` and none of it is revived; the path is written fresh. What it
knew had to be re-derived, because those facts are not invented twice for free, and each is now a
line somewhere in `internal/ingest`: 16 kHz mono PCM out, and nothing else; a ceiling on the samples
one recording may produce, so a decoder that will not stop cannot write clips forever; a program
that is absent reported as a missing dependency rather than as a file that failed to parse; the
magic-byte guard that keeps a WAV named `notes.md` out of the text extractor and an MP4 whose brand
says *video* out of ffmpeg; and the fact that the answer to "how long is this recording" arrives by
reading it, which is why a corrupt container that yields no samples is a failure rather than an empty
success.
