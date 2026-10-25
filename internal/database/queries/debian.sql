-- name: CreateDebianDistribution :one
INSERT INTO debian_distributions (repository_id, name, codename, description)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at, updated_at;

-- name: GetDebianDistributionsByRepo :many
SELECT id, repository_id, name,
       COALESCE(codename, '') AS codename,
       COALESCE(description, '') AS description,
       created_at, updated_at
FROM debian_distributions
WHERE repository_id = $1;

-- name: CreateDebianComponent :one
INSERT INTO debian_components (distribution_id, name, description)
VALUES ($1, $2, $3)
RETURNING id, created_at, updated_at;

-- name: GetDebianComponentsByDist :many
SELECT id, distribution_id, name,
       COALESCE(description, '') AS description,
       created_at, updated_at
FROM debian_components
WHERE distribution_id = $1;

-- name: CreateDebianArchitecture :one
INSERT INTO debian_architectures (distribution_id, name)
VALUES ($1, $2)
RETURNING id, created_at, updated_at;

-- name: GetDebianArchitecturesByDist :many
SELECT id, distribution_id, name, created_at, updated_at
FROM debian_architectures
WHERE distribution_id = $1;
