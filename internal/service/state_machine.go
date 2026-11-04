// Package service coordinates job state and cooperative cancellation.
package service

import "fmt"

type State string

const (
	Precheck     State = "PRECHECK"
	Snapshotting State = "SNAPSHOTTING"
	CatchingUp   State = "CATCHING_UP"
	Streaming    State = "STREAMING"
	Polling      State = "POLLING"
	Paused       State = "PAUSED"
	Stopped      State = "STOPPED"
)

type Event string

const (
	PrecheckPassed    Event = "precheck_passed"
	SnapshotCollected Event = "snapshot_collected"
	ActivateStream    Event = "activate_stream"
	ActivatePoll      Event = "activate_poll"
	Pause             Event = "pause"
	Resume            Event = "resume"
	Stop              Event = "stop"
	Restart           Event = "restart"
)

type JobState struct {
	State      State  `json:"state"`
	ResumeFrom State  `json:"resume_from,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func active(state State) bool {
	switch state {
	case Precheck, Snapshotting, CatchingUp, Streaming, Polling:
		return true
	}
	return false
}

// MaxReasonLength bounds persisted reasons; reasons are fixed summaries, never
// raw driver or target errors.
const MaxReasonLength = 512

// Validate rejects state that Transition could never have produced, so a
// corrupted or hand-edited row stops the runner instead of being reinterpreted.
func (s JobState) Validate() error {
	switch {
	case active(s.State):
		if s.ResumeFrom != "" {
			return fmt.Errorf("active state %s must not carry resume_from", s.State)
		}
	case s.State == Paused:
		if !active(s.ResumeFrom) {
			return fmt.Errorf("paused state requires an active resume_from")
		}
	case s.State == Stopped:
		if s.ResumeFrom != "" && !active(s.ResumeFrom) {
			return fmt.Errorf("stopped state has invalid resume_from")
		}
	default:
		return fmt.Errorf("unknown job state")
	}
	if len(s.Reason) > MaxReasonLength {
		return fmt.Errorf("job state reason too long")
	}
	return nil
}
func Transition(current JobState, event Event) (JobState, error) {
	if !active(current.State) && current.State != Paused && current.State != Stopped {
		return JobState{}, fmt.Errorf("invalid persisted job state")
	}
	next := current
	next.Reason = ""
	switch event {
	case Pause:
		if active(current.State) {
			next.State = Paused
			next.ResumeFrom = current.State
			return next, nil
		}
	case Stop:
		if current.State == Stopped {
			return current, nil
		}
		next.State = Stopped
		if current.State != Paused {
			next.ResumeFrom = current.State
		}
		return next, nil
	case Resume:
		if current.State == Paused && active(current.ResumeFrom) {
			return JobState{State: current.ResumeFrom}, nil
		}
	case Restart:
		if current.State == Stopped && active(current.ResumeFrom) {
			return JobState{State: current.ResumeFrom}, nil
		}
	case PrecheckPassed:
		if current.State == Precheck {
			return JobState{State: Snapshotting}, nil
		}
	case SnapshotCollected:
		if current.State == Snapshotting {
			return JobState{State: CatchingUp}, nil
		}
	case ActivateStream:
		if current.State == CatchingUp {
			return JobState{State: Streaming}, nil
		}
	case ActivatePoll:
		if current.State == CatchingUp {
			return JobState{State: Polling}, nil
		}
	}
	return JobState{}, fmt.Errorf("invalid state transition %s + %s", current.State, event)
}
