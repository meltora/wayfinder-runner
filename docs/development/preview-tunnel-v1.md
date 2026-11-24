# Preview tunnel protocol v1

Frozen contract between the runner and the Meltora runner gateway for P5e (D-P5-5, ADR-0050). The runner implements one side, the TypeScript gateway the other. Shared frame vectors live in `api/runner/v1/testdata/preview_tunnel_v1_frames.json`.

## T1 Transport

- The runner opens a websocket to `/v1/runners/preview-tunnel` on the existing mTLS gateway listener, with no query string. The protocol version is the `/v1` in the path.
- Authentication: the same client certificate as the control stream (`/v1/runners/stream`), with the same certificate-superseded rules.
- The gateway accepts a tunnel only while the same runner has a live control session with the same certificate fingerprint. When that control session ends or its certificate is superseded, the gateway closes the tunnel.
- The gateway accepts the upgrade only if the runner's current verified capability manifest advertises `preview_tunnel_transport_version: 1`. Otherwise it answers HTTP 403 and no websocket opens. When a newer manifest drops the field, the gateway closes any open tunnel with 1000. The runner treats a 403 like any other failed dial and retries with backoff.
- One tunnel per runner. When a new tunnel opens, the gateway closes the old one with close code 1000.
- The runner opens the tunnel only while it advertises `preview_tunnel_transport_version: 1` and its control stream is connected. It reconnects with jittered exponential backoff (REQ-E7), the same policy as the control client.
- Only binary messages are allowed. A text message closes the tunnel with 1003.
- Liveness: each side sends a websocket ping every 20 s. If no pong arrives within 60 s, the tunnel is closed.

## T2 Framing

Each websocket binary message carries exactly one frame:

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | type |
| 1 | 4 | stream id, uint32 big-endian |
| 5 | 0..65536 | payload |

A message shorter than 5 bytes or longer than 65541 bytes is a protocol error.

| Type | Name | Direction | Payload |
|---|---|---|---|
| 1 | OPEN | gateway → runner | strict JSON `{"preview_id":"…","preview_key":"…"}`, 1..1024 bytes of valid UTF-8 that parse as one JSON object with exactly these two keys, no duplicates, both values non-empty strings |
| 2 | DATA | both | 1..65536 bytes |
| 3 | END | both | empty: the sender will write no more on this stream |
| 4 | RESET | both | exactly 1 byte, the reset code |
| 5 | WINDOW | both | exactly 4 bytes, uint32 big-endian credit, 1..2^31−1 |

Reset codes: 1 `not_found`, 2 `refused`, 3 `limit`, 4 `upstream_error`, 5 `idle`, 6 `protocol`. A receiver treats an unknown reset code as `protocol`.

### Stream ids

- Only the gateway opens streams. Ids are odd and strictly increasing within one tunnel, starting at any odd value ≥ 1. Stream id 0 is never valid.
- A stream is closed once both sides have sent END, or once either side has sent or received RESET. A closed stream id is never reused.

### Flow control

- Each stream starts with a 256 KiB (262144-byte) send window in each direction.
- Each DATA frame uses up its payload length from the sender's window. A sender never sends more than its window.
- The receiver grants more credit with WINDOW frames as it consumes data.
- If a peer sends DATA beyond the window it was granted, the receiver sends RESET `protocol` for that stream.
- Per-tunnel buffer cap: the cap governs additional credit only. While a side holds 16 MiB (16777216 bytes) or more of received, not yet consumed DATA across the tunnel, it sends no WINDOW frames. The hard worst case per tunnel is the sum of initial windows, 256 × 256 KiB = 64 MiB.

### Errors

Tunnel protocol errors close the whole tunnel with close code 1002:
- a frame that is too short or too long;
- an unknown frame type;
- any frame with an even stream id or stream id 0;
- an OPEN sent by the runner, an OPEN with a non-increasing id, or an OPEN with an invalid payload: bytes that are not valid UTF-8 (a BOM counts as a byte before the object, so it is rejected), not exactly one JSON object surrounded only by JSON whitespace (space, tab, CR, LF), a duplicate key after unescaping, a missing or extra key, an empty or non-string value, or a string escape that leaves a lone UTF-16 surrogate. The 1024-byte limit counts raw payload bytes.
- a DATA payload of 0 bytes, or an END, RESET or WINDOW with the wrong payload length;
- a WINDOW credit of 0, or a WINDOW grant that would push the receiving side's send window for that stream above 2^31−1;
- a second END on the same stream.

Stream-level errors reset only that stream, with RESET `protocol`:
- DATA after the sender's own END;
- DATA beyond the granted window.

Races:
- DATA, END, WINDOW or RESET for an odd, already closed stream id at or below the highest opened id is dropped silently.
- A RESET with an unknown code closes that stream as `protocol`; the tunnel stays open.
- The same frames for an id above the highest opened id are a protocol error (1002).

## T3 Stream semantics

- On OPEN, the runner checks the hosted preview registry. It continues only if `preview_id` names a hosted preview this runner owns, its `preview_key` matches, its state is deployed and it has not expired. Otherwise it sends RESET `not_found`.
- The local target is taken only from the runner's own registry entry for that preview_id: the 127.0.0.1 host port that maps to port 8080 of the preview's `<prefix>-gateway` container. Nothing in the OPEN payload chooses the address.
- The runner dials the target and copies bytes in both directions. It does not parse or rewrite HTTP. If the dial fails, it sends RESET `upstream_error`.
- END is propagated as a TCP half-close. When the upstream connection closes, the runner sends END. On an upstream read or write error, it sends RESET `upstream_error`.
- The gateway enforces REQ-E5 by routing a key only to the deploying runner's tunnel. The runner check above is defence in depth.
- The edge strips the one-time token, the `__Host-` session cookie and all Meltora auth headers before it writes the request to a stream (T6). Nothing token-related crosses the tunnel.

## T4 Runner limits

- At most 64 open streams per preview and 256 per tunnel. An OPEN above either limit gets RESET `limit`.
- A stream with no DATA in either direction for 5 minutes gets RESET `idle`. An open websocket (HMR) stream stays alive as long as DATA flows in either direction.
- The edge enforces the 25 MB request body cap and the rate limits.
- When a preview is destroyed or expires, the runner sends RESET `not_found` on all of its streams.

## T5 Capability

- The signed manifest field `preview_tunnel_transport_version: 1` sits immediately after `preview_hosting`, with omitempty.
- The runner publishes it only when `preview_hosting` is published and the tunnel is enabled.
- `preview_hosting` itself is unchanged.

## T6 Tokens

The runner never sees one-time tokens, cookies or the session hash. Issuing and redeeming them is on the app and edge side.
