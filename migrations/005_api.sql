-- API-layer constraints.

-- Repository names are routed globally (URLs carry only the bare name), so they
-- must be unique across organizations, not just per (org_id, name).
CREATE UNIQUE INDEX IF NOT EXISTS repositories_name_global_unique ON repositories (name);
