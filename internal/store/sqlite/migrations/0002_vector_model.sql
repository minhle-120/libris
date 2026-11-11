-- Which model produced each vector.
--
-- A vector is only comparable with vectors from the same model, so changing
-- embeddingModel invalidates every stored vector — the consequence ADR 0004
-- accepts. Without this column a model change would quietly mix two vector
-- spaces into one similarity search, which still returns numbers and still means
-- nothing.
ALTER TABLE segments ADD COLUMN model TEXT;
