# Implementation Ideas

A curated list of potential features and improvements for lplex, organized by category.

## Data Decoding & Interpretation

1. **PGN decoder library** — Decode raw NMEA 2000 payloads into human-readable fields (GPS position from PGN 129025, speed from 128259, wind from 130306, etc.). Currently the system stores and streams raw hex bytes; decoded values would make the data immediately useful to clients.

2. **Decoded values endpoint** (`GET /values/decoded`) — Serve last-known values as named fields (latitude, longitude, SOG, COG, depth, wind speed/angle, water temp) instead of raw hex, so web dashboards don't need their own PGN decoders.

3. **SignalK bridge** — Translate NMEA 2000 frames into [SignalK](https://signalk.org) format, the open standard for boat data. This would let lplex interoperate with the large SignalK ecosystem of apps and dashboards.

## Monitoring & Observability

4. **Prometheus metrics endpoint** (`GET /metrics`) — Expose frame throughput, ring buffer utilization, consumer lag, active sessions, device count, journal size, replication latency, and backfill progress as Prometheus metrics.

5. **Health check endpoint** (`GET /health` or `GET /healthz`) — Return structured health status for the boat-side server: CAN interface state, frame rate, last frame timestamp, broker status, replication connection status. Useful for systemd watchdog integration and load balancer probes. (Cloud already has `/healthz`.)

6. **Bus silence alerting** — Detect when no frames have been received for a configurable duration and log/emit an alert. Indicates CAN bus disconnection or power issues on the boat.

7. **CAN bus statistics** — Track per-PGN frame rates, bus utilization percentage, and error frame counts. Surface via API and logs.

## Web & Client

8. **Embedded web dashboard** — Bundle a lightweight web UI (served from the binary via `embed`) showing live device table, frame rate, value gauges, and replication status. Eliminates the need for a separate frontend deployment.

9. **WebSocket transport** — Add a `GET /ws` endpoint as an alternative to SSE for bidirectional communication and better mobile browser support.

10. **REST API for journal replay** — `GET /journal/frames?from=<seq>&to=<seq>` to query historical frames without SSE, useful for batch analysis and charting.

## Data Export & Integration

11. **NMEA 0183 TCP output** — Translate common PGNs into NMEA 0183 sentences and serve over a TCP socket, enabling integration with legacy chart plotters, OpenCPN, and other tools that speak 0183.

12. **InfluxDB / TimescaleDB writer** — Optional sink that writes decoded values to a time-series database for long-term analysis and Grafana dashboards.

13. **Webhook notifications** — Fire HTTP webhooks on configurable events (device appeared/disappeared, anchor alarm via GPS geofence, engine hours threshold, etc.).

14. **Journal export to Parquet/CSV** — CLI tool or endpoint to convert `.lpj` journal files into Parquet or CSV for analysis in pandas, DuckDB, or spreadsheets.

## Networking & Security

15. **Authentication for HTTP API** — API key or JWT-based auth for the HTTP endpoints. Currently the API is wide open with permissive CORS (`*`), which is fine on a boat LAN but risky when exposed to the internet or via cloud.

16. **Rate limiting** — Per-client rate limiting on `/send` to prevent accidental CAN bus flooding from misbehaving clients.

17. **TLS for HTTP server** — Support HTTPS on the boat-side HTTP server, not just the cloud gRPC connection.

## Cloud & Multi-Instance

18. **Cloud dashboard web app** — A multi-instance web frontend for `lplex-cloud` showing fleet overview, per-boat status, device tables, and live data.

19. **Instance lifecycle management** — Auto-pause/archive instances that haven't connected in N days, with ability to resume on reconnect. Track connection history and uptime stats.

20. **Cloud-to-cloud replication** — Allow `lplex-cloud` instances to replicate to each other for geographic redundancy.

21. **Push notifications from cloud** — Mobile push notifications (via FCM/APNs) for configurable events: boat disconnected, anchor drag, bilge pump activation, battery voltage low.

## CAN Bus & Protocol

22. **NMEA 2000 command helpers** — Higher-level API for common bus operations: request all product info, request configuration, address claim, etc., beyond the current raw `/send`.

23. **Virtual CAN support for development** — Built-in `vcan` test mode that replays journal files into the broker, making it easy to develop and test without a physical CAN bus.

24. **Multi-interface support** — Read from multiple CAN interfaces simultaneously (e.g., `can0` + `can1` for boats with separate engine and nav buses).

## Developer Experience

25. **Journal inspection CLI** (`lplexjournal`) — Standalone tool to inspect `.lpj` files: dump block metadata, list devices, search for specific PGNs/sequences, verify CRC integrity, show compression stats. (lplexdump has `-inspect` mode, but a dedicated tool could do more.)

26. **Replay mode** — `lplex --replay journal.lpj` to replay a journal file through the broker at real-time or accelerated speed, re-serving it to SSE clients. Great for demos and debugging.

27. **Go client library improvements** — Add auto-reconnect, connection pooling, and a higher-level `Watch(pgn)` API to the `lplexc` package that returns a channel of typed values.

28. **Integration test harness** — End-to-end test that spins up boat `lplex` + cloud `lplex-cloud`, replicates data, and verifies round-trip through SSE on the cloud side.

## API Enhancements

29. **DELETE /clients/{id}** — Explicitly drop a session instead of relying solely on timeout-based cleanup.

30. **GET /clients/{id}** — Session introspection endpoint to check cursor position, filter, timeout, and last activity without side effects.

31. **Batch send** — Accept an array of frames in `POST /send` for atomic multi-frame transmission.

32. **ISO 8601 duration: month/year support** — The current duration parser only handles `PT` (hours/minutes/seconds) and `P` (days/weeks). Adding `P1M`/`P1Y` would allow more natural retention policies.

## Suggested Starting Points

Quick wins (small scope, high value):
- Health check endpoint (#5)
- DELETE /clients/{id} (#29)
- Session introspection (#30)
- Bus silence alerting (#6)

Medium effort, high impact:
- Prometheus metrics (#4)
- PGN decoder library (#1)
- Replay mode (#26)
- Embedded web dashboard (#8)

Larger projects:
- SignalK bridge (#3)
- Cloud dashboard (#18)
- InfluxDB writer (#12)
- Webhook notifications (#13)
