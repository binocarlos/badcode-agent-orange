package watchapi

import "github.com/binocarlos/badcode-agent-orange/orchestrator"

// Compile-time proof that the real Slice-C/D impls satisfy the watchapi ports, so
// the demo (Task 14) and Slice F bind them without adapters.
var (
	_ Approver        = (*orchestrator.ApprovalService)(nil)
	_ Rejecter        = (*orchestrator.ApprovalService)(nil)
	_ RevisionLister  = (*orchestrator.MemBoard)(nil)
	_ TelemetryReader = (*orchestrator.MemTelemetry)(nil)

	// Feedback/Trigger reuse the frozen seams; assert the real impls satisfy them.
	_ orchestrator.FeedbackApplier = orchestrator.HumanFeedbackApplier{}
	_ orchestrator.Triggerer       = orchestrator.ExchangeTrigger{}
)
