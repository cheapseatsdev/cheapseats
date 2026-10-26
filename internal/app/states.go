package app

import "github.com/cheapseatsdev/cheapseats/internal/state"

// AttemptState is where work submitted for a fix stands, as fix_attempts.state stores it.
type AttemptState string

// Attempt states, and Open, the status of a fix with no attempt that's still standing.
const (
	Proposed  AttemptState = "proposed"  // a commit or pull request is up
	Accepted  AttemptState = "accepted"  // it merged
	Withdrawn AttemptState = "withdrawn" // it was closed, which reopens the fix
	Dismissed AttemptState = "dismissed" // the team decided not to do the fix; the note says why
	Open                   = "open"
)

// AttemptStates is every attempt state, in the order the diagram reads them.
var AttemptStates = []AttemptState{Proposed, Accepted, Withdrawn, Dismissed}

// newAttempt is an attempt before it's recorded (state.Start).
const newAttempt AttemptState = state.Start

// AttemptTransitions is every move the code makes between attempt states. MarkFix records an
// attempt in any state; SetAttemptState moves one to any other, since a pull request can be
// reopened or a merge reverted. A dismissal has no work behind it, so it can only be withdrawn,
// which undoes it.
var AttemptTransitions = state.Table[AttemptState]{
	newAttempt: {Proposed, Accepted, Withdrawn, Dismissed},
	Proposed:   {Accepted, Withdrawn, Dismissed},
	Accepted:   {Proposed, Withdrawn},
	Withdrawn:  {Proposed, Accepted, Dismissed},
	Dismissed:  {Withdrawn},
}

func validState(s AttemptState) bool {
	return s == Proposed || s == Accepted || s == Withdrawn || s == Dismissed
}

// Shipped reports whether the attempt stands for work that went out, proposed or accepted: what
// an outcome measures and the trend marks.
func (s AttemptState) Shipped() bool { return s == Proposed || s == Accepted }
