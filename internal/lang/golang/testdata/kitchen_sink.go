// Package svc does things.
package svc

import (
	"context"
	"fmt"
	repo "go-api/services/userrepo"
)

// MaxRetries is the retry cap.
const MaxRetries = 3

var globalTimeout = 30

// UserService handles users.
type UserService struct {
	Repo    *repo.Repository
	name    string
	BaseService
}

type Finder interface {
	Find(ctx context.Context, id int) (*User, error)
	fmt.Stringer
}

type ID = int64

// Find looks a user up.
func (s *UserService) Find(ctx context.Context, id int) (*User, error) {
	u, err := s.Repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("find: %w", err)
	}
	s.log(u)
	other := repo.New()
	_ = other
	x := &UserService{}
	y := User{}
	_, _ = x, y
	helper(id)
	return u, nil
}

func helper(id int) {}
