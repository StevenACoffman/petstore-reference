-- name: CreatePet :one
INSERT INTO pets (
    name, species, birth_date, birth_date_estimated, status, photo_urls, tags, created_at, modified_at, created_by, modified_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8, $9
)
RETURNING *;

-- name: GetPet :one
SELECT * FROM pets
WHERE id = $1 LIMIT 1;

-- name: ListPets :many
SELECT * FROM pets
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('species')::text IS NULL OR species = sqlc.narg('species'))
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2;

-- name: CountPets :one
SELECT COUNT(*) FROM pets
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('species')::text IS NULL OR species = sqlc.narg('species'));

-- name: UpdatePet :one
UPDATE pets
SET
    name = $2,
    species = $3,
    birth_date = $4,
    birth_date_estimated = $5,
    status = $6,
    photo_urls = $7,
    tags = $8,
    modified_at = NOW(),
    modified_by = $9
WHERE id = $1
RETURNING *;

-- name: DeletePet :execrows
DELETE FROM pets
WHERE id = $1;

