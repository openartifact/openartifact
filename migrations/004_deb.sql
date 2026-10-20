-- Debian: persist the full control stanza so generated Packages entries carry
-- Depends, Pre-Depends, Maintainer, Section, Priority, etc.
ALTER TABLE deb_packages ADD COLUMN IF NOT EXISTS control_fields jsonb NOT NULL DEFAULT '{}'::jsonb;
