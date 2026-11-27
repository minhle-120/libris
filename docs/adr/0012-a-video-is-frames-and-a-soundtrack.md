# A video is stored as frames and as a soundtrack, never as one clip

A video is a source with a timeline and pictures, and the obvious storage is the obvious one: cut
it into windows like a recording, hand each window to the model whole, one row per window. That is
what this ADR does not do. A window of a video becomes **two** segments over the same seconds — one
carrying the frames of it, one carrying its sound — in two documents of one source, retrieved as
two independent rows. The reason is that a clip sent as `input_video` is *only* pictures, whatever
the file it came from contains.

## The soundtrack is not in the video

One 12 s clip, made for the test: the frames spell a word and nothing else, the soundtrack says
"The swimming pool closes at nine thirty tonight." Nothing about pools is in the pixels.

| | |
|---|---|
| the clip, sound and all ↔ text of that sentence | +0.617 |
| the same clip **muted** ↔ the same text | **+0.630** |
| that audio alone, as `input_audio` ↔ the same text | **+0.825** — control: the audio tower is in the model, in the same space |
| clip with speech ↔ clip muted | +0.950 — encode noise, not words |

Muting it changed the answer by less than the difference between two runs. And the answer model
says the same thing in words: asked to *"describe what you see, then transcribe anything you
hear"*, it wrote *"There is no sound."* The identical bytes sent as `input_audio` came back as
*"a swimming pool clothes at 9:30 tonight"*.

`llama.cpp` agrees, in the singular case: `tools/server/README.md:1472` — *"Models that hear their
video (LFM3-Omni) also get its soundtrack"* — and `mtmd.cpp:3390-3401` gives real video handling to
`PROJECTOR_TYPE_LFM3VL` and `PROJECTOR_TYPE_QWEN3VL` and nobody else. Of the two, `lfm3-vl-2b` sits
in `internal/models/models.go` `retiredDirs`: libris **deletes** the only model in its catalogue
that fuses the two. Writing a fused clip would be writing a row for a model this app uninstalls.

So the frames are one row and the words spoken are another. Neither is a copy of the other, both
carry the same `Span{T0,T1}`, and `00:07–00:23` is one moment and two rows — which is what the
viewer already draws for a PDF page, whose text and raster are also two rows over one page.

## Cost, and the ceiling that comes with it

Frames are 107 prompt tokens apiece on both shipped projectors, and `--video-fps` **resamples
rather than caps**: at one frame a second a 15 s clip is 15 frames and 1622 tokens; at llama.cpp's
own default of four it is 60 frames and 6482, which the server refuses outright (`input (6482
tokens) is too large to process`), at `-ub 16384` too. One frame per second is therefore written
into the preset as a constant and not offered as a setting, and the clip length is the setting —
its maximum, 30 s, is where the batch runs out, not a preference.

The clip file is `testsrc2` at CRF 23 for 15 s: 640×360 = 1.42 MB, 1280×720 = 5.59 MB, 1920×1080 =
11.45 MB. A clip is inlined as base64 into the embedding request, and `index.MaxAssetBytes` is
32 MB — 1080p passes, and not by a margin that should stop anybody thinking about it.

Adding the soundtrack's rows back: 25.4 tokens per second of audio (ADR 0010), so a 15 s window of
a video costs roughly 2k tokens across its two rows and produces twice the rows a recording does.

## Considered options

- **One fused clip per window** — `{"content": [video, audio]}` yields one vector, and it is what
  the interleaved-input shape in llama.cpp's own guide describes. Rejected: it doubles the bytes in
  the one row that has to fit a batch, it needs a domain change (`domain.Segment` holds one asset),
  it welds two window rules into one row, and nobody has measured it beating two rows that already
  sit +0.825 and +0.617 from the same sentence in the same space.
- **Frames only, no soundtrack** — rejected. Everything a person *says* in a video would be gone,
  and ADR 0010 exists because sound is where answers are.
- **The soundtrack only, pictures dropped** — rejected for the mirror reason: a slide, a chart, a
  whiteboard and a face are all in the pixels and nowhere else.
- **Transcribe the soundtrack** — rejected by ADR 0010 already, and the cost is unchanged: a fourth
  model role for something the shipped projector does natively.
- **Merge the two rows after ranking** so one moment cannot take two of the three clip slots —
  rejected, and this is the part worth recording: it would be the second thing in the app
  overriding ADR 0002, retrieval is ordered by the embedding model and by nothing else. A moment
  surfacing twice is the model's choice, made in a space where both of its vectors are honest.
  If a real notebook starts showing the same instant crowding out everything else, that is a
  measurement and a new ADR.

## Consequences

`SourceVideo` and `KindVideo` exist, `input_video` exists on the wire, clips are mp4 (h264 + aac)
because that is the container both a browser's `<video>` and the server's decoder open without a
plugin, and `videoClipSeconds`/`videoClipOverlap` join the settings with the audio pair.

- **ffprobe joins the tool folder** (ADR 0009). A video is cut by ffmpeg and measured by ffprobe —
  the length has to be known before the first window is cut, because windows are sought rather than
  streamed — and the model server looks for the same two programs in one directory
  (`video-ffmpeg-dir`). A bundle carrying one of the pair has a modality that fails in two places
  at once, so `tools.All()` names both and the release check `test -f`s both.
- **ADR 0008 is unchanged**, and the video is a small case for it: a missing program is still one
  finding per program with the program in the detail, so a silent file and an unequipped machine
  read differently without a new code.
- **A file with no soundtrack is normal**, not a fault. It gets its frames document and no
  soundtrack document, and no finding — nothing is missing from the machine, and the row would
  otherwise blame a person for their own silent clip.
- **The labels around a video clip are not finished.** Delivered as `[label, video, label, video]`
  in a tool result, the model reads the template's own `[0m5.00s]` timestamps as clip boundaries —
  it enumerated *"Clip 1 (00:07–00:23)"* and once cited an invented `[[Chunk 3]] (00:50s)` — and two
  clips in one call lost a sentence that one clip had quoted. Audio does not do this: a clip of
  sound is one continuous blob, a clip of video is N images with text woven between them. The path
  ships with labels above the part because that is what audio proved; fixing the video's is a
  measurement and a change of its own.
- **Nothing here is a measurement of real footage.** Captions on a moving test pattern are the
  easiest possible video. The frame rate, the window length and every cosine above have to be
  re-taken on real clips with real queries — motion, small objects and cut-heavy footage are where
  frame sampling is known to be worst, and none of that is in these numbers.
