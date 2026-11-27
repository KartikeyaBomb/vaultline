# Vaultline

A fault-tolerant, deduplicating snapshot store written in Go.

- **Content-defined chunking** (FastCDC) and **incremental snapshots**, so only changed data is read and sent
- **Raft-replicated metadata** (hashicorp/raft) and **3-way chunk replication** using rendezvous hashing
- **Self-healing:** anti-entropy re-replication, a bitrot scrubber, and chaos-tested against node crashes, network partitions, and chunk corruption
- **Observability:** Prometheus metrics, OpenTelemetry traces, and a Grafana dashboard
- **Cold tiering** to S3 in pack files, with a projected cost model

> Status: under active development. See the [design doc](docs/DESIGN.md) and the [issues](../../issues) for the build plan.

## Roadmap

| # | Feature | Phase |
|---|---|---|
| F1 | Project scaffold + local content-addressed chunk store | Single-node core |
| F2 | FastCDC content-defined chunker | Single-node core |
| F3 | Snapshot trees + single-node backup/restore | Single-node core |
| F4 | Incremental snapshots + dedup stats | Single-node core |
| F5 | gRPC chunk service + node binary | Distributed store |
| F6 | Raft-replicated metadata (FSM, catalog, chunk index) | Distributed store |
| F7 | Rendezvous placement + 3-way replication | Distributed store |
| F8 | Failure detection, anti-entropy repair, scrubber | Distributed store |
| F9 | Fault-injection network + chaos test suite | Hardening |
| F10 | Retention + garbage collection | Hardening |
| F11 | Prometheus + OpenTelemetry + docker-compose cluster | Ops & tiering |
| F12 | S3 cold tiering with pack files | Ops & tiering |
| F13 | Benchmark harness + cost model | Proof |
| F14 | Docs, demo, and v0.1.0 release | Proof |

## Results

The benchmark results will be published in `docs/BENCHMARKS.md` once F13 is done.
