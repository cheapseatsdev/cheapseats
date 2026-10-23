package budget

import "github.com/cheapseatsdev/cheapseats/internal/state"

// State is where a billable attempt's reservation stands, as spend_events.state stores it.
type State string

const (
	Reserved State = "reserved" // admitted: the allowance is held against today's budget
	Settled  State = "settled"  // the known cost replaced the reservation
	Unknown  State = "unknown"  // the cost isn't known: the larger of it and the reservation stands
)

// States is every reservation state, in the order the diagram reads them.
var States = []State{Reserved, Settled, Unknown}

// Transitions is every move the code makes between reservation states: Reserve admits, Finish
// settles. A process that dies between the two leaves the reservation standing.
var Transitions = state.Table[State]{
	state.Start: {Reserved},
	Reserved:    {Settled, Unknown},
}
