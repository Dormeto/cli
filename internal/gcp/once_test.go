package gcp

import (
	"context"
	"sync"
	"testing"
)

type countingTokens struct {
	mu       sync.Mutex
	identity map[string]int
}

func (c *countingTokens) AccessToken(context.Context) (string, error) { return "a", nil }
func (c *countingTokens) Account(context.Context) (string, error)     { return "me", nil }
func (c *countingTokens) IdentityToken(_ context.Context, audience string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity[audience]++
	return "id-" + audience, nil
}

// An identity token is asked once per audience, whatever the goroutines that need it.
func TestOncePerAudienceAsksEachAudienceOnce(t *testing.T) {
	inner := &countingTokens{identity: map[string]int{}}
	tokens := OncePerAudience(inner)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			audience := []string{"x", "y"}[i%2]
			if got, err := tokens.IdentityToken(context.Background(), audience); err != nil || got != "id-"+audience {
				t.Errorf("%q, %v", got, err)
			}
		}()
	}
	wg.Wait()
	if inner.identity["x"] != 1 || inner.identity["y"] != 1 {
		t.Errorf("asked %v", inner.identity)
	}
}
