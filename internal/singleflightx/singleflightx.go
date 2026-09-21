// Package singleflightx adds context-aware waiting to golang.org/x/sync/singleflight.
//
// The upstream Group.Do blocks until the shared call finishes, and it starts that
// call on the first caller's context. Both properties are wrong for work that is
// shared by unrelated requests: a caller whose request ends must stop waiting
// without aborting the flight, and the flight must not die with the request that
// happened to arrive first.
package singleflightx

import (
	"context"
	"errors"

	"golang.org/x/sync/singleflight"
)

// Group coalesces concurrent calls that share a key into a single execution.
// The result type is fixed on the group because Go does not allow type
// parameters on methods.
type Group[T any] struct {
	sfg singleflight.Group
}

// Do returns the result of fn for key: the first caller runs fn while the others
// wait for, and share, its result.
//
// A canceled ctx stops the wait only. The flight keeps running for the callers
// still interested in it, so a later retry rejoins the same flight instead of
// starting a competing one. Whether the work itself should survive the request
// that started it is the caller's decision: run fn on a context that does not
// cancel with that request when it should.
func (g *Group[T]) Do(ctx context.Context, key string, fn func() (T, error)) (T, error) {
	var zero T

	res := g.sfg.DoChan(key, func() (any, error) {
		v, err := fn()
		if err != nil {
			return nil, err
		}
		return v, nil
	})

	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case r := <-res:
		if r.Err != nil {
			return zero, r.Err
		}
		v, ok := r.Val.(T)
		if !ok {
			return zero, errors.New("singleflightx: flight produced an unexpected value type")
		}
		return v, nil
	}
}
