// Package observability integrates the incident agent with Coze Loop
// (cozeloop-go) tracing. Integration is optional: when COZELOOP_API_TOKEN
// and COZELOOP_WORKSPACE_ID are unset, New returns nil and the agent runs
// without network telemetry.
package observability

import (
	"context"
	"os"

	cozeloop "github.com/coze-dev/cozeloop-go"
)

// Env variables consumed by the cozeloop client (see cozeloop-go/const.go):
//
//	COZELOOP_API_TOKEN    - PAT/JWT token (required to enable tracing)
//	COZELOOP_WORKSPACE_ID - workspace id (required to enable tracing)
//	COZELOOP_API_BASE_URL - optional endpoint override
const (
	EnvAPIToken    = "COZELOOP_API_TOKEN"
	EnvWorkspaceID = "COZELOOP_WORKSPACE_ID"
)

// Tracer is the minimal tracing surface consumed by the agent package.
// The concrete *Coze implementation below satisfies it structurally so
// neither package has to import the other.
type Tracer interface {
	// StartSpan opens a child span linked to the span carried by ctx.
	StartSpan(ctx context.Context, name, spanType string, tags map[string]any, input any) (context.Context, Span)
	// Close flushes buffered spans and releases the client.
	Close(ctx context.Context)
}

// Span is one open trace span.
type Span interface {
	SetTags(ctx context.Context, tags map[string]any)
	SetInput(ctx context.Context, input any)
	SetOutput(ctx context.Context, output any)
	SetError(ctx context.Context, err error)
	SetStatusCode(ctx context.Context, code int)
	SetModelName(ctx context.Context, name string)
	SetInputTokens(ctx context.Context, n int)
	SetOutputTokens(ctx context.Context, n int)
	Finish(ctx context.Context)
}

// New builds a Coze Loop tracer from the environment. It returns (nil, nil)
// when tracing is not configured, so callers can use the tracer without
// any feature/credential gating. A configuration error (bad token etc.)
// also disables tracing rather than aborting the diagnosis run.
func New() Tracer {
	if os.Getenv(EnvAPIToken) == "" || os.Getenv(EnvWorkspaceID) == "" {
		return nil
	}
	client, err := cozeloop.NewClient()
	if err != nil {
		return nil
	}
	return &Coze{client: client}
}

// Coze adapts the cozeloop client to the package-local Tracer/Span
// interfaces.
type Coze struct {
	client cozeloop.Client
}

func (c *Coze) StartSpan(ctx context.Context, name, spanType string, tags map[string]any, input any) (context.Context, Span) {
	spanCtx, span := c.client.StartSpan(ctx, name, spanType, nil)
	if len(tags) > 0 {
		span.SetTags(spanCtx, tags)
	}
	if input != nil {
		span.SetInput(spanCtx, input)
	}
	return spanCtx, &cozeSpan{ctx: spanCtx, span: span}
}

func (c *Coze) Close(ctx context.Context) {
	c.client.Close(ctx)
}

type cozeSpan struct {
	ctx  context.Context
	span cozeloop.Span
}

func (s *cozeSpan) SetTags(_ context.Context, tags map[string]any) {
	s.span.SetTags(s.ctx, tags)
}

func (s *cozeSpan) SetInput(_ context.Context, input any) {
	s.span.SetInput(s.ctx, input)
}

func (s *cozeSpan) SetOutput(_ context.Context, output any) {
	s.span.SetOutput(s.ctx, output)
}

func (s *cozeSpan) SetError(_ context.Context, err error) {
	s.span.SetError(s.ctx, err)
}

func (s *cozeSpan) SetStatusCode(_ context.Context, code int) {
	s.span.SetStatusCode(s.ctx, code)
}

func (s *cozeSpan) SetModelName(_ context.Context, name string) {
	s.span.SetModelName(s.ctx, name)
}

func (s *cozeSpan) SetInputTokens(_ context.Context, n int) {
	s.span.SetInputTokens(s.ctx, n)
}

func (s *cozeSpan) SetOutputTokens(_ context.Context, n int) {
	s.span.SetOutputTokens(s.ctx, n)
}

func (s *cozeSpan) Finish(_ context.Context) {
	s.span.Finish(s.ctx)
}
