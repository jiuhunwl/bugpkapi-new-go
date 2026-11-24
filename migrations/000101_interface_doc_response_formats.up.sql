-- Multi-format public docs response. An optional JSON array of
-- {"type","example"} entries replaces the single doc_response_type/
-- doc_response_example pair when present; NULL keeps the legacy pair
-- authoritative so existing interfaces need no backfill.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `doc_response_formats` json DEFAULT NULL
    COMMENT 'Public doc response formats [{type,example}]; NULL falls back to doc_response_type/doc_response_example'
    AFTER `doc_response_example`;
