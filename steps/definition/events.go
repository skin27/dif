package definition

// EventType names an event: something that happens to a flow or to a message
// it processes. The engine reports them to an observer and the event source
// lets a flow react to them.
type EventType string

const (
	EventFlowStarted       EventType = "flow.started"
	EventFlowPaused        EventType = "flow.paused"
	EventFlowResumed       EventType = "flow.resumed"
	EventFlowStopped       EventType = "flow.stopped"
	EventSourceFailed      EventType = "source.failed"
	EventExchangeCompleted EventType = "exchange.completed" // a message was processed to its end
	EventExchangeHandled   EventType = "exchange.handled"   // a step failed and an error route took the message
	EventExchangeFailed    EventType = "exchange.failed"    // a message failed
	EventStepCompleted     EventType = "step.completed"     // a step completed successfully
	EventStepFailed        EventType = "step.failed"        // a step failed
)

// AllEvents is the name with which an event source receives events of every type.
const AllEvents = "*"

// EventHeader is the header of an event message that holds its type. A message
// that has it is part of handling an event, and so gives no exchange or step
// events itself: a flow that reacts to events cannot trigger itself.
const EventHeader = "event.type"

var eventTypes = []EventType{
	EventFlowStarted, EventFlowPaused, EventFlowResumed, EventFlowStopped, EventSourceFailed,
	EventExchangeCompleted, EventExchangeHandled, EventExchangeFailed,
	EventStepCompleted, EventStepFailed,
}

// EventTypes returns every event type.
func EventTypes() []EventType { return append([]EventType(nil), eventTypes...) }

// ValidEventType reports whether name is an event type or AllEvents.
func ValidEventType(name string) bool {
	if name == AllEvents {
		return true
	}
	for _, t := range eventTypes {
		if string(t) == name {
			return true
		}
	}
	return false
}

// EventTopic is the name of the topic on which events of the type (or AllEvents)
// are published.
func EventTopic(t string) string { return "dif.event." + t }
