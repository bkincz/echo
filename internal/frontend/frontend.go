package frontend

import (
	"context"
	"encoding/json"
	"io"

	"github.com/echo-ssr/echo/internal/nodeproc"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type DevOptions struct {
	Host string
	Port int
}

type BuildClientOptions struct {
	OutDir string
}

type BuildServerOptions struct {
	SSREntry string
	OutDir   string
}

type RenderOptions struct {
	SSREntry     string
	URL          string
	RoutePattern string
	Status       int
	Shell        string
	LoaderData   json.RawMessage
}

type RenderResult struct {
	HTML string
}

type Engine interface {
	Name() string
	Validate(appDir string) error
	StartDev(ctx context.Context, appDir string, opts DevOptions) (*nodeproc.Process, error)
	BuildClient(ctx context.Context, appDir string, opts BuildClientOptions) error
	BuildServer(ctx context.Context, appDir string, opts BuildServerOptions) error
	Render(ctx context.Context, appDir string, opts RenderOptions) (RenderResult, error)
	Close() error
}

// StreamingEngine is optionally implemented by an Engine to support chunked
// SSR. When the server detects this interface it calls RenderStream instead of
// Render, writing HTML chunks directly to the ResponseWriter as React resolves
// Suspense boundaries. This enables better TTFB and meaningful use of Suspense.
type StreamingEngine interface {
	Engine
	RenderStream(ctx context.Context, appDir string, opts RenderOptions, w io.Writer) error
}
