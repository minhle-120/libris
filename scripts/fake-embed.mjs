// A stand-in for llama.cpp, shared by `make e2e` and `make ui`: embeddings and the
// two model calls a question makes. It is one process on one port because that is how
// a single llama.cpp binary serves both.
//
// Deliberately the ugliest code in the repo: it mirrors the wire shape that
// server uses and nothing else, so the pipeline is exercised through HTTP rather
// than through a mock that agrees with whatever the caller wanted.
//
// Run directly to serve one port forever:  node scripts/fake-embed.mjs 8124
import { createServer } from 'node:http';

/**
 * The deltas of one streamed answer, in the order a reasoning model sends them: the thinking
 * first, in its own field, then the prose. Both cut into small pieces, because a client that
 * replaces its copy of either one on every delta instead of appending looks correct against a
 * server that sends one big frame.
 */
function streamPieces(think, text) {
  const thoughts = think ? (String(think).match(/.{1,14}/gs) ?? []) : [];
  const pieces = text.match(/.{1,9}/gs) ?? [];
  return [
    ...thoughts.map((reasoning_content) => ({ reasoning_content })),
    ...pieces.map((content) => ({ content }))
  ];
}

/**
 * The research model's two moves, derived from the request rather than scripted, so
 * one fake answers a question about any corpus: a research call with no tool reply in
 * front of it searches, and one that already has a search result finalises. That is
 * the whole loop, and it is the shape the UI has to survive.
 */
function researchReply(body) {
  const searched = body.messages.some((m) => m.role === 'tool');
  const tool = searched ? 'finalize_research_response' : 'query_knowledge_base';
  const args = searched
    ? { indices: [1] }
    : // Keywords taken from the question rather than invented, so the vector the fake
      // embeds is about something in the corpus at all.
        { keywords: keywordsFrom(body), query: questionFrom(body) };
  return {
    choices: [
      {
        message: {
          role: 'assistant',
          content: null,
          tool_calls: [
            {
              id: `call_${body.messages.length}`,
              type: 'function',
              function: { name: tool, arguments: JSON.stringify(args) }
            }
          ]
        },
        finish_reason: 'tool_calls'
      }
    ]
  };
}

function questionFrom(body) {
  const user = [...body.messages].reverse().find((m) => m.role === 'user');
  const text = typeof user?.content === 'string' ? user.content : (user?.content?.[0]?.text ?? '');
  return text.replace(/^### User Query:\s*/m, '').trim() || 'the question';
}

function keywordsFrom(body) {
  return (
    questionFrom(body)
      .toLowerCase()
      .replace(/[^a-z0-9 ]/g, ' ')
      .split(/\s+/)
      .filter((w) => w.length > 3)
      .slice(0, 4)
      .join(' ') || 'the source'
  );
}

export const DIM = 768; // EmbeddingGemma 2's length; the number the probe must discover

export class EmbedServer {
  constructor(dim = DIM) {
    this.dim = dim;
    this.requests = [];
    this.down = false;

    // The model half. `script` overrides the derived replies with an exact list of
    // completions, which is how a check makes a turn end a specific way: a refusal, an
    // answer with no citations, a model that ignores tool_choice and answers in prose.
    this.chatRequests = [];
    this.chatScript = null;
    this.chatHold = false;
    this.chatDown = false;
    this.answer = 'The sources support this. [[Chunk 1]]';

    this.server = createServer((req, res) => {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        // The harnesses need the server to disappear and come back: that is the
        // failure the user recovers from with the retry button.
        if (req.url === '/control') {
          let cmd;
          try {
            cmd = JSON.parse(body || '{}');
          } catch {
            res.writeHead(400);
            res.end('{"error":"not json"}');
            return;
          }
          // Reset first: a caller that says "start over, and say this" means the
          // script it named, and a reset processed afterwards would delete it.
          if ('reset' in cmd) {
            this.think = null;
            this.chatScript = null;
            this.chatHold = false;
            this.chatRequests = [];
            this.chatDown = false;
          }
          if ('down' in cmd) this.down = !!cmd.down;
          if ('chatDown' in cmd) this.chatDown = !!cmd.chatDown;
          if ('script' in cmd) this.chatScript = cmd.script;
          // A script that is held rather than consumed: a loop that re-prompts a
          // stubborn model asks more than once, and a one-item script would otherwise
          // fall back to the compliant default after the first refusal.
          if ('hold' in cmd) this.chatHold = !!cmd.hold;
          if ('answer' in cmd) this.answer = cmd.answer;
          // Thinking for the next streamed answer, in the field a reasoning model uses.
          // Off unless asked for, so a harness that is not looking at the thinking sees the
          // answer exactly as it did before.
          if ('think' in cmd) this.think = cmd.think || null;
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
            lastPrompt: typeof last?.input?.[0] === 'string' ? last.input[0].slice(0, 40) : null,
            // Enough to assert the two calls really are two calls: only the research
            // turn is given tools, and only the synthesis call is asked to stream.
            chatRequests: this.chatRequests.length,
            // Which of the two calls each request was: only the research turn is given
            // tools, so a count of them is a count of research calls.
            researchCalls: this.chatRequests.filter((r) => r.tools?.length).length,
            lastTools: this.chatRequests.filter((r) => r.tools?.length).at(-1)?.tools?.map((t) => t.function?.name) ?? null,
            lastStream: this.chatRequests.at(-1)?.stream ?? null,
            ...this.thinkingSeen()
          }));
          return;
        }

        if (req.url === '/v1/chat/completions') {
          this.chat(res, body);
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
        } catch {
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

  /**
   * What the last research call and the last answer call were told about thinking, for a
   * harness in another process that can only ask. The research call is expected to carry the
   * user's switch; the answer call is expected to say nothing and let the server decide.
   */
  thinkingSeen() {
    const last = (withTools) =>
      this.chatRequests.filter((r) => Boolean(r.tools?.length) === withTools).at(-1);
    return {
      lastResearchKwargs: last(true)?.chat_template_kwargs ?? null,
      lastAnswerKwargs: last(false)?.chat_template_kwargs ?? null
    };
  }

  /**
   * One chat completion. Research and synthesis are told apart by the request, which
   * is how a real server sees them: only the research call is given tools.
   */
  chat(res, raw) {
    let body;
    try {
      body = JSON.parse(raw);
    } catch {
      res.writeHead(400);
      res.end('{"error":"not json"}');
      return;
    }
    this.chatRequests.push(body);

    if (this.chatDown) {
      res.writeHead(503, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'all inference slots are busy' } }));
      return;
    }

    const scripted = this.chatScript?.length
      ? (this.chatHold ? this.chatScript[0] : this.chatScript.shift())
      : null;
    const completion =
      scripted ??
      (body.tools?.length
        ? researchReply(body)
        : { choices: [{ message: { role: 'assistant', content: this.answer }, finish_reason: 'stop' }] });

    if (!body.stream) {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ object: 'chat.completion', model: body.model, ...completion }));
      return;
    }

    // Synthesis streams. The answer arrives in small pieces so the client has to
    // reassemble frames, including one that splits a `[[Chunk 1]]` label in half.
    res.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-store',
      'x-accel-buffering': 'no'
    });
    const text = completion.choices?.[0]?.message?.content ?? '';
    const deltas = streamPieces(this.think, text);
    let i = 0;
    const send = () => {
      const delta = i < deltas.length ? deltas[i] : {};
      const finish = i >= deltas.length ? 'stop' : null;
      res.write(
        `data: ${JSON.stringify({
          object: 'chat.completion.chunk',
          model: body.model,
          choices: [{ index: 0, delta, finish_reason: finish }]
        })}\n\n`
      );
      i++;
      if (i <= deltas.length) return setTimeout(send, 12);
      res.write('data: [DONE]\n\n');
      res.end();
    };
    send();
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
