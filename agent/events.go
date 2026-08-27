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
	projectID     string
	correlationID string
	startTime     time.Time
	totalSteps    int
}

// Event types - these are the only valid values used by the live worker.
const (
	EventTypeGeneration = "generation"
)

// Resource types - these are the only valid values used by the live worker.
const (
	ResourceTypeScenario = "scenario"
)

// Stages - standardized across all event types.
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

// NewGenerationEmitterForProject scopes generation events to an app project.
func NewGenerationEmitterForProject(ctx context.Context, scenarioID, projectID string) *EventEmitter {
	e := newEmitter(ctx, EventTypeGeneration, ResourceTypeScenario, scenarioID)
	e.projectID = projectID
	return e
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

// WithProjectID attaches the app project UUID so SSE clients can filter by project.
func (e *EventEmitter) WithProjectID(projectID string) *EventEmitter {
	e.projectID = projectID
	return e
}

// SetTotalSteps configures the total step count for progress tracking.
func (e *EventEmitter) SetTotalSteps(total int) *EventEmitter {
	e.totalSteps = total
	return e
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
		ProjectID:     e.projectID,
		Stage:         stage,
		Message:       message,
		StepInfo:      stepInfo,
		ErrorInfo:     errorInfo,
		CorrelationID: e.correlationID,
		Timestamp:     time.Now().Format(time.RFC3339),
	}
	return database.PublishStreamEvent(e.ctx, event)
}
