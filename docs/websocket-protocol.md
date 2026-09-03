# WebSocket protocol

Realtime sessions connect to:

```text
GET /api/v1/realtime/sessions/{session_id}?ticket=<one-time-ticket>
```

The server validates the configured `Origin` and atomically claims the ticket before upgrading. URLs, query strings, tickets, credentials, and stream payloads must never be logged.

After a successful upgrade, the connection becomes a **Connected Session**. It is `establishing` until the runtime stream opens, `active` while carrying payload, and finished by the first terminal signal. Later close, timeout, or pump signals only participate in cleanup and cannot replace the selected outcome.

## Exec and serial

Clients send UTF-8 JSON text frames:

```json
{"type":"stdin","data":"printf ani-terminal-ok\\r"}
{"type":"resize","rows":30,"cols":120}
```

The server sends `stdout`, `stderr`, `exit`, and sanitized `error` JSON text frames. `stdin.data` is limited to 64 KiB. Rows and columns must be positive and no greater than 4096.

Malformed JSON, unsupported frame types, binary terminal frames, and serial `resize` frames produce:

```json
{"type":"error","code":"INVALID_TERMINAL_FRAME","message":"invalid terminal frame"}
```

The server then closes with status `1011`.

The required WebSocket subprotocol is `ani.terminal.v1`.

## VNC

VNC uses the `binary` WebSocket message type as a transparent RFB byte stream. It is never JSON-encoded or base64-encoded. The required WebSocket subprotocol is `ani.vnc.v1`.

Text frames are rejected with status `1011`; their contents are never reflected in the close reason, logs, metrics, or traces.

## Lifecycle outcomes

| Outcome | Trigger | Best-effort wire result |
|---|---|---|
| `normal` | Runtime EOF, or exec exit after the `exit` frame | `1000` |
| `client_closed` | Browser sends `1000` or `1001` | Preserve the client close |
| `idle_timeout` | No payload activity before the idle limit | `1008`, `idle timeout` |
| `max_duration` | Claimed access expires | `1008`, `session expired` |
| `message_too_big` | WebSocket message exceeds the configured limit | `1009` |
| `invalid_terminal_frame` | Invalid exec/serial frame | sanitized `INVALID_TERMINAL_FRAME`, then `1011` |
| `invalid_vnc_frame` | Non-binary VNC frame | `1011` |
| `backpressure` | A bounded input/output queue is full | sanitized `BACKPRESSURE_LIMIT`, then `1011` |
| `runtime_unavailable` | Runtime stream cannot be opened | sanitized `RUNTIME_UNAVAILABLE`, then `1011` |
| `runtime_failed` | An opened runtime stream fails | sanitized `RUNTIME_STREAM_FAILED`, then `1011` |
| `transport_failed` | WebSocket read/write fails unexpectedly | best-effort `1011` |
| `shutdown` | Server context is canceled | `1001`, `server shutdown` |
| `invalid_start` | Internal caller violates the accepted-connection contract | best-effort `1011` |

`ani_session_bytes_total` counts only runtime payload bytes: `stdin.data`, `stdout.data`, and `stderr.data` for terminal sessions, and raw RFB bytes for VNC. JSON envelopes, close frames, pings, and protocol metadata are excluded.
