// Package parser implements the w_popularity facebook adapter.
//
// Status: STUB. Returns shared.ErrNotImplemented.
//
// Strategy:
//   primary:  Graph API with page access token
//   fallback: camoufox
package parser

import (
	"context"
	"time"

	shared "github.com/suenot/w-popularity-shared"
)

// Config controls runtime behaviour. Add platform-specific fields here.
type Config struct {
	// Token, cookie, or API key — fill in per implementation.
	Credential string
	// HTTPTimeout caps every outbound call.
	HTTPTimeout time.Duration
	// CamoufoxURL is set when falling back to browser-based scraping.
	CamoufoxURL string
}

// New constructs a stubbed parser. Real impl is pending.
func New(cfg Config) *FacebookParser { return &FacebookParser{cfg: cfg} }

type FacebookParser struct{ cfg Config }

func (p *FacebookParser) Platform() shared.Platform { return shared.PlatformFacebook }

func (p *FacebookParser) FetchChannel(ctx context.Context, handle string) (shared.ChannelSnapshot, error) {
	return shared.ChannelSnapshot{}, shared.ErrNotImplemented
}

func (p *FacebookParser) FetchRecentPosts(ctx context.Context, handle string, since time.Time) ([]shared.PostSnapshot, error) {
	return nil, shared.ErrNotImplemented
}
