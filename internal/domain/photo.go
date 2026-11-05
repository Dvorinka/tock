package domain

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
)

// photoReaper records profile photo paths of user rows deleted in one
// transaction. Files are removed only after that transaction commits.
type photoReaper struct {
	paths []string
}

type reaperKey struct{}

func withReaper(ctx context.Context) (context.Context, *photoReaper) {
	r := &photoReaper{}
	return context.WithValue(ctx, reaperKey{}, r), r
}

func reaperFrom(ctx context.Context) *photoReaper {
	r, _ := ctx.Value(reaperKey{}).(*photoReaper)
	return r
}

func (s *Service) reap(r *photoReaper) {
	if r == nil || s.storageDir == "" {
		return
	}
	for _, p := range r.paths {
		unlinkProfilePhoto(s.storageDir, p)
	}
}

// deleteUserRow removes the user and remembers the photo path for after commit.
func (s *Service) deleteUserRow(ctx context.Context, q *db.Queries, id pgtype.UUID) error {
	if r := reaperFrom(ctx); r != nil {
		if path, err := q.ProfilePhotoPath(ctx, id); err == nil && path != nil && *path != "" {
			r.paths = append(r.paths, *path)
		}
	}
	return q.DeleteUserByID(ctx, id)
}

// unlinkProfilePhoto removes one uploaded avatar. The stored path must stay
// under profile-photos/, and only the basename is joined, so a bad path
// cannot leave that directory.
func unlinkProfilePhoto(storageDir, path string) {
	if storageDir == "" || !strings.HasPrefix(path, "profile-photos/") {
		return
	}
	base := filepath.Base(path)
	if base == "." || base == ".." || base == "" {
		return
	}
	_ = os.Remove(filepath.Join(storageDir, "profile-photos", base))
}
