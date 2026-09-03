# SG-CONNECTED-SESSION-ARCH-20260903

## Scope

- Baseline: `86a0870c7da5c9a354f744bda2c0fe25c4be4548`
- Goal: deepen the post-upgrade WebSocket lifecycle into one Connected Session module without changing the API, runtime ports, store contract, dependencies, or deployment manifests.
- Authority: local worktree only. No commit, push, tag, image publication, deployment, cluster access, or live dependency validation.

## Contract decisions implemented

- A Connected Session starts after ticket claim and WebSocket acceptance. It becomes active only after the runtime stream opens.
- `session.Manager.Claim` returns a sanitized `ClaimedAccess`; ticket, idempotency, and persistence state remain behind Manager/Store.
- `websockettransport.Handler` owns only Origin/subprotocol/ticket admission, claim, upgrade, and pre-upgrade lease cleanup.
- One concrete `connectedsession.Module.Run` owns exec, serial, and VNC runtime open, pumps, timers, bounded queues, wire close, runtime close, lease release, and final observation.
- The first terminal signal freezes one bounded outcome. Later signals cannot overwrite it or emit a second finish.
- Serial invalid frames use `INVALID_TERMINAL_FRAME`, message `invalid terminal frame`, and WebSocket status `1011`.
- Payload metrics count terminal data fields and raw RFB bytes, excluding JSON envelopes and protocol frames.
- Runtime-open failures emit one failed observation without connected/closed gauge transitions. Connected sessions emit one connected and one finish.

## TDD evidence

| Slice | Red evidence | Green evidence |
|---|---|---|
| Claimed access projection | Manager test failed to compile because `SessionID`, `LeaseID`, `Mode`, `Target`, and `Exec` did not exist | `go test ./internal/session -run TestManagerIdempotencyEncryptionAndClaim -count=1` |
| Serial invalid frame | Package had no non-test implementation | `TestSerialInvalidFrameEndsOnceAsProtocolFailure` passed with sanitized error, `1011`, one connected, and one finish |
| Serial payload | New test initially had no `client_closed` outcome/lifecycle bridge | `TestSerialCarriesPayloadAndCountsRuntimeBytes` passed with 4 input and 4 output runtime bytes |
| Exec lifecycle | Test timed out waiting for the module to become connected | `TestExecCarriesTerminalFramesAndEndsNormally` passed for resize, stdin/stdout, exit, and normal close |
| Observability | Test failed to compile because `NewConnectedSessionObserver` did not exist | Scrape test passed for active gauge, bytes, bounded end outcome, and runtime-only errors |
| Close ordering | Full race test observed EOF instead of the max-duration `1008` close frame | Final frame is sent after outcome freeze and before pump cancellation; max-duration race test passed 10 consecutive runs |

## Replaced shallow coverage

- Removed Handler-owned `bridge_exec.go`, `bridge_byte.go`, and `exec_protocol.go`.
- Removed their implementation-coupled decoder/bridge tests.
- Replacement tests observe the public Connected Session seam across exec, serial, VNC, runtime-open failure, manual-clock limit races, bytes, outcomes, and exactly-once observation.
- Handler retains an admission contract test proving a post-claim mode mismatch releases capacity without opening the runtime or Connected Session.

## Verification

Executed with task-owned Go temporary/cache directories under ignored `.bin/` because the default `/tmp` quota and sandbox loopback restrictions are environmental constraints.

```text
go test ./internal/transport/websocket/connectedsession -count=5
PASS

go test -race ./internal/transport/websocket -run 'TestIdleTimeoutAndMessageLimit/max_duration' -count=10
PASS

make check
PASS: buf lint
PASS: API OpenAPI tests
PASS: generated drift check
PASS: manifest checks
PASS: root and API tests
PASS: full root race tests
PASS: root and API vet
PASS: session-gateway build
PASS: git diff --check
```

`go.mod`, `go.sum`, `api/`, `internal/runtime/`, `internal/store/`, and `deploy/` are unchanged.

## Not verified

- No live Redis, Kubernetes, KubeVirt, browser, guest login, VNC render/input, rollout, or restart-persistence check was performed in this work package.
- The current worktree is not committed, pushed, published, or deployed.

## Follow-up

The historical state above was superseded later on 2026-09-03: implementation commit `58393cd` was merged into `main` by `d0353b8`, pushed, and deployed. See `SG-CONNECTED-SESSION-DEPLOY-20260903.md` for the distinct publication and live smoke evidence.
