-- What the model thought before it answered, kept with the answer.

-- The reasoning a reasoning model streams ahead of its prose. It arrives as deltas
-- and used to be shown while it worked and thrown away when the first sentence
-- arrived — which meant a reader who wanted to know how an answer had been reached
-- had to be watching at the right second, and could never ask again. It is stored so
-- the answer can be opened and inspected later, beside the citations that already
-- say where its sentences came from.
--
-- What it is *not* is knowledge. Nothing here reaches a later turn's prompt: the
-- history a model is given is built from `content` alone, because thinking about how
-- to find something is not a fact this library established, and pasted back in it
-- lets a later answer repeat a conclusion it never found evidence for. Same argument
-- as `trace_json` below, and the same reason `RecentMessages` does not select it.
ALTER TABLE messages ADD COLUMN reasoning TEXT NOT NULL DEFAULT '';

-- How long the model thought before its first sentence, in milliseconds, measured
-- from the deltas where the thinking arrived rather than from the request — so the
-- wait for a search round trip is not billed to the thinking. Stored because the
-- answer's label says "Thought for 6s", and a row that lost the number after a
-- reload would be a row that quietly stopped answering the question it poses.
ALTER TABLE messages ADD COLUMN reasoning_ms INTEGER NOT NULL DEFAULT 0;
