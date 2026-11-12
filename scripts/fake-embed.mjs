// A stand-in for llama.cpp's embedding server, shared by `make e2e` and `make ui`.
//
// Deliberately the ugliest code in the repo: it mirrors the wire shape that
// server uses and nothing else, so the pipeline is exercised through HTTP rather
// than through a mock that agrees with whatever the caller wanted.
//
// Run directly to serve one port forever:  node scripts/fake-embed.mjs 8124
import { createServer } from 'node:http';

export const DIM = 768; // EmbeddingGemma 2's length; the number the probe must discover

export class EmbedServer {
  constructor(dim = DIM) {
    this.dim = dim;
    this.requests = [];
    this.down = false;

    this.server = createServer((req, res) => {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        // The harnesses need the server to disappear and come back: that is the
        // failure the user recovers from with the retry button.
        if (req.url === '/control') {
          this.down = !!JSON.parse(body || '{}').down;
          res.writeHead(204);
          res.end();
          return;
        }

        // A harness running in another process cannot read this.requests, so it
        // asks: how many batches arrived, and what the most recent one looked like.
        if (req.url === '/stats') {
          const last = this.requests.at(-1);
          res.writeHead(200, { 'content-type': 'application/json' });
          res.end(JSON.stringify({
            requests: this.requests.length,
            lastModel: last?.model ?? null,
            lastInputs: last?.input?.length ?? 0,
            lastPrompt: typeof last?.input?.[0] === 'string' ? last.input[0].slice(0, 40) : null
          }));
          return;
        }

        if (this.down) {
          res.writeHead(503, { 'content-type': 'application/json' });
          res.end(JSON.stringify({ error: { message: 'all inference slots are busy' } }));
          return;
        }

        let parsed;
        try {
          parsed = JSON.parse(body);
        } catch (e) {
          res.writeHead(400);
          res.end('{"error":"not json"}');
          return;
        }

        // Record what the server saw, in enough detail to assert the pipeline
        // sent one entry per segment and named the model.
        this.requests.push({ model: parsed.model, input: parsed.input });

        const data = parsed.input.map((entry, i) => ({
          index: i,
          object: 'embedding',
          // A vector whose first element encodes the row, so a mapping that
          // scrambles order shows up as a mismatch against the segment's text.
          embedding: this.vector(entry, i)
        }));
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ object: 'list', model: parsed.model, data }));
      });
    });
  }

  vector(entry, row) {
    const out = new Array(this.dim).fill(0);
    out[0] = row + 1;
    const first = typeof entry === 'string' ? entry : (entry.content?.[0]?.text ?? '');
    out[1] = first.length;
    return out;
  }

  // Port 0 asks the OS for a free one, which is what the in-process harnesses
  // want; the standalone mode passes an explicit port the app's settings can name.
  async listen(port = 0) {
    await new Promise((r) => this.server.listen(port, '127.0.0.1', r));
    return `http://127.0.0.1:${this.server.address().port}`;
  }

  close() {
    this.server.close();
  }
}


// `node scripts/fake-embed.mjs <port>` serves forever, for the harnesses that
// start it as a separate process.
if (process.argv[1] && import.meta.url.endsWith(process.argv[1].replace(/^\.\//, ''))) {
  const port = Number(process.argv[2] ?? 8124);
  const srv = new EmbedServer();
  await srv.listen(port);
  console.log(`fake embeddings server on http://127.0.0.1:${port} (dim ${DIM})`);
  process.on('SIGTERM', () => process.exit(0));
}
