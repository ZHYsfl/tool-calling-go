package toolcalling

import "context"

type progressReporterKey struct{}

// ProgressReporter is injected through context and can be used by tools to
// stream fine-grained progress back to the orchestration control plane.
type ProgressReporter func(message string, data map[string]any)

// WithProgressReporter attaches a reporter to a context.
func WithProgressReporter(ctx context.Context, reporter ProgressReporter) context.Context {
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

// ReportProgress emits one progress event if a reporter exists in ctx.
// It returns true if reported, false otherwise.
func ReportProgress(ctx context.Context, message string, data map[string]any) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(progressReporterKey{})
	reporter, ok := v.(ProgressReporter)
	if !ok || reporter == nil {
		return false
	}
	reporter(message, data)
	return true
}
