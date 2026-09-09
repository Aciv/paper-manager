-- Add bounded per-document workflow control without removing the legacy
-- lifecycle status or processing-run tables during the worker transition.
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS markdown_status TEXT NOT NULL DEFAULT 'not_started',
    ADD COLUMN IF NOT EXISTS markdown_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS markdown_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN IF NOT EXISTS metadata_extraction_status TEXT NOT NULL DEFAULT 'not_started',
    ADD COLUMN IF NOT EXISTS metadata_extraction_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS metadata_extraction_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN IF NOT EXISTS source_lookup_status TEXT NOT NULL DEFAULT 'not_started',
    ADD COLUMN IF NOT EXISTS source_lookup_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS source_lookup_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN IF NOT EXISTS reference_lookup_status TEXT NOT NULL DEFAULT 'not_started',
    ADD COLUMN IF NOT EXISTS reference_lookup_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reference_lookup_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

ALTER TABLE documents
    ADD CONSTRAINT chk_documents_markdown_status
        CHECK (markdown_status IN ('not_started', 'queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    ADD CONSTRAINT chk_documents_metadata_extraction_status
        CHECK (metadata_extraction_status IN ('not_started', 'queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    ADD CONSTRAINT chk_documents_source_lookup_status
        CHECK (source_lookup_status IN ('not_started', 'queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    ADD CONSTRAINT chk_documents_reference_lookup_status
        CHECK (reference_lookup_status IN ('not_started', 'queued', 'running', 'succeeded', 'failed', 'cancelled', 'skipped')),
    ADD CONSTRAINT chk_documents_markdown_generation CHECK (markdown_generation >= 0),
    ADD CONSTRAINT chk_documents_metadata_extraction_generation CHECK (metadata_extraction_generation >= 0),
    ADD CONSTRAINT chk_documents_source_lookup_generation CHECK (source_lookup_generation >= 0),
    ADD CONSTRAINT chk_documents_reference_lookup_generation CHECK (reference_lookup_generation >= 0);

-- A stored Markdown manifest proves Markdown conversion completed. A metadata
-- row alone does not: migration 4 created empty rows for every old document.
UPDATE documents AS d
SET markdown_status = 'succeeded',
    markdown_generation = GREATEST(d.markdown_generation, 1),
    markdown_updated_at = dm.updated_at
FROM document_markdowns AS dm
WHERE dm.document_id = d.id;

-- Only an extraction-owned snapshot proves the extraction stage completed;
-- catalog/manual/unknown snapshots deliberately remain not_started.
UPDATE documents AS d
SET metadata_extraction_status = 'succeeded',
    metadata_extraction_generation = GREATEST(d.metadata_extraction_generation, 1),
    metadata_extraction_updated_at = pm.updated_at
FROM paper_metadata AS pm
WHERE pm.document_id = d.id
  AND pm.metadata_source = 'extraction';

-- Preserve a queued reference run even when its root span has not started yet.
WITH latest_reference_run AS (
    SELECT DISTINCT ON (owner_document_id)
        owner_document_id,
        attempt,
        CASE state
            WHEN 'queued' THEN 'queued'
            WHEN 'running' THEN 'running'
            WHEN 'cancel_requested' THEN 'cancelled'
        END AS stage_status,
        updated_at
    FROM processing_runs
    WHERE workflow_kind = 'catalog_reference'
      AND state IN ('queued', 'running', 'cancel_requested')
    ORDER BY owner_document_id, updated_at DESC, id DESC
)
UPDATE documents AS d
SET reference_lookup_status = r.stage_status,
    reference_lookup_generation = GREATEST(d.reference_lookup_generation, r.attempt),
    reference_lookup_updated_at = r.updated_at
FROM latest_reference_run AS r
WHERE r.owner_document_id = d.id
  AND d.reference_lookup_status = 'not_started';

-- Active stage spans are precise enough to preserve in-flight work. Terminal
-- observation rows are not used to infer success when no canonical artifact
-- exists, keeping the backfill conservative.
WITH latest_stage_span AS (
    SELECT DISTINCT ON (document_id, kind)
        document_id, kind, status, attempt, updated_at
    FROM document_processing_spans
    WHERE status IN ('queued', 'running')
      AND (
          (kind = 'recognition' AND scope = 'document') OR
          (kind = 'metadata_extraction' AND scope = 'document') OR
          (kind = 'catalog_source' AND scope = 'source') OR
          (kind = 'catalog_reference' AND scope = 'reference_run')
      )
    ORDER BY document_id, kind, updated_at DESC, id DESC
)
UPDATE documents AS d
SET markdown_status = s.status,
    markdown_generation = GREATEST(d.markdown_generation, s.attempt),
    markdown_updated_at = s.updated_at
FROM latest_stage_span AS s
WHERE s.document_id = d.id
  AND s.kind = 'recognition'
  AND d.markdown_status = 'not_started';

WITH latest_stage_span AS (
    SELECT DISTINCT ON (document_id)
        document_id, status, attempt, updated_at
    FROM document_processing_spans
    WHERE kind = 'metadata_extraction' AND scope = 'document'
      AND status IN ('queued', 'running')
    ORDER BY document_id, updated_at DESC, id DESC
)
UPDATE documents AS d
SET metadata_extraction_status = s.status,
    metadata_extraction_generation = GREATEST(d.metadata_extraction_generation, s.attempt),
    metadata_extraction_updated_at = s.updated_at
FROM latest_stage_span AS s
WHERE s.document_id = d.id
  AND d.metadata_extraction_status = 'not_started';

WITH latest_stage_span AS (
    SELECT DISTINCT ON (document_id)
        document_id, status, attempt, updated_at
    FROM document_processing_spans
    WHERE kind = 'catalog_source' AND scope = 'source'
      AND status IN ('queued', 'running')
    ORDER BY document_id, updated_at DESC, id DESC
)
UPDATE documents AS d
SET source_lookup_status = s.status,
    source_lookup_generation = GREATEST(d.source_lookup_generation, s.attempt),
    source_lookup_updated_at = s.updated_at
FROM latest_stage_span AS s
WHERE s.document_id = d.id
  AND d.source_lookup_status = 'not_started';

WITH latest_stage_span AS (
    SELECT DISTINCT ON (document_id)
        document_id, status, attempt, updated_at
    FROM document_processing_spans
    WHERE kind = 'catalog_reference' AND scope = 'reference_run'
      AND status IN ('queued', 'running')
    ORDER BY document_id, updated_at DESC, id DESC
)
UPDATE documents AS d
SET reference_lookup_status = s.status,
    reference_lookup_generation = GREATEST(d.reference_lookup_generation, s.attempt),
    reference_lookup_updated_at = s.updated_at
FROM latest_stage_span AS s
WHERE s.document_id = d.id
  AND d.reference_lookup_status = 'not_started';

CREATE INDEX IF NOT EXISTS idx_documents_markdown_stage_active
    ON documents (markdown_updated_at, id)
    WHERE markdown_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_documents_metadata_extraction_stage_active
    ON documents (metadata_extraction_updated_at, id)
    WHERE metadata_extraction_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_documents_source_lookup_stage_active
    ON documents (source_lookup_updated_at, id)
    WHERE source_lookup_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_documents_reference_lookup_stage_active
    ON documents (reference_lookup_updated_at, id)
    WHERE reference_lookup_status IN ('queued', 'running');
