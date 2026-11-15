package gcptest

import (
	"context"
	"sync"
	"testing"
)

// The fake's identity tokens may be asked from several goroutines at once.
func TestTokensCollectAudiencesFromSeveralGoroutines(t *testing.T) {
	var aud []string
	tokens := Tokens{Audiences: &aud}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tokens.IdentityToken(context.Background(), "a")
		}()
	}
	wg.Wait()
	if len(aud) != 8 {
		t.Errorf("%d audiences", len(aud))
	}
}
