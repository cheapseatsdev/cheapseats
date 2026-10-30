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
	CheckRunning: {CheckDone, CheckFailed},
	CheckFailed:  {CheckRunning},
}
