// Package robotapi is a client for Gizmatron's device API: its camera stream
// (with per-frame capture telemetry) and the control lease that gates motion.
package robotapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// ControlHeader carries the control token on state-changing requests.
const ControlHeader = "X-Gizmatron-Control"

// Frame is one camera frame and the head pose at the moment it was captured.
type Frame struct {
	JPEG       []byte
	Seq        uint64
	CapturedAt time.Time
	Pan, Tilt  float64
	Moving     bool
}

type Client struct {
	BaseURL string
	HTTP    *http.Client // for short requests; streams use their own client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ErrControlHeld means another controller holds the robot. Message is the
// robot's explanation (who holds it, since when).
type ErrControlHeld struct{ Message string }

func (e *ErrControlHeld) Error() string { return e.Message }

// do sends a JSON request and decodes a JSON response into out (if non-nil).
// A 409 becomes *ErrControlHeld.
func (c *Client) do(ctx context.Context, method, path, token string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(ControlHeader, token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusConflict {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = "control is held by another controller"
		}
		return &ErrControlHeld{Message: e.Error}
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Control is the robot's view of who holds control.
type Control struct {
	Held             bool   `json:"held"`
	Holder           string `json:"holder,omitempty"`
	Class            string `json:"class,omitempty"`
	Since            string `json:"since,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
}

// Acquire takes (or, when token is non-empty, renews) control. It returns the
// token to use for subsequent commands.
func (c *Client) Acquire(ctx context.Context, token, holder, class string, force bool) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/control", token,
		map[string]any{"holder": holder, "class": class, "force": force}, &out)
	if err != nil {
		return "", err
	}
	return out.Token, nil
}

func (c *Client) Release(ctx context.Context, token string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/control", token, nil, nil)
}

func (c *Client) Control(ctx context.Context) (Control, error) {
	var out struct {
		Control Control `json:"control"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/control", "", nil, &out)
	return out.Control, err
}

// Look aims the head. It returns when the move has finished.
func (c *Client) Look(ctx context.Context, token string, pan, tilt float64, speedMs int) error {
	return c.do(ctx, http.MethodPost, "/api/v1/look", token,
		map[string]any{"pan": pan, "tilt": tilt, "speed": speedMs}, nil)
}

// Limits is the reachable pan/tilt range in degrees.
type Limits struct {
	PanMin, PanMax, TiltMin, TiltMax float64
}

func (c *Client) Limits(ctx context.Context) (Limits, error) {
	var out struct {
		Limits struct {
			PanMin  float64 `json:"pan_min"`
			PanMax  float64 `json:"pan_max"`
			TiltMin float64 `json:"tilt_min"`
			TiltMax float64 `json:"tilt_max"`
		} `json:"limits"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/look", "", nil, &out); err != nil {
		return Limits{}, err
	}
	l := out.Limits
	return Limits{l.PanMin, l.PanMax, l.TiltMin, l.TiltMax}, nil
}

// ErrCameraOff means the robot's camera is not running.
var ErrCameraOff = errors.New("robot camera is not running")

// StreamFrames reads the robot's MJPEG stream and calls fn for each frame
// until ctx is cancelled or the stream ends. It returns ErrCameraOff when the
// robot reports the camera stopped.
func (c *Client) StreamFrames(ctx context.Context, fn func(Frame)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/video", nil)
	if err != nil {
		return err
	}
	// No overall timeout: the stream is open-ended. ctx ends it.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return ErrCameraOff
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("video: HTTP %d", resp.StatusCode)
	}
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return fmt.Errorf("video: not a multipart stream (%q)", resp.Header.Get("Content-Type"))
	}
	return readParts(resp.Body, params["boundary"], fn)
}

// readParts reads MJPEG parts using each part's Content-Length, so a frame is
// delivered the moment its last byte arrives. (mime/multipart only ends a part
// when the NEXT boundary shows up, which would add a full frame of latency to
// every frame -- bad for a tracking loop.)
func readParts(r io.Reader, boundary string, fn func(Frame)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	tp := textproto.NewReader(br)
	delim := "--" + boundary
	for {
		// Skip to the next boundary line (tolerating the CRLF after a part).
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return endOfStream(err)
			}
			if strings.HasPrefix(line, delim) {
				if strings.HasPrefix(line, delim+"--") {
					return nil // closing boundary
				}
				break
			}
		}
		hdr, err := tp.ReadMIMEHeader()
		if err != nil {
			return endOfStream(err)
		}
		n, err := strconv.Atoi(hdr.Get("Content-Length"))
		if err != nil || n <= 0 || n > 8<<20 {
			return fmt.Errorf("video: bad part Content-Length %q", hdr.Get("Content-Length"))
		}
		jpeg := make([]byte, n)
		if _, err := io.ReadFull(br, jpeg); err != nil {
			return endOfStream(err)
		}
		fn(frameFromHeaders(jpeg, hdr.Get))
	}
}

// endOfStream maps the ways the robot's stream ends -- it never sends a
// closing boundary, it just stops when the camera stops or the connection
// drops -- to a clean return.
func endOfStream(err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

// frameFromHeaders builds a Frame from the robot's telemetry headers. Missing
// or malformed values are left at zero rather than dropping the frame.
func frameFromHeaders(jpeg []byte, get func(string) string) Frame {
	f := Frame{JPEG: jpeg}
	f.Seq, _ = strconv.ParseUint(get("X-Frame-Seq"), 10, 64)
	f.CapturedAt, _ = time.Parse(time.RFC3339Nano, get("X-Captured-At"))
	f.Pan, _ = strconv.ParseFloat(get("X-Head-Pan"), 64)
	f.Tilt, _ = strconv.ParseFloat(get("X-Head-Tilt"), 64)
	f.Moving, _ = strconv.ParseBool(get("X-Head-Moving"))
	return f
}
