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
    ? { indices: citedChunks(body) }
    : // The question itself, taken from the turn rather than invented, so the vector
      // the real search embeds is about something in the corpus at all.
        { query: questionFrom(body) };
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

/**
 * Every chunk label the search actually put in front of the model, cited back. "What I
 * used" is the whole point of the field, and a fake that always cites chunk 1 makes the
 * evidence set the answer is written from a constant — which hides exactly the thing a
 * media check needs to see: the page the researcher found is the page the writer is given.
 */
function citedChunks(body) {
  const seen = new Set();
  for (const m of body.messages ?? []) {
    const text = typeof m.content === 'string' ? m.content : (m.content ?? []).map((p) => p?.text ?? '').join('');
    for (const label of text.matchAll(/\[\[Chunk (\d+)\]\]/g)) seen.add(Number(label[1]));
  }
  return [...seen].sort((a, b) => a - b);
}

/**
 * One of the two things GET /props can mean. With ?model= the router proxies to the
 * instance, and the instance's "modalities" is written from its loaded projector. Without
 * it the router speaks about itself: no model, no modalities, and an alias upstream
 * comments as "a dummy response to make sure the UI doesn't break" — which is exactly
 * what a client reads as "no vision" if it asks the wrong question and decodes absence
 * into a bool. Both answers are served here so that mistake cannot pass quietly.
 */
function propsPayload(query, vision) {
  const named = query.get('model');
  if (named) {
    return {
      model_alias: named,
      modalities: { vision, video: false, audio: false },
      media_marker: '<__media__>',
      total_slots: 1,
      is_sleeping: false
    };
  }
  return {
    role: 'router',
    max_instances: 3,
    model_alias: 'llama-server',
    model_path: 'none',
    default_generation_settings: { params: {}, n_ctx: 0 }
  };
}

function questionFrom(body) {
  const user = [...body.messages].reverse().find((m) => m.role === 'user');
  const text = typeof user?.content === 'string' ? user.content : (user?.content?.[0]?.text ?? '');
  return text.replace(/^### User Query:\s*/m, '').trim() || 'the question';
}

export const DIM = 768; // EmbeddingGemma 2's length; the number the probe must discover

export class EmbedServer {
  constructor(dim = DIM) {
    this.dim = dim;
    this.requests = [];
    this.down = false;
    // Held: an embedding request arrives and nothing answers it, which is the only way
    // a browser harness can catch a run in the middle of a pass and stop it. Releasing
    // answers whatever is queued, in the order it came.
    this.embedHold = false;
    this.held = [];

    // The model half. `script` overrides the derived replies with an exact list of
    // completions, which is how a check makes a turn end a specific way: a refusal, an
    // answer with no citations, a model that ignores tool_choice and answers in prose.
    this.chatRequests = [];
    this.chatScript = null;
    this.chatHold = false;
    this.chatDown = false;
    this.answer = 'The sources support this. [[Chunk 1]]';

    // GET /props, answered twice over: the router talks about itself, and an instance
    // talks about its projector. Both exist because only the pair tells a client
    // whether it asked the right question — see the handler.
    this.vision = true;
    this.propsAsked = { named: 0, unnamed: 0 };

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
            this.embedHold = false;
            this.held = [];
            this.think = null;
            this.chatScript = null;
            this.chatHold = false;
            this.chatRequests = [];
            this.chatDown = false;
          }
          if ('down' in cmd) this.down = !!cmd.down;
          if ('embedHold' in cmd) {
            this.embedHold = !!cmd.embedHold;
            if (!this.embedHold) {
              const queued = this.held.splice(0);
              for (const answer of queued) answer();
            }
          }
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
          // What the serving model can be shown. Off makes the fake a text-only model,
          // which is how a check sees the refusal rather than the answer.
          if ('vision' in cmd) this.vision = !!cmd.vision;
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
            // Which of the two /props answers was asked for. An unqualified ask is worth
            // nothing about a model, so a client that makes only those has a bug even
            // when every other check passes.
            propsNamed: this.propsAsked.named,
            propsUnnamed: this.propsAsked.unnamed,
            ...this.mediaSeen(),
            ...this.mediaInputs(),
            ...this.thinkingSeen()
          }));
          return;
        }

        // A router answers GET /props about itself unless the query names a model
        // (server-models.cpp, get_router_props): role "router", an alias upstream
        // comments as "a dummy response to make sure the UI doesn't break", and no
        // modalities key at all. Naming the model proxies to the instance, which answers
        // from its own loaded projector. Answering only one of the two would let a
        // client that asks the wrong question read the dummy as a verdict and pass every
        // check in this file.
        if ((req.url ?? '').startsWith('/props')) {
          const query = new URL(req.url, 'http://libris.invalid').searchParams;
          this.propsAsked[query.get('model') ? 'named' : 'unnamed']++;
          res.writeHead(200, { 'content-type': 'application/json' });
          res.end(JSON.stringify(propsPayload(query, this.vision)));
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
        const answer = () => {
          // A stopped run hangs up on its own request; answering a dead socket is the
          // harmless half of holding, and the harness counts the arrival anyway.
          if (res.writableEnded || res.destroyed) return;
          res.writeHead(200, { 'content-type': 'application/json' });
          res.end(JSON.stringify({ object: 'list', model: parsed.model, data }));
        };
        if (this.embedHold) {
          this.held.push(answer);
          return;
        }
        answer();
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
   * How many media parts each of the last two calls was sent, by message role.
   *
   * This is the shape the evidence path promises: a page raster reaches the model that
   * decides what to cite as the file, not as a line of prose about a file, and reaches
   * the model that writes the answer the same way. Reading the roles apart is the point
   * — a check that only looked at the answer call would pass while the researcher was
   * still being told an image exists somewhere.
   */
  mediaSeen() {
    const scan = (request) => {
      if (!request) return null;
      const byRole = {};
      for (const m of request.messages ?? []) {
        if (!Array.isArray(m.content)) continue;
        const n = m.content.filter((p) => p?.type === 'image_url').length;
        if (n) byRole[m.role] = (byRole[m.role] ?? 0) + n;
      }
      return byRole;
    };
    const last = (withTools) =>
      this.chatRequests.filter((r) => Boolean(r.tools?.length) === withTools).at(-1);
    return { lastResearchMedia: scan(last(true)), lastAnswerMedia: scan(last(false)) };
  }

  /**
   * How many embedding inputs arrived as a modality rather than as words, by part type —
   * `imageURL` for a page raster, `inputAudio` for a clip. It is the embedding half of the
   * same fact mediaSeen() asserts about a chat call: a recording that reaches the embedder
   * as a file path, or as a transcript somebody wrote, still gets a vector of the right
   * length, and every check that counts vectors passes while the thing being searched for is
   * words nobody spoke.
   */
  mediaInputs() {
    const byType = {};
    for (const request of this.requests) {
      for (const entry of request.input ?? []) {
        if (typeof entry === 'string') continue;
        for (const part of entry.content ?? []) {
          if (part?.type && part.type !== 'text') {
            byType[part.type] = (byType[part.type] ?? 0) + 1;
          }
        }
      }
    }
    return { inputsByType: byType };
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
