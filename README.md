# Gizmo Server

The operator station for [Gizmatron](https://github.com/arabenjamin/gizmatron). The robot is
deliberately a "fancy RC car" with no agency of its own; gizmo-server is where the thinking
happens:

- **Perception:** pulls the robot's camera stream and runs face detection
  ([Pigo](https://github.com/esimov/pigo), pure Go).
- **Behaviours:** face tracking and an idle scan that looks for people, driving the
  robot's head over its device API.
- **Human control panel:** a web GUI at `/` with the annotated camera feed,
  take/release control, behaviour toggles, and manual head aiming.

AI agents do **not** go through gizmo-server. They talk to the robot's own MCP endpoint,
so they keep working when gizmo-server is down.

## Who has control

The robot arbitrates control itself: one controller at a time, and **a human can take
control from an AI agent, never the reverse**. gizmo-server acquires control as a *human*
when you press **Take control**, and every command it sends (manual aims and the tracker's
moves) carries that lease. It renews the lease only while you are active in the GUI or a
behaviour you started is running. Walk away and it lapses, and agents can drive again.

If someone else takes over, behaviours stop immediately and the GUI says who took control.

## How tracking works

Every robot frame carries the head pose **at the moment it was captured**
(`X-Head-Pan/Tilt` on each MJPEG part). The tracker converts a face's pixel offset into
the target's *absolute* direction, `capture pose + offset`, rather than an offset from
wherever the head is now. A frame captured before the last move landed then agrees with the
fresh ones instead of causing a second correction. The old on-board tracker suffered exactly
that failure, and the simulation test in `behavior/tracker_test.go` reproduces it.

Calibration (measured on the robot): 9.8 px per degree of pan, 11.2 px per degree of tilt,
and pan sign −1 (raising the base moves image content right).

Tilt is calibrated with the arm in its start pose. The robot's `look` raises the arm into
that pose automatically, so tracking works straight from the folded rest pose. After losing a
face, the tracker holds its aim for 4 s before scanning again, because detection drops out
briefly whenever a face turns.

## Layout

| Package | Role |
|---|---|
| `robotapi` | Robot client: MJPEG stream with per-frame telemetry, control lease, look |
| `perception` | Pigo face detection (cascade embedded) and frame annotation |
| `behavior` | Tracker and idle scan: pure logic, no I/O |
| `server` | HTTP server, `Operator` (loops, lease handling), GUI |

## Running

```bash
go test ./...

docker build -t gizmo-server:latest .
docker run -d --name gizmo-server --restart unless-stopped \
  -p 127.0.0.1:9090:9090 \
  -e GIZMATRON_URL=https://gizmatron.<tailnet>.ts.net \
  gizmo-server:latest
```

Then open http://localhost:9090.

**Bind to `127.0.0.1`.** The server proxies the robot's API with no authentication of its
own, so publishing port 9090 on the LAN would bypass the robot's tailnet-only access. To use
it from another device, put `tailscale serve` in front of it.

| Variable | Default | |
|---|---|---|
| `GIZMATRON_URL` | `http://localhost:8080` | Robot API |
| `BRAIN_URL` | `http://agent-brain:3000` | agent-brain, for the GUI's chat panel |

## API

| Method | Path | |
|---|---|---|
| GET | `/api/v1/state` | Connection, fps, faces, head pose, mode, who has control |
| POST/DELETE | `/api/v1/control` | Take (`{"force":bool}`) / release control as the human operator |
| POST | `/api/v1/behavior` | `{"tracking":bool,"scan":bool}` (requires control) |
| POST | `/api/v1/activity` | Operator heartbeat (keeps a held lease alive) |
| GET | `/api/v1/stream` | Annotated MJPEG feed |
| * | `/api/v1/robot/*` | Robot API proxy; stamps the operator's control token |
| * | `/api/v1/brain/*` | agent-brain proxy |
