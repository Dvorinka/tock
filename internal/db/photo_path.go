package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// ProfilePhotoPath reads users.profile_photo_path on this querier, including
// an open transaction. GetUserByID does not return the column.
func (q *Queries) ProfilePhotoPath(ctx context.Context, id pgtype.UUID) (*string, error) {
	var path *string
	err := q.db.QueryRow(ctx, `SELECT profile_photo_path FROM users WHERE id = $1`, id).Scan(&path)
	return path, err
}
