-- What a finished turn needs recorded, so an answer survives the process that
-- wrote it.
--
-- `messages` and `citations` have existed since 0001. The three columns below are
-- what the answer path discovered it needed once the research loop existed:

-- How an assistant turn ended ('answered', 'no-info', 'ungrounded', 'canceled',
-- 'interrupted'). The UI draws these differently — an unchecked answer and a
-- grounded one must not look identical — and the list of a chat's messages is
-- where a reader finds that out, so it is stored rather than inferred.
ALTER TABLE messages ADD COLUMN status TEXT NOT NULL DEFAULT '';

-- The research turn's own transcript: every assistant reply and tool result, no
-- system prompt and no seeded question. Written when the answer finishes, so a
-- turn can be replayed against the same evidence after a prompt change.
--
-- It is never read back into a model. The synthesiser's prompt is system + chat
-- history + evidence + question, and reasoning about what to search next is not
-- knowledge — pasted in, it lets the answer model repeat a conclusion it never
-- found evidence for. Nothing in the API serves it either.
--
-- Like a vector, it is dead weight in a query that will not use it, so the
-- message queries do not select it.
ALTER TABLE messages ADD COLUMN trace_json TEXT NOT NULL DEFAULT '';

-- 1 when this turn must not reach a later turn's history. A question the library
-- could not answer is excluded whole — the user's message with its answer, since
-- leaving the query in place keeps the dead end while deleting only its outcome,
-- and "I couldn't find X" read back as a past answer is evidence that X is absent
-- from the world rather than from this corpus.
ALTER TABLE messages ADD COLUMN exclude_from_history INTEGER NOT NULL DEFAULT 0;

-- The `citations` column is the frozen form of [[Chunk N]]: chunk number, segment
-- and doc ids, source id and display name, kind, ordinal, quote, asset and
-- provenance. Self-contained on purpose, because a re-ingest deletes a source's
-- segment rows and writes new ones, and an answer written before that rebuild
-- still has to say what it was citing and open it.
