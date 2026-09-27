// Package behavior decides where the robot's head should look. It is pure
// logic -- no I/O, no goroutines -- so it can be tested exhaustively; the
// server feeds it frames and executes the commands it returns.
//
// Ported from the robot's old on-board GizmoBrain, with one structural change:
// every frame now carries the head pose it was CAPTURED at. The tracker
// therefore estimates the target's absolute direction (capture pose + offset)
// instead of an offset relative to wherever the head is now. The old design
// had to rate-limit itself and act once per detection because frames captured
// before a move landed made it re-apply the same correction; with absolute
// estimates a stale frame simply agrees with the fresh ones.
package behavior

import (
	"math"
	"time"
)

// Pose is a head aim in degrees: pan from centre, tilt from level.
type Pose struct{ Pan, Tilt float64 }

type Limits struct{ PanMin, PanMax, TiltMin, TiltMax float64 }

// Target is a face centre in image pixels.
type Target struct{ X, Y int }

type Config struct {
	FrameW, FrameH float64
	// Measured 2026-09-17 by phase-correlating head-camera frames during
	// single-joint sweeps.
	PxPerDegPan, PxPerDegTilt float64
	// PanSign maps "target right of centre" to a pan correction. Measured:
	// raising the base moves scene content RIGHT, so centring a right-hand
	// target needs pan to DECREASE (-1). Tilt needs no sign: a higher tilt
	// looks up, and image y grows downward.
	PanSign float64

	Gain        float64       // fraction of the remaining error closed per command
	DeadbandPx  float64       // hold still when the target is this close to centre
	MaxStepDeg  float64       // cap per command, so one bad detection cannot lurch
	FilterAlpha float64       // EMA weight of a new target estimate
	StaleAfter  time.Duration // no detection for this long => target lost
	// HoldAfterLost keeps the head where the target was last seen before the
	// idle scan resumes. Detection drops out for a second or two whenever a
	// face turns or the confidence dips; the person is almost always still
	// there, and scanning straight away swung the head off them (measured).
	HoldAfterLost time.Duration
	MinInterval   time.Duration // minimum gap between commands
	ScanStepDeg   float64
	ScanInterval  time.Duration
	ScanPanLimit  float64
	ScanBands     []float64 // tilt levels the idle scan sweeps through
}

var DefaultConfig = Config{
	FrameW: 640, FrameH: 480,
	PxPerDegPan: 9.8, PxPerDegTilt: 11.2,
	PanSign:       -1,
	Gain:          0.6,
	DeadbandPx:    25,
	MaxStepDeg:    8,
	FilterAlpha:   0.45,
	StaleAfter:    1500 * time.Millisecond,
	HoldAfterLost: 4 * time.Second,
	MinInterval:   100 * time.Millisecond,
	ScanStepDeg:   5,
	ScanInterval:  500 * time.Millisecond,
	ScanPanLimit:  60,
	// Within ~+/-25 deg of level is where a seated or standing person's head
	// is; the ~45 deg vertical field of view makes these bands overlap.
	ScanBands: []float64{0, 12, -12, 24},
}

type Mode string

const (
	ModeIdle     Mode = "idle"
	ModeTracking Mode = "tracking"
	ModeScanning Mode = "scanning"
	ModeHolding  Mode = "holding" // target just lost; waiting before scanning
)

type Tracker struct {
	cfg Config
	lim Limits

	Scan bool // sweep for a target when none is seen

	haveTarget bool
	target     Pose // filtered absolute aim of the target
	lastSeen   time.Time

	haveAim bool
	aim     Pose // where the head is (last command, resynced from frames)
	lastCmd time.Time

	scanDir  float64
	scanBand int
	lastScan time.Time
}

func NewTracker(cfg Config, lim Limits) *Tracker {
	return &Tracker{cfg: cfg, lim: lim, scanDir: 1}
}

// Reset forgets the target and the believed aim; the next frame resyncs.
func (t *Tracker) Reset() {
	t.haveTarget, t.haveAim = false, false
}

// Observe feeds one analysed frame: the head pose it was captured at, whether
// the head was moving, and the faces found (largest first).
func (t *Tracker) Observe(capture Pose, moving bool, capturedAt time.Time, faces []Target) {
	// Resync the believed aim from reality on settled frames taken after our
	// last command, so a manual move by the operator is not fought.
	if !t.haveAim || (!moving && capturedAt.After(t.lastCmd)) {
		t.aim, t.haveAim = capture, true
	}
	if len(faces) == 0 {
		return
	}
	f := faces[0]
	errPan := (float64(f.X) - t.cfg.FrameW/2) / t.cfg.PxPerDegPan
	errTilt := (float64(f.Y) - t.cfg.FrameH/2) / t.cfg.PxPerDegTilt
	abs := t.clamp(Pose{
		Pan:  capture.Pan + t.cfg.PanSign*errPan,
		Tilt: capture.Tilt - errTilt,
	})
	if t.haveTarget {
		a := t.cfg.FilterAlpha
		t.target = Pose{a*abs.Pan + (1-a)*t.target.Pan, a*abs.Tilt + (1-a)*t.target.Tilt}
	} else {
		t.target, t.haveTarget = abs, true
	}
	t.lastSeen = capturedAt
}

// Next returns the command to issue now, if any.
func (t *Tracker) Next(now time.Time) (Pose, Mode, bool) {
	if t.haveTarget && now.Sub(t.lastSeen) > t.cfg.StaleAfter {
		t.haveTarget = false
	}
	if !t.haveAim || now.Sub(t.lastCmd) < t.cfg.MinInterval {
		return Pose{}, t.Mode(now), false
	}

	if t.haveTarget {
		dPan, dTilt := t.target.Pan-t.aim.Pan, t.target.Tilt-t.aim.Tilt
		if math.Abs(dPan*t.cfg.PxPerDegPan) < t.cfg.DeadbandPx &&
			math.Abs(dTilt*t.cfg.PxPerDegTilt) < t.cfg.DeadbandPx {
			return Pose{}, ModeTracking, false // centred
		}
		next := t.clamp(Pose{
			Pan:  t.aim.Pan + t.step(dPan),
			Tilt: t.aim.Tilt + t.step(dTilt),
		})
		if next == t.aim {
			return Pose{}, ModeTracking, false // pinned at a limit
		}
		return next, ModeTracking, true
	}

	if t.Scan && t.holding(now) {
		return Pose{}, ModeHolding, false
	}
	if t.Scan && now.Sub(t.lastScan) >= t.cfg.ScanInterval {
		pan := t.aim.Pan + t.scanDir*t.cfg.ScanStepDeg
		// At the end of a sweep, reverse and move to the next tilt band, so the
		// search covers a grid rather than one line.
		if math.Abs(pan) > t.cfg.ScanPanLimit {
			t.scanDir = -t.scanDir
			pan = t.aim.Pan + t.scanDir*t.cfg.ScanStepDeg
			t.scanBand = (t.scanBand + 1) % len(t.cfg.ScanBands)
		}
		return t.clamp(Pose{Pan: pan, Tilt: t.cfg.ScanBands[t.scanBand]}), ModeScanning, true
	}
	return Pose{}, t.Mode(now), false
}

// Commanded records that p was sent to the robot and finished at now.
func (t *Tracker) Commanded(p Pose, mode Mode, now time.Time) {
	t.aim, t.lastCmd = p, now
	if mode == ModeScanning {
		t.lastScan = now
	}
}

// Mode reports what the tracker is doing.
func (t *Tracker) Mode(now time.Time) Mode {
	switch {
	case t.haveTarget && now.Sub(t.lastSeen) <= t.cfg.StaleAfter:
		return ModeTracking
	case t.Scan && t.holding(now):
		return ModeHolding
	case t.Scan:
		return ModeScanning
	}
	return ModeIdle
}

// holding reports whether a target was seen recently enough that the scan
// should wait rather than move the head away from where it was.
func (t *Tracker) holding(now time.Time) bool {
	return !t.lastSeen.IsZero() && now.Sub(t.lastSeen) < t.cfg.HoldAfterLost
}

func (t *Tracker) step(d float64) float64 {
	s := t.cfg.Gain * d
	return math.Max(-t.cfg.MaxStepDeg, math.Min(t.cfg.MaxStepDeg, s))
}

func (t *Tracker) clamp(p Pose) Pose {
	return Pose{
		Pan:  math.Max(t.lim.PanMin, math.Min(t.lim.PanMax, p.Pan)),
		Tilt: math.Max(t.lim.TiltMin, math.Min(t.lim.TiltMax, p.Tilt)),
	}
}
