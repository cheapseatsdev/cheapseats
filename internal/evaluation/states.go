package evaluation

import "github.com/cheapseatsdev/cheapseats/internal/state"

type CheckState string

const (
	CheckReady   CheckState = "ready"
	CheckRunning CheckState = "running"
	CheckDone    CheckState = "done"
	CheckFailed  CheckState = "failed"
)

var CheckStates = []CheckState{CheckReady, CheckRunning, CheckDone, CheckFailed}
var CheckTransitions = state.Table[CheckState]{
	state.Start:  {CheckReady},
	CheckReady:   {CheckRunning},
	CheckRunning: {CheckDone, CheckFailed, CheckReady}, // paused without consuming an attempt
	CheckFailed:  {CheckRunning},
}

var CheckBatchTransitions = state.Table[CheckState]{
	state.Start:  {CheckReady},
	CheckReady:   {CheckRunning},
	CheckRunning: {CheckDone, CheckFailed, CheckReady},
	CheckFailed:  {CheckRunning},
	CheckDone:    {CheckRunning}, // explicit retry of failed items only
}

type ReviewState string

const (
	ReviewDraft      ReviewState = "draft"
	ReviewReviewed   ReviewState = "reviewed"
	ReviewUnresolved ReviewState = "unresolved"
)

var ReviewStates = []ReviewState{ReviewDraft, ReviewReviewed, ReviewUnresolved}
var ReviewTransitions = state.Table[ReviewState]{
	state.Start:      {ReviewDraft, ReviewReviewed, ReviewUnresolved},
	ReviewDraft:      {ReviewReviewed, ReviewUnresolved},
	ReviewReviewed:   {ReviewDraft, ReviewUnresolved},
	ReviewUnresolved: {ReviewDraft, ReviewReviewed},
}
