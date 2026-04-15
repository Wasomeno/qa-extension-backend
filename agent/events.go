package agent

import (
	"context"
	"fmt"
	"qa-extension-backend/database"
	"time"

	"github.com/google/uuid"
)

// EventEmitter provides a fluent, consistent API for emitting stream events.
// It ensures all events from a single operation share a correlation ID and
// that required fields are always populated correctly.
type EventEmitter struct {
	ctx           context.Context
	eventType     string
	resourceType  string
	resourceID    string
	correlationID string
	startTime     time.Time
	totalSteps    int
}

// Event types - these are the only valid values
const (
	EventTypeGeneration = "generation"
	EventTypeExecution  = "execution"
	EventTypeAgent      = "agent"
)

// Resource types - these are the only valid values
const (
	ResourceTypeScenario  = "scenario"
	ResourceTypeRecording = "recording"
	ResourceTypeSession   = "session"
)

// Stages - standardized across all event types
const (
	StageStart    = "start"
	StageProgress = "progress"
	StageDone     = "done"
	StageError    = "error"
)

// NewGenerationEmitter creates an emitter for test generation events.
func NewGenerationEmitter(ctx context.Context, scenarioID string) *EventEmitter {
	return newEmitter(ctx, EventTypeGeneration, ResourceTypeScenario, scenarioID)
}

// NewExecutionEmitter creates an emitter for test execution events.
func NewExecutionEmitter(ctx context.Context, recordingID string) *EventEmitter {
	return newEmitter(ctx, EventTypeExecution, ResourceTypeRecording, recordingID)
}

// NewAgentEmitter creates an emitter for agent tool/chat events.
func NewAgentEmitter(ctx context.Context, sessionID string) *EventEmitter {
	if sessionID == "" {
		return newEmitter(ctx, EventTypeAgent, "", "")
	}
	return newEmitter(ctx, EventTypeAgent, ResourceTypeSession, sessionID)
}

// NewAgentToolEmitter creates an emitter for agent tool execution (no specific resource).
func NewAgentToolEmitter(ctx context.Context) *EventEmitter {
	return newEmitter(ctx, EventTypeAgent, "", "")
}

func newEmitter(ctx context.Context, eventType, resourceType, resourceID string) *EventEmitter {
	return &EventEmitter{
		ctx:           ctx,
		eventType:     eventType,
		resourceType:  resourceType,
		resourceID:    resourceID,
		correlationID: uuid.New().String(),
		startTime:     time.Now(),
	}
}

// SetTotalSteps configures the total step count for progress tracking.
func (e *EventEmitter) SetTotalSteps(total int) *EventEmitter {
	e.totalSteps = total
	return e
}

// Elapsed returns the time since the emitter was created.
func (e *EventEmitter) Elapsed() time.Duration {
	return time.Since(e.startTime)
}

// Start emits a start event with a formatted message.
func (e *EventEmitter) Start(format string, args ...any) error {
	return e.emit(StageStart, fmt.Sprintf(format, args...), nil, nil)
}

// Progress emits a progress event with optional step info.
func (e *EventEmitter) Progress(message string) error {
	return e.emit(StageProgress, message, nil, nil)
}

// Progressf emits a progress event with a formatted message.
func (e *EventEmitter) Progressf(format string, args ...any) error {
	return e.emit(StageProgress, fmt.Sprintf(format, args...), nil, nil)
}

// Step emits a progress event with step information.
func (e *EventEmitter) Step(currentStep int, stepName string) error {
	return e.StepWithAction(currentStep, stepName, "")
}

// StepWithAction emits a progress event with step information including action type.
func (e *EventEmitter) StepWithAction(currentStep int, stepName, action string) error {
	stepInfo := &database.StreamStepInfo{
		CurrentStep: currentStep,
		TotalSteps:  e.totalSteps,
		StepName:    stepName,
		Action:      action,
	}
	if e.totalSteps > 0 {
		stepInfo.Progress = (currentStep * 100) / e.totalSteps
	}
	return e.emit(StageProgress, stepName, stepInfo, nil)
}

// Done emits a completion event.
func (e *EventEmitter) Done(format string, args ...any) error {
	return e.emit(StageDone, fmt.Sprintf(format, args...), nil, nil)
}

// Error emits an error event with just a message.
func (e *EventEmitter) Error(message string) error {
	return e.emit(StageError, message, nil, nil)
}

// ErrorWithCode emits an error event with a code and details.
func (e *EventEmitter) ErrorWithCode(code, message, details string) error {
	return e.emit(StageError, message, nil, &database.StreamErrorInfo{
		Code:    code,
		Details: details,
	})
}

// ErrorFromErr emits an error event from a Go error.
func (e *EventEmitter) ErrorFromErr(err error) error {
	if err == nil {
		return nil
	}
	return e.emit(StageError, err.Error(), nil, nil)
}

// emit is the internal method that publishes the event.
func (e *EventEmitter) emit(stage, message string, stepInfo *database.StreamStepInfo, errorInfo *database.StreamErrorInfo) error {
	event := database.StreamEvent{
		Type:          e.eventType,
		ResourceType:  e.resourceType,
		ResourceID:    e.resourceID,
		Stage:         stage,
		Message:       message,
		StepInfo:      stepInfo,
		ErrorInfo:     errorInfo,
		CorrelationID: e.correlationID,
		Timestamp:     time.Now().Format(time.RFC3339),
	}
	return database.PublishStreamEvent(e.ctx, event)
}

// ============================================================================
// Convenience functions for quick one-off events
// ============================================================================

// EmitQuickStart emits a simple start event without creating an emitter.
func EmitQuickStart(ctx context.Context, eventType, resourceType, resourceID, message string) error {
	return database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:          eventType,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Stage:         StageStart,
		Message:       message,
		CorrelationID: uuid.New().String(),
	})
}

// EmitQuickDone emits a simple done event.
func EmitQuickDone(ctx context.Context, eventType, resourceType, resourceID, message string) error {
	return database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:          eventType,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Stage:         StageDone,
		Message:       message,
		CorrelationID: uuid.New().String(),
	})
}

// EmitQuickError emits a simple error event.
func EmitQuickError(ctx context.Context, eventType, resourceType, resourceID, message string) error {
	return database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:          eventType,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Stage:         StageError,
		Message:       message,
		CorrelationID: uuid.New().String(),
	})
}
