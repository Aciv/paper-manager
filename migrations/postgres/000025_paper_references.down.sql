-- This cutover is intentionally irreversible: reconstructing placeholder
-- documents and their citation edges from shared references would require
-- inventing document IDs and re-deriving source-free state. The down migration
-- documents the schema boundary and removes the new tables.
DROP TABLE IF EXISTS paper_citations;
DROP TABLE IF EXISTS paper_references;
