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
-- Cursor paging: (created_at, id) is a total order because id is unique, so a row
-- inserted between fetches cannot shift the window the way OFFSET did.
--
-- COUNT(*) OVER() sits inside the CTE, over the filter-matching rows only. Putting
-- it outside the cursor predicate would count the remainder after the cursor and
-- silently report the wrong total. The window still scans the filtered set on every
-- page, exactly as the old separate CountPets did; that is the first thing to drop
-- if this table ever grows.
WITH filtered AS (
    SELECT *, COUNT(*) OVER () AS total_count
    FROM pets
    WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
      AND (sqlc.narg('species')::text IS NULL OR species = sqlc.narg('species'))
)
SELECT * FROM filtered
WHERE (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');


-- name: UpdatePet :one
-- A NULL parameter means "leave this column as it is", so one statement serves
-- both a partial and a full update without a read-modify-write cycle.
-- birth_date needs an explicit flag rather than COALESCE because it is nullable:
-- for it, NULL is a legitimate value to store, not an absence of instruction.
UPDATE pets
SET
    name = COALESCE(sqlc.narg('name'), name),
    species = COALESCE(sqlc.narg('species'), species),
    birth_date = CASE WHEN sqlc.arg('set_birth_date')::bool
                      THEN sqlc.narg('birth_date')::date
                      ELSE birth_date END,
    birth_date_estimated = COALESCE(sqlc.narg('birth_date_estimated'), birth_date_estimated),
    status = COALESCE(sqlc.narg('status'), status),
    photo_urls = COALESCE(sqlc.narg('photo_urls')::text[], photo_urls),
    tags = COALESCE(sqlc.narg('tags')::text[], tags),
    modified_at = NOW(),
    modified_by = sqlc.arg('modified_by')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeletePet :execrows
DELETE FROM pets
WHERE id = $1;

