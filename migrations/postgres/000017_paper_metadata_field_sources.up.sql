-- Preserve field-level manual edits when asynchronous extraction or catalog
-- tasks complete later. Historical rows only retain row-level provenance, so
-- old manual rows are conservatively protected for every editable field.
ALTER TABLE paper_metadata
    ADD COLUMN IF NOT EXISTS field_sources JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE paper_metadata
SET field_sources = CASE metadata_source
    WHEN 'manual' THEN jsonb_build_object(
        'title', 'manual', 'abstract', 'manual', 'authors', 'manual',
        'tags', 'manual', 'source_name', 'manual', 'source_url', 'manual',
        'venue', 'manual', 'published_at', 'manual', 'volume', 'manual',
        'issue', 'manual', 'pages', 'manual', 'language', 'manual'
    )
    WHEN 'extraction' THEN jsonb_build_object(
        'title', 'extraction', 'abstract', 'extraction', 'authors', 'extraction',
        'tags', 'extraction', 'source_name', 'extraction', 'source_url', 'extraction',
        'venue', 'extraction', 'published_at', 'extraction', 'volume', 'extraction',
        'issue', 'extraction', 'pages', 'extraction', 'language', 'extraction'
    )
    WHEN 'catalog' THEN jsonb_build_object(
        'title', 'catalog', 'abstract', 'catalog', 'authors', 'catalog',
        'tags', 'catalog', 'source_name', 'catalog', 'source_url', 'catalog',
        'venue', 'catalog', 'published_at', 'catalog', 'volume', 'catalog',
        'issue', 'catalog', 'pages', 'catalog', 'language', 'catalog'
    )
    ELSE '{}'::jsonb
END
WHERE field_sources = '{}'::jsonb;
