package gcp

import (
	"context"
	"sync"
)

// OncePerAudience is tokens with each identity token asked once per audience and kept for the run: a command that calls a
// router's controller many times (`schedule list`) asks gcloud once, not once per call. Access tokens and the account are
// tokens' own.
func OncePerAudience(tokens TokenSource) TokenSource {
	return &oncePerAudience{TokenSource: tokens, kept: map[string]*identity{}}
}

type oncePerAudience struct {
	TokenSource
	mu   sync.Mutex
	kept map[string]*identity
}

// identity is one audience's token, asked once.
type identity struct {
	once  sync.Once
	token string
	err   error
}

func (o *oncePerAudience) IdentityToken(ctx context.Context, audience string) (string, error) {
	o.mu.Lock()
	id := o.kept[audience]
	if id == nil {
		id = &identity{}
		o.kept[audience] = id
	}
	o.mu.Unlock()
	id.once.Do(func() { id.token, id.err = o.TokenSource.IdentityToken(ctx, audience) })
	return id.token, id.err
}
