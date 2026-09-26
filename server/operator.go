package server

// Operator is gizmo-server's side of the robot: it watches the camera, runs
// perception and behaviours, and holds control of the robot on behalf of the
// human at the GUI.
//
// Control lives on the robot. Operator acquires it as a HUMAN controller, so
// while the operator is driving (or a behaviour they started is running) no AI
// agent can take over. It renews the lease only while that is true -- an
// operator who walks away lets it lapse, and agents can drive again.

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log"
	"sync"
	"time"

	"github.com/arabenjamin/gizmo-server/behavior"
	"github.com/arabenjamin/gizmo-server/perception"
	"github.com/arabenjamin/gizmo-server/robotapi"
	"github.com/hybridgroup/mjpeg"
)

const (
	holderName = "gizmo-gui"
	// activeWindow: the operator counts as present this long after their last
	// interaction. The robot drops an idle human lease after 2 minutes.
	activeWindow   = 90 * time.Second
	keepaliveEvery = 30 * time.Second
	trackSpeedMs   = 10 // per-degree servo delay for tracking moves
)

type Operator struct {
	robot  *robotapi.Client
	det    *perception.Detector
	stream *mjpeg.Stream
	log    *log.Logger

	mu           sync.Mutex
	token        string
	lostReason   string
	tracking     bool
	scan         bool
	tracker      *behavior.Tracker
	lastActivity time.Time

	connected  bool
	streamErr  string
	lastFrame  robotapi.Frame
	faces      []perception.Face
	frameTimes []time.Time // recent frame arrivals, for fps
	detectMs   float64
	mode       behavior.Mode
}

func NewOperator(robot *robotapi.Client, det *perception.Detector, stream *mjpeg.Stream, lg *log.Logger) *Operator {
	return &Operator{
		robot: robot, det: det, stream: stream, log: lg,
		tracker: behavior.NewTracker(behavior.DefaultConfig, behavior.Limits{PanMin: -90, PanMax: 90, TiltMin: -130, TiltMax: 50}),
		mode:    behavior.ModeIdle,
	}
}

// Run starts the frame, command and keepalive loops until ctx ends.
func (o *Operator) Run(ctx context.Context) {
	if lim, err := o.robot.Limits(ctx); err == nil {
		o.mu.Lock()
		o.tracker = behavior.NewTracker(behavior.DefaultConfig, behavior.Limits(lim))
		o.mu.Unlock()
	} else {
		o.log.Printf("OPERATOR: using default look limits (robot said: %v)", err)
	}
	go o.frameLoop(ctx)
	go o.commandLoop(ctx)
	go o.keepaliveLoop(ctx)
}

// frameLoop keeps a stream open to the robot, reconnecting as needed.
func (o *Operator) frameLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := o.robot.StreamFrames(ctx, o.onFrame)
		o.mu.Lock()
		o.connected = false
		switch {
		case errors.Is(err, robotapi.ErrCameraOff):
			o.streamErr = "robot camera is off"
		case err != nil:
			o.streamErr = err.Error()
		default:
			o.streamErr = "stream ended"
		}
		o.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (o *Operator) onFrame(f robotapi.Frame) {
	g, err := perception.DecodeGray(f.JPEG)
	if err != nil {
		return
	}
	start := time.Now()
	faces := o.det.Detect(g)
	detectMs := float64(time.Since(start).Microseconds()) / 1000

	o.mu.Lock()
	o.connected, o.streamErr = true, ""
	o.lastFrame = robotapi.Frame{Seq: f.Seq, CapturedAt: f.CapturedAt, Pan: f.Pan, Tilt: f.Tilt, Moving: f.Moving}
	o.faces, o.detectMs = faces, detectMs
	now := time.Now()
	o.frameTimes = append(o.frameTimes, now)
	for len(o.frameTimes) > 0 && now.Sub(o.frameTimes[0]) > 2*time.Second {
		o.frameTimes = o.frameTimes[1:]
	}
	targets := make([]behavior.Target, len(faces))
	for i, fc := range faces {
		targets[i] = behavior.Target{X: fc.X, Y: fc.Y}
	}
	o.tracker.Observe(behavior.Pose{Pan: f.Pan, Tilt: f.Tilt}, f.Moving, f.CapturedAt, targets)
	o.mu.Unlock()

	// The operator's view: the robot's frame with detections drawn on it.
	perception.Annotate(g, faces)
	var buf bytes.Buffer
	if jpeg.Encode(&buf, g, &jpeg.Options{Quality: 80}) == nil {
		o.stream.UpdateJPEG(buf.Bytes())
	}
}

// commandLoop executes the tracker's decisions while a behaviour is enabled.
func (o *Operator) commandLoop(ctx context.Context) {
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		o.mu.Lock()
		o.tracker.Scan = o.scan
		active := (o.tracking || o.scan) && o.token != ""
		var (
			pose behavior.Pose
			mode behavior.Mode
			ok   bool
		)
		if active {
			pose, mode, ok = o.tracker.Next(time.Now())
			if mode == behavior.ModeTracking && !o.tracking {
				ok = false // a face is seen but only scanning is enabled
				mode = behavior.ModeScanning
			}
		} else {
			mode = behavior.ModeIdle
		}
		o.mode = mode
		token := o.token
		o.mu.Unlock()
		if !ok {
			continue
		}

		err := o.robot.Look(ctx, token, pose.Pan, pose.Tilt, trackSpeedMs)
		o.mu.Lock()
		var held *robotapi.ErrControlHeld
		switch {
		case errors.As(err, &held):
			o.loseControlLocked(held.Message)
		case err != nil:
			o.log.Printf("OPERATOR: look failed: %v", err)
		default:
			o.tracker.Commanded(pose, mode, time.Now())
		}
		o.mu.Unlock()
	}
}

// keepaliveLoop renews the lease while the operator is present or a behaviour
// runs, and notices when the lease has gone.
func (o *Operator) keepaliveLoop(ctx context.Context) {
	tick := time.NewTicker(keepaliveEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		o.mu.Lock()
		token := o.token
		wanted := o.tracking || o.scan || time.Since(o.lastActivity) < activeWindow
		o.mu.Unlock()
		if token == "" {
			continue
		}
		if !wanted {
			// Present no longer: stop renewing and give control back.
			o.robot.Release(ctx, token)
			o.mu.Lock()
			if o.token == token {
				o.token = ""
				o.lostReason = "released after the operator was idle"
			}
			o.mu.Unlock()
			continue
		}
		_, err := o.robot.Acquire(ctx, token, holderName, "human", false)
		var held *robotapi.ErrControlHeld
		if errors.As(err, &held) {
			o.mu.Lock()
			o.loseControlLocked(held.Message)
			o.mu.Unlock()
		}
	}
}

// loseControlLocked stops everything that needs control. Caller holds mu.
func (o *Operator) loseControlLocked(why string) {
	if o.token == "" {
		return
	}
	o.log.Printf("OPERATOR: lost control: %s", why)
	o.token, o.lostReason = "", why
	o.tracking, o.scan = false, false
}

// TakeControl acquires control of the robot as the human operator.
func (o *Operator) TakeControl(ctx context.Context, force bool) error {
	o.mu.Lock()
	token := o.token
	o.mu.Unlock()
	tok, err := o.robot.Acquire(ctx, token, holderName, "human", force)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.token, o.lostReason, o.lastActivity = tok, "", time.Now()
	o.mu.Unlock()
	return nil
}

// ReleaseControl gives control back and stops behaviours.
func (o *Operator) ReleaseControl(ctx context.Context) error {
	o.mu.Lock()
	token := o.token
	o.token, o.tracking, o.scan = "", false, false
	o.mu.Unlock()
	if token == "" {
		return nil
	}
	return o.robot.Release(ctx, token)
}

var errNoControl = errors.New("take control of the robot first")

// SetBehavior turns tracking and/or scanning on or off. Nil leaves a flag as is.
func (o *Operator) SetBehavior(tracking, scan *bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	turningOn := (tracking != nil && *tracking) || (scan != nil && *scan)
	if turningOn && o.token == "" {
		return errNoControl
	}
	if tracking != nil {
		if *tracking && !o.tracking {
			o.tracker.Reset() // fresh target; resync aim from the next frame
		}
		o.tracking = *tracking
	}
	if scan != nil {
		o.scan = *scan
	}
	o.lastActivity = time.Now()
	return nil
}

// Touch records operator activity (keeps a held lease alive).
func (o *Operator) Touch() {
	o.mu.Lock()
	o.lastActivity = time.Now()
	o.mu.Unlock()
}

// Token returns the control token if the operator holds control.
func (o *Operator) Token() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.token
}

// State is the operator's status for the GUI.
func (o *Operator) State(ctx context.Context) map[string]any {
	robotControl, ctlErr := o.robot.Control(ctx)
	o.mu.Lock()
	defer o.mu.Unlock()
	// Count only recent arrivals, so the rate drops to zero when frames stop
	// (frameTimes is otherwise pruned only when a new frame arrives).
	var recent []time.Time
	for _, t := range o.frameTimes {
		if time.Since(t) <= 2*time.Second {
			recent = append(recent, t)
		}
	}
	fps := 0.0
	if n := len(recent); n > 1 {
		fps = float64(n-1) / recent[n-1].Sub(recent[0]).Seconds()
	}
	st := map[string]any{
		"connected":   o.connected,
		"stream_err":  o.streamErr,
		"you_control": o.token != "",
		"lost_reason": o.lostReason,
		"tracking":    o.tracking,
		"scan":        o.scan,
		"mode":        o.mode,
		"faces":       o.faces,
		"fps":         fps,
		"detect_ms":   o.detectMs,
		"frame": map[string]any{
			"seq": o.lastFrame.Seq, "pan": o.lastFrame.Pan, "tilt": o.lastFrame.Tilt,
			"moving": o.lastFrame.Moving, "captured_at": o.lastFrame.CapturedAt,
		},
	}
	if ctlErr == nil {
		st["control"] = robotControl
	} else {
		st["control_err"] = ctlErr.Error()
	}
	return st
}
