package service

import "testing"

func TestTransitionsFollowTable(t *testing.T) {
	cases := []struct {
		state State
		event Event
		want  State
	}{
		{Precheck, PrecheckPassed, Snapshotting}, {Snapshotting, SnapshotCollected, CatchingUp},
		{CatchingUp, ActivateStream, Streaming}, {CatchingUp, ActivatePoll, Polling},
		{Streaming, Pause, Paused}, {Polling, Stop, Stopped},
	}
	for _, tc := range cases {
		next, err := Transition(JobState{State: tc.state}, tc.event)
		if err != nil || next.State != tc.want {
			t.Fatalf("%s + %s => %+v, %v", tc.state, tc.event, next, err)
		}
	}
	for _, tc := range []struct {
		state State
		event Event
	}{{Precheck, ActivateStream}, {Snapshotting, ActivatePoll}, {Stopped, ActivateStream}, {Paused, PrecheckPassed}, {Streaming, PrecheckPassed}} {
		if _, err := Transition(JobState{State: tc.state}, tc.event); err == nil {
			t.Errorf("accepted %s + %s", tc.state, tc.event)
		}
	}
}
func TestPauseResumeRetainsPreviousState(t *testing.T) {
	paused, err := Transition(JobState{State: Streaming}, Pause)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := Transition(paused, Resume)
	if err != nil || resumed.State != Streaming || resumed.ResumeFrom != "" {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err := Transition(JobState{State: Paused}, Resume); err == nil {
		t.Fatal("resumed without saved state")
	}
}
func TestStopRestartPreservesCheckpointPhase(t *testing.T) {
	paused, _ := Transition(JobState{State: Streaming}, Pause)
	stopped, err := Transition(paused, Stop)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := Transition(stopped, Restart)
	if err != nil || restarted.State != Streaming {
		t.Fatalf("restart: %+v %v", restarted, err)
	}
}
func TestTransitionOutputsValidate(t *testing.T) {
	states := []State{Precheck, Snapshotting, CatchingUp, Streaming, Polling, Paused, Stopped}
	events := []Event{PrecheckPassed, SnapshotCollected, ActivateStream, ActivatePoll, Pause, Resume, Stop, Restart}
	seen := []JobState{{State: Precheck}}
	for i := 0; i < len(seen); i++ {
		if err := seen[i].Validate(); err != nil {
			t.Fatalf("reachable state %+v rejected: %v", seen[i], err)
		}
		for _, event := range events {
			next, err := Transition(seen[i], event)
			if err != nil {
				continue
			}
			known := false
			for _, s := range seen {
				known = known || s == next
			}
			if !known {
				seen = append(seen, next)
			}
		}
	}
	for _, s := range states {
		found := false
		for _, r := range seen {
			found = found || r.State == s
		}
		if !found {
			t.Errorf("state %s unreachable from PRECHECK", s)
		}
	}
}
func TestValidateRejectsImpossibleState(t *testing.T) {
	for _, s := range []JobState{
		{State: "BOGUS"},
		{State: Paused},
		{State: Paused, ResumeFrom: Stopped},
		{State: Stopped, ResumeFrom: Paused},
		{State: Streaming, ResumeFrom: Precheck},
		{State: Precheck, Reason: string(make([]byte, MaxReasonLength+1))},
	} {
		if s.Validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}
