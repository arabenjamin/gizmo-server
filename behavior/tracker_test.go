package behavior

import (
	"math"
	"testing"
	"time"
)

var lim = Limits{PanMin: -90, PanMax: 90, TiltMin: -130, TiltMax: 50}

// project places a target at absolute aim tgt in the image of a head aimed at
// head, inverting the tracker's own model (and so the measured calibration).
func project(cfg Config, head, tgt Pose) Target {
	errPan := (tgt.Pan - head.Pan) / cfg.PanSign
	errTilt := head.Tilt - tgt.Tilt
	return Target{
		X: int(math.Round(cfg.FrameW/2 + errPan*cfg.PxPerDegPan)),
		Y: int(math.Round(cfg.FrameH/2 + errTilt*cfg.PxPerDegTilt)),
	}
}

func TestSigns(t *testing.T) {
	cfg := DefaultConfig
	now := time.Unix(1000, 0)

	// A face RIGHT of centre: pan must DECREASE (measured PanSign -1).
	tr := NewTracker(cfg, lim)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 500, Y: 240}})
	p, mode, ok := tr.Next(now.Add(200 * time.Millisecond))
	if !ok || mode != ModeTracking || p.Pan >= 0 || p.Tilt != 0 {
		t.Errorf("face right: got %+v %v %v, want pan < 0, tilt 0", p, mode, ok)
	}

	// A face BELOW centre: tilt must DECREASE (look down).
	tr = NewTracker(cfg, lim)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 320, Y: 420}})
	p, _, ok = tr.Next(now.Add(200 * time.Millisecond))
	if !ok || p.Tilt >= 0 || p.Pan != 0 {
		t.Errorf("face below: got %+v, want tilt < 0, pan 0", p)
	}
}

// TestConvergesDespiteStaleFrames simulates the failure that plagued the old
// on-board brain: the tracker only ever sees frames captured two commands ago.
// With capture-pose estimates it must still converge without overshoot.
func TestConvergesDespiteStaleFrames(t *testing.T) {
	cfg := DefaultConfig
	tr := NewTracker(cfg, lim)
	person := Pose{Pan: -27, Tilt: 14}
	head := Pose{0, 0}
	now := time.Unix(1000, 0)
	type frame struct {
		pose Pose
		at   time.Time
	}
	history := []frame{{head, now}, {head, now}, {head, now}}

	maxPan := 0.0
	commands := 0
	for i := 0; i < 40; i++ {
		// The frame the tracker sees was captured two commands ago, and its
		// timestamp says so.
		stale := history[len(history)-3]
		tr.Observe(stale.pose, false, stale.at, []Target{project(cfg, stale.pose, person)})
		now = now.Add(150 * time.Millisecond)
		if p, _, ok := tr.Next(now); ok {
			tr.Commanded(p, ModeTracking, now)
			head = p
			commands++
		}
		history = append(history, frame{head, now.Add(time.Millisecond)})
		maxPan = math.Min(maxPan, head.Pan)
	}
	if commands < 3 {
		t.Fatalf("only %d commands issued; the simulation is not exercising tracking", commands)
	}
	if math.Abs(head.Pan-person.Pan) > 3 || math.Abs(head.Tilt-person.Tilt) > 3 {
		t.Errorf("did not converge: head %+v, person %+v", head, person)
	}
	if overshoot := person.Pan - maxPan; overshoot > 1.5 {
		t.Errorf("overshot pan by %.1f deg", overshoot)
	}
}

func TestDeadbandHoldsWhenCentred(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	now := time.Unix(1000, 0)
	tr.Observe(Pose{10, 5}, false, now, []Target{{X: 330, Y: 245}}) // ~1 deg off
	if _, mode, ok := tr.Next(now.Add(time.Second)); ok || mode != ModeTracking {
		t.Errorf("centred target: ok=%v mode=%v, want hold while tracking", ok, mode)
	}
}

func TestStepIsCapped(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	now := time.Unix(1000, 0)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 639, Y: 240}}) // far right edge
	p, _, _ := tr.Next(now.Add(200 * time.Millisecond))
	if math.Abs(p.Pan) > DefaultConfig.MaxStepDeg+1e-9 {
		t.Errorf("step %.1f exceeds cap %.1f", p.Pan, DefaultConfig.MaxStepDeg)
	}
}

func TestMinIntervalBetweenCommands(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	now := time.Unix(1000, 0)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 600, Y: 240}})
	p, m, _ := tr.Next(now)
	tr.Commanded(p, m, now)
	if _, _, ok := tr.Next(now.Add(50 * time.Millisecond)); ok {
		t.Error("commanded again inside MinInterval")
	}
}

func TestLostTargetGoesIdleThenScans(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	now := time.Unix(1000, 0)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 600, Y: 240}})
	later := now.Add(2 * time.Second) // past StaleAfter
	if _, mode, ok := tr.Next(later); ok || mode != ModeIdle {
		t.Errorf("lost target without scan: ok=%v mode=%v, want idle", ok, mode)
	}
	tr.Scan = true
	// Just lost: hold where the target was rather than scanning away from it.
	if _, mode, ok := tr.Next(later); ok || mode != ModeHolding {
		t.Errorf("just after losing the target: ok=%v mode=%v, want holding", ok, mode)
	}
	p, mode, ok := tr.Next(now.Add(DefaultConfig.HoldAfterLost + time.Second))
	if !ok || mode != ModeScanning || p.Pan != 5 || p.Tilt != 0 {
		t.Errorf("scan step: %+v %v %v, want pan 5 tilt 0 scanning", p, mode, ok)
	}
}

func TestScanReversesAndChangesBand(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	tr.Scan = true
	now := time.Unix(1000, 0)
	tr.Observe(Pose{58, 0}, false, now, nil)
	p, _, ok := tr.Next(now.Add(time.Second))
	if !ok || p.Pan != 53 || p.Tilt != 12 {
		t.Errorf("at sweep end: %+v, want reverse to pan 53 and next band tilt 12", p)
	}
}

func TestResyncsAfterManualMove(t *testing.T) {
	tr := NewTracker(DefaultConfig, lim)
	now := time.Unix(1000, 0)
	tr.Observe(Pose{0, 0}, false, now, nil)
	tr.Commanded(Pose{5, 0}, ModeScanning, now)
	// The operator aims the head elsewhere; a settled frame after that says so.
	tr.Observe(Pose{-40, 10}, false, now.Add(time.Second), nil)
	tr.Scan = true
	p, _, _ := tr.Next(now.Add(2 * time.Second))
	if p.Pan != -35 {
		t.Errorf("scan continued from stale aim: next pan %.0f, want -35 (from the real pose -40)", p.Pan)
	}
}

func TestClampsToLimits(t *testing.T) {
	tr := NewTracker(DefaultConfig, Limits{PanMin: -10, PanMax: 10, TiltMin: -5, TiltMax: 5})
	now := time.Unix(1000, 0)
	tr.Observe(Pose{0, 0}, false, now, []Target{{X: 0, Y: 0}}) // far up-left
	p, _, _ := tr.Next(now.Add(200 * time.Millisecond))
	if p.Pan > 10 || p.Tilt > 5 {
		t.Errorf("command %+v exceeds limits", p)
	}
}
