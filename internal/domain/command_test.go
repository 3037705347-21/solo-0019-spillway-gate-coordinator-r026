package domain

import (
	"testing"
	"time"
)

func assessCommand(t *testing.T, status CommandStatus, starting, target float64) GateCommand {
	t.Helper()
	command, err := NewGateCommand("cmd-x", "gate-x", target, starting, 10, "测试", "dispatcher", time.Now())
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	if status != CommandPending {
		command.Status = status
	}
	return command
}

func assessGate(aperture float64) Gate {
	return Gate{ID: "gate-x", Status: GateIdle, Aperture: aperture, MaxAperture: 10, Version: 7}
}

func TestAssessReview(t *testing.T) {
	cases := []struct {
		name     string
		starting float64
		target   float64
		current  float64
		want     ReviewAssessmentKind
	}{
		{"baseline matches and target differs", 1.0, 2.0, 1.0, ReviewReady},
		{"gate moved away from frozen baseline", 1.0, 2.0, 3.0, ReviewSuperseded},
		{"target position already reached", 1.0, 2.0, 2.0, ReviewSatisfied},
		{"gate moved but happens to rest on target", 1.0, 2.0, 2.0, ReviewSatisfied},
		{"tiny float drift within epsilon stays ready", 1.0, 2.0, 1.0 + 1e-12, ReviewReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := assessCommand(t, CommandPending, tc.starting, tc.target)
			assessment, err := command.AssessReview(assessGate(tc.current))
			if err != nil {
				t.Fatalf("assess: %v", err)
			}
			if assessment.Kind != tc.want {
				t.Fatalf("kind: want %q, got %q (%s)", tc.want, assessment.Kind, assessment.Reason)
			}
			if assessment.GateVersion != 7 {
				t.Fatalf("gate version should be carried through, got %d", assessment.GateVersion)
			}
		})
	}
}

func TestAssessReview_OnlyPending(t *testing.T) {
	command := assessCommand(t, CommandApproved, 1.0, 2.0)
	if _, err := command.AssessReview(assessGate(1.0)); err == nil {
		t.Fatal("non-pending command assessment must fail")
	} else if ErrorCodeOf(err) != "command_not_pending" {
		t.Fatalf("want command_not_pending, got %v", err)
	}
}

func TestAssessReview_GateMismatch(t *testing.T) {
	command := assessCommand(t, CommandPending, 1.0, 2.0)
	gate := assessGate(1.0)
	gate.ID = "gate-other"
	if _, err := command.AssessReview(gate); err == nil {
		t.Fatal("assessment against another gate must fail")
	}
}

// 评估只依赖位置：时间流逝不参与判定。
func TestAssessReview_TimeIsIrrelevant(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	command, err := NewGateCommand("cmd-x", "gate-x", 2.0, 1.0, 10, "测试", "dispatcher", old)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	gate := assessGate(1.0)
	gate.UpdatedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	assessment, err := command.AssessReview(gate)
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if assessment.Kind != ReviewReady {
		t.Fatalf("unchanged position must stay ready regardless of time, got %q", assessment.Kind)
	}
}
