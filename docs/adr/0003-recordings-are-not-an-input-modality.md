# Recordings are not an input modality

Libris used to accept `.mp3`/`.wav`/`.flac`/`.m4a`/`.ogg`/`.opus`: `ffprobe` measured the
duration, `ffmpeg` cut it into fixed-length WAV slices, each slice was embedded whole and
stored with an empty `text`, and the ingest job finished with a warning — "no transcript:
audio chunks are searchable by embedding only". The path worked end to end. It also could
not do the one thing the rest of the product is built for: a hit on a recording could be
ranked, opened and shown, but never quoted, so an answer leaning on it cited nothing.

The modality is removed. The slicer, the `audio` source and segment kinds, `Provenance.T0`
and `T1`, the `input_audio` and `input_video` wire shapes, the `audioWindowSeconds` setting
and the UI's time-range citation labels are gone. A recording uploaded now is classified
`unsupported` and says so.

## Considered options

- **Ship it as searchable-but-unquotable, honestly labelled** — rejected: that makes a
  source a person can find and cannot use. Retrieval exists to put words in front of the
  synthesiser; a modality that contributes vectors and no words is a permanent caveat in
  every answer it touches, and the label only moves the caveat into the source row.
- **Transcription as a fourth model role** — rejected for now, not forever: it needs a
  whisper-class model in the pinned catalogue, a fourth llama.cpp server section in the
  generated preset, and text spans to cite into — `T0/T1` become character offsets, or a
  citation still points at a moment rather than a sentence. That is a feature with a
  download, a router section and a schema change in it, and there is no measurement saying
  it would be used.

## Consequences

`ingest.Detect` refuses a recording by its magic bytes before the extension gets a say:
"the extension wins for text" is right for a `.md` and wrong for a WAV named `notes.md`,
which would otherwise be indexed as a hundred megabytes of mojibake. Rows written before
this change stay in the database and stay recallable — including the ones whose `kind` is
now an undefined string — and the `segments.t0`/`t1` columns stay in the schema, unread:
a migration whose only effect is to break an older binary's reads, with no user on the
other side of it, is a change with no beneficiary. An older `settings.json` carrying
`audioWindowSeconds` still loads; `FileStore.Dropped()` names it once and the next save
rewrites the file.

Anything that later puts recordings back must arrive with words: a transcription that
produces citable text, not another set of vectors behind a "searchable by embedding only"
disclaimer.
