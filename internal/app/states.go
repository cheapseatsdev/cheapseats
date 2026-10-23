package app

import "github.com/cheapseatsdev/cheapseats/internal/state"

// AttemptState is where work submitted for a fix stands, as fix_attempts.state stores it.
type AttemptState string

// Attempt states, and Open, the status of a fix with no attempt that's still standing.
const (
	Proposed  AttemptState = "proposed"  // a commit or pull request is up
	Accepted  AttemptState = "accepted"  // it merged
	Withdrawn AttemptState = "withdrawn" // it was closed, which reopens the fix
	Open                   = "open"
)

// AttemptStates is every attempt state, in the order the diagram reads them.
var AttemptStates = []AttemptState{Proposed, Accepted, Withdrawn}

// newAttempt is an attempt before it's recorded (state.Start).
const newAttempt AttemptState = state.Start

// AttemptTransitions is every move the code makes between attempt states. MarkFix records an
// attempt in any state; SetAttemptState moves one to any other, since a pull request can be
// reopened or a merge reverted, so every move is listed.
var AttemptTransitions = state.Table[AttemptState]{
	newAttempt: {Proposed, Accepted, Withdrawn},
	Proposed:   {Accepted, Withdrawn},
	Accepted:   {Proposed, Withdrawn},
	Withdrawn:  {Proposed, Accepted},
}

func validState(s AttemptState) bool { return s == Proposed || s == Accepted || s == Withdrawn }
