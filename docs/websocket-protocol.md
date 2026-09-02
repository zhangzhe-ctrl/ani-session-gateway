# WebSocket protocol

Realtime sessions connect to:

```text
GET /api/v1/realtime/sessions/{session_id}?ticket=<one-time-ticket>
```

The server validates the configured `Origin` and atomically claims the ticket before upgrading. URLs, query strings, tickets, credentials, and stream payloads must never be logged.

## Exec and serial

Clients send UTF-8 JSON text frames:

```json
{"type":"stdin","data":"printf ani-terminal-ok\\r"}
{"type":"resize","rows":30,"cols":120}
```

The server sends `stdout`, `stderr`, `exit`, and sanitized `error` JSON text frames. `stdin.data` is limited to 64 KiB. Rows and columns must be positive and no greater than 4096.

The required WebSocket subprotocol is `ani.terminal.v1`.

## VNC

VNC uses the `binary` WebSocket message type as a transparent RFB byte stream. It is never JSON-encoded or base64-encoded. The required WebSocket subprotocol is `ani.vnc.v1`.
