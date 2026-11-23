This is the body of every GitHub Release created by a `v*` tag. Update it before
cutting the tag; the workflow pastes it verbatim.

---

**Libris** is a local-first notebook: add sources — PDFs, images, audio — ask
questions across them, and get answers whose citations open the original at the
right page or timestamp. The UI, the database and the models all run on your
machine; nothing leaves `127.0.0.1`.

### The files

| | |
| --- | --- |
| `libris-vX.Y.Z-linux-amd64` | Linux x86-64. `chmod +x` and run it. |
| `libris-vX.Y.Z-windows-amd64.exe` | Windows x86-64. Double-click, or run it in a terminal to see the log. |
| `SHA256SUMS.txt` | Checksums for both. |

Both are single static binaries with the UI inside them — there is nothing to
install and nothing to unpack. Verify them with `sha256sum -c SHA256SUMS.txt`, or
in PowerShell `Get-FileHash .\libris-vX.Y.Z-windows-amd64.exe`.

Run it and it listens on `127.0.0.1:8080`, opens your browser, and keeps its data
in a `libris` folder inside your Documents directory:

```
libris                                  # or: .\libris-vX.Y.Z-windows-amd64.exe
libris -addr 127.0.0.1:9000 -data /mnt/nvme/libris -open=false
libris -version                          # the revision this file was built from
```

### What is not inside the file

Libris is the app; the models are servers you run beside it. It talks to three
OpenAI-compatible HTTP endpoints — one chat model, one embedding model, one
reranker — and their URLs and model names are set in the Settings page, not on the
command line. **llama.cpp's `llama-server` is the intended host for all three.**
Sampling and context size are `llama-server`'s decision, made on its command line;
Libris will never override them.

Ingest also uses external tools when they are present: `pdftotext` and `pdftoppm`
(from poppler) for PDFs, `ffmpeg`/`ffprobe` for audio. Without them the app still
runs, and those formats report themselves as unsupported rather than failing.

### Windows

SmartScreen will say _"Windows protected your PC"_ because these binaries are not
code-signed — signing needs a certificate bought by a legal entity, and this is a
local tool. The file is the one linked here; check its checksum if that is not
reassuring enough. Run it in a terminal if it starts and vanishes: the console is
where a startup failure explains itself.

### Status

Settings, ingest, embedding, retrieval, the two-call answer path with citations, and
the web UI are all here. Not built yet: audio transcription (audio is sliced and
searchable, but has no text to quote), a vector index (retrieval is a brute-force
scan), ingest progress events, and the MCP server.
