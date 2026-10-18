-- Intentionally irreversible: once shared reference rows are split into
-- per-citing-paper citations, reconstructing which rows were previously shared
-- would require re-deriving reference identity from historical raw text. The
-- down migration documents the schema boundary.
DROP TABLE IF EXISTS paper_citations;
