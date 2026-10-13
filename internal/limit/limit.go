// Package limit runs a function over items a few at a time. It ports
// manager packages/estimate/src/limit.ts (mapLimit): the estimate's AWS reads,
// --parallel accounts or a region's workloads.
package limit

import (
	"context"
	"sync"
)

// Map calls fn over items, at most n at a time (fewer than one: one), and
// returns the results in the items' order, whatever finishes first.
//
// The first error fails Map, as Node's Promise.all rejects with the first
// rejection; no further item is started after it (Node's workers go on to the
// end, their results thrown away) and the context the calls in flight receive
// is cancelled. A cancelled ctx stops Map the same way and is its error.
func Map[T, R any](ctx context.Context, items []T, n int, fn func(context.Context, T) (R, error)) ([]R, error) {
	out := make([]R, len(items))
	n = max(1, min(n, len(items)))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		next    int
		first   error
		failed  bool
		workers sync.WaitGroup
		take    = func() (int, bool) {
			mu.Lock()
			defer mu.Unlock()
			if failed || next >= len(items) {
				return 0, false
			}
			if err := ctx.Err(); err != nil {
				first, failed = err, true
				return 0, false
			}
			next++
			return next - 1, true
		}
		fail = func(err error) {
			mu.Lock()
			defer mu.Unlock()
			if !failed {
				first, failed = err, true
				cancel()
			}
		}
	)
	for range n {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i, ok := take(); ok; i, ok = take() {
				r, err := fn(ctx, items[i])
				if err != nil {
					fail(err)
					return
				}
				out[i] = r
			}
		}()
	}
	workers.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}
