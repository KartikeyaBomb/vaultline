# Vaultline — Design

Vaultline is a fault-tolerant, deduplicating snapshot store. A client backs up a directory tree as a series of snapshots. The cluster stores the data as content-addressed chunks, replicated three ways across nodes, and the metadata is replicated with Raft. Cold chunks are moved to S3.

## 1. Goals and non-goals

**Goals**

- Back up a directory as an immutable snapshot and restore any snapshot byte-for-byte.
- Content-defined chunking (FastCDC), so edits that insert or shift bytes only change nearby chunks.
- Incremental snapshots: unchanged files are not re-read, and chunks the cluster already has are not re-sent.
- 5-node cluster: metadata replicated with Raft, each chunk stored on 3 nodes.
- Survive and repair after: a node crash, a network partition, a corrupted chunk on disk, and loss of the Raft leader.
- Prometheus metrics, OpenTelemetry traces, and a Grafana dashboard.
- Move cold chunks to S3 (MinIO locally) in pack files, and keep restores working.
- A repeatable benchmark that produces the headline numbers.

**Non-goals (v0.1)**

- Encryption at rest or client-side encryption (noted as future work).
- Multi-tenant auth. Mutual TLS between nodes is optional.
- Erasure coding (3x replication only).
- Windows support.

## 2. Target numbers and how each is measured

The numbers in the project pitch are targets. The README reports whatever `bench/` actually measures.

| Target | Definition | Measured by |
|---|---|---|
| ~12:1 dedup across 30 daily snapshots of a 20 GB dataset | (sum of logical bytes across all 30 snapshots) ÷ (bytes of unique chunks before compression). Compression is reported separately so it doesn't inflate the ratio. | `bench/run30` with `bench/datagen` churn model |
| Restore 20 GB in < 60 s after losing 1 of 5 nodes | Wall-clock time from `vaultline restore` start to a verified tree (all file hashes match), with one node container killed before the restore starts | `bench/restore-degraded` |
| ~65% lower projected storage cost under an 80%-cold model | Cost per month of all-hot 3x replicated storage vs. 20% hot 3x + 80% in S3, with prices taken from config | `bench/costmodel` |
| Recovery under crash, partition, corruption | Chaos tests pass: no acknowledged snapshot is lost, all restores verify, and the cluster returns to 3 replicas per chunk | `internal/testcluster` chaos tests + `deploy/chaos.sh` |

**Disk budget warning:** a full 20 GB × 30-day run with 3x replication needs about 100–150 GB of free disk. `bench` takes a `--scale` flag. Use `--scale 0.1` (2 GB) while developing, and run the full 20 GB once on a cloud VM or an external disk.

## 3. Architecture

```
                 ┌────────────────────────── vaultline CLI (client) ───────────────────────────┐
                 │  walk tree → FastCDC chunk → BLAKE3 hash → HasChunks? → PutChunk ×3 → Commit │
                 └──────────┬────────────────────────────┬──────────────────────────┬──────────┘
                            │ gRPC (chunks)              │ gRPC (chunks)            │ gRPC (meta, to any node;
                            ▼                            ▼                          ▼  forwarded to leader)
   ┌───────────────┐  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐
   │ node-1        │  │ node-2        │  │ node-3        │  │ node-4        │  │ node-5        │
   │ ┌───────────┐ │  │ ┌───────────┐ │  │ ┌───────────┐ │  │ ┌───────────┐ │  │ ┌───────────┐ │
   │ │ Raft + FSM│◄┼──┼►│ Raft + FSM│◄┼──┼►│ Raft + FSM│◄┼──┼►│ Raft + FSM│◄┼──┼►│ Raft + FSM│ │
   │ │ (bbolt)   │ │  │ │ (leader)  │ │  │ │           │ │  │ │           │ │  │ │           │ │
   │ └───────────┘ │  │ └───────────┘ │  │ └───────────┘ │  │ └───────────┘ │  │ └───────────┘ │
   │ chunkstore    │  │ chunkstore    │  │ chunkstore    │  │ chunkstore    │  │ chunkstore    │
   │ repair/scrub  │  │ repair/scrub  │  │ repair/scrub  │  │ repair/scrub  │  │ repair/scrub  │
   │ /metrics      │  │ tierer, GC    │  │ /metrics      │  │ /metrics      │  │ /metrics      │
   └───────┬───────┘  └───────┬───────┘  └───────────────┘  └───────────────┘  └───────────────┘
           │                  │ cold packs (PUT / range GET)
           ▼                  ▼
        ┌─────────────────────────┐      ┌────────────┐ ┌─────────┐ ┌────────┐
        │ S3 / MinIO (cold tier)  │      │ Prometheus │ │ Grafana │ │ Jaeger │
        └─────────────────────────┘      └────────────┘ └─────────┘ └────────┘
```

Every node runs the same binary (`vaultline node`) and has two roles:

1. **Metadata member.** It is one of 5 Raft voters (hashicorp/raft). The finite state machine (FSM) is stored in bbolt.
2. **Chunk server.** It stores chunks on local disk in a content-addressed layout.

Only the current Raft leader runs the cluster-wide jobs (GC, tiering, committing membership changes). Every node runs its own local jobs (anti-entropy repair, scrubbing).

## 4. Data model

| Object | Identity | Contents | Where it lives |
|---|---|---|---|
| **Chunk** | `BLAKE3-256(plaintext)` | 16–256 KiB of file data, stored zstd-compressed | Chunk servers (3 replicas) or an S3 pack |
| **Tree node** | hash of its serialized bytes | Directory entries: name, mode, size, mtime, and either a list of chunk hashes (file) or a child tree hash (dir) | Stored as a chunk, so it is replicated like data |
| **Snapshot record** | snapshot ID (ULID) | dataset name, root tree hash, parent snapshot ID, created_at, logical bytes, new bytes, file count | Raft FSM |
| **Chunk index entry** | chunk hash | size, refcount, state (`live` / `tombstoned`), tier (`hot` / `cold`), pack location if cold | Raft FSM |
| **Membership** | node ID | address, state (`up` / `suspect` / `down`), last heartbeat | Raft FSM, versioned by `epoch` |

**Why manifests are not stored in Raft:** a 20 GB dataset at a 64 KiB average chunk size is about 330K chunks. Thirty full manifests in the FSM would mean hundreds of MB of Raft state. Tree nodes are stored as ordinary content-addressed chunks instead, so unchanged subtrees dedup between snapshots for free, and Raft only stores one root hash per snapshot.

Tree nodes are serialized with protobuf in a deterministic order (entries sorted by name), so identical directories always produce the same hash.

## 5. Chunking

FastCDC with a gear rolling hash and normalized chunking:

- min 16 KiB, average 64 KiB, max 256 KiB (configurable)
- Implemented in-house in `internal/chunker` (about 150 lines). Tests cover: determinism; that inserting one byte changes only about 1–2 chunks; that chunk sizes follow the expected distribution; and that no chunk falls outside min/max.

Expected tradeoff: a smaller average chunk size gives better dedup but more index entries and more RPC overhead. 64 KiB is the starting point, and `bench` sweeps 16, 32, 64 and 128 KiB.

## 6. Write path (backup)

1. **Load parent.** If the dataset has a previous snapshot, the client fetches its tree. It keeps a local cache at `~/.cache/vaultline/<dataset>.idx` that maps path → (size, mtime, inode, chunk list).
2. **Walk.** For each file: if size, mtime and inode match the parent entry, reuse its chunk list without reading the file. Otherwise, stream the file through FastCDC and hash each chunk.
3. **Dedup check.** Batch hashes, 4,096 per call, into `HasChunks`, sent to any node, which answers from its FSM replica. Only missing chunks are uploaded.
4. **Upload.** The client gets membership (with its epoch) from the cluster, computes placement for each chunk (§8), and calls `PutChunk` on all 3 replicas in parallel. An upload succeeds when **2 of 3** acknowledge (W=2). Anti-entropy fills in the third replica later. Uploads use a bounded worker pool (default 32 in flight).
5. **Trees.** Tree nodes are built bottom-up and uploaded like any other chunk.
6. **Commit.** The client sends `CommitSnapshot{dataset, root, parent, new_chunks[], stats}` to any node, which forwards it to the leader. The leader:
   - checks that each new chunk exists on at least 2 placement nodes (batched `HasChunks` to the replicas),
   - proposes a Raft command. The FSM increments refcounts and rejects the commit if any referenced chunk is `tombstoned` (see GC, §10). Large commits are split into pages of 10K chunks, plus a final `Seal`.
7. The snapshot exists only after `Seal` is applied. A client that crashes before that leaves orphan chunks, and GC removes them after a grace period.

## 7. Read path (restore)

1. Read the snapshot record (root hash) from any node.
2. Walk the tree breadth-first, fetching tree chunks.
3. Fetch file chunks with a worker pool (default 64 in flight). For each chunk, try its placement nodes in order. On a timeout, an error or a hash mismatch, try the next replica, and report the bad replica so it gets repaired.
4. If a chunk is `cold`, do a ranged GET from its S3 pack.
5. Write each file with `pwrite` at its chunk offsets, set mode and mtime, then verify the file's hash against the tree.

To restore 20 GB in under 60 s, the client needs about 340 MB/s, so reads are spread across all 4 surviving nodes. Placement spreads chunks evenly, so losing one node removes about 1/5 of the read bandwidth and makes no data unavailable.

## 8. Placement and replication

- **Rendezvous (highest-random-weight) hashing:** for chunk `h`, score each `up` node with `hash(h ‖ nodeID)` and take the top 3. Placement depends only on the chunk hash and the committed membership, so **chunk locations are not stored in Raft.**
- When membership changes (a node goes `down` or joins), only chunks whose top 3 included that node move, which is about 3/N of the data.
- Every RPC carries `membership_epoch`. A node with a newer epoch tells the caller to refresh.

## 9. Failure handling

| Failure | Detection | Behaviour | Repair |
|---|---|---|---|
| **Chunk server crash** | Nodes send the leader a heartbeat every 1 s. The node is `suspect` after 5 s and `down` after 30 s (committed to Raft) | Writes succeed with W=2. Reads fall through to other replicas | Once it is `down`, placement changes. Each survivor's **anti-entropy** loop walks its local chunks, computes the new top 3, and pushes chunks to nodes that are missing them (batched `HasChunks`, then `PutChunk`). Gauge: `under_replicated_chunks` |
| **Node returns** | Heartbeat resumes, and it is marked `up` | Placement includes it again | Anti-entropy moves chunks back. Extra replicas are deleted once 3 confirmed copies exist elsewhere |
| **Raft leader crash** | Raft election (about 1–2 s) | Commits stall briefly. Clients retry on `ErrNotLeader` / `ErrLeadershipLost` | Leader-only jobs (GC, tierer) restart on the new leader. They are idempotent and driven by FSM state |
| **Network partition (3 \| 2)** | Raft | The majority side keeps committing. The minority side can't commit snapshots or change membership, but it still serves chunk reads | Once the partition heals, Raft catches up and anti-entropy fixes any replica gaps. Orphan chunks are left for GC |
| **Chunk corruption (bitrot)** | Hash check on every read, plus a background **scrubber** that re-hashes about N MB/s of local chunks | The read fails over to another replica | The corrupt file is moved to `quarantine/` and a good copy is fetched from a peer. Counter: `scrub_corruptions_total` |
| **Disk full** | `PutChunk` returns `RESOURCE_EXHAUSTED` | The client picks the next node in rendezvous order (a "sloppy" replica) | Anti-entropy later moves the chunk to its correct placement |

**Invariant tested in chaos runs:** every snapshot that reached `Seal` restores and verifies, and the cluster gets back to 3 healthy replicas of every live chunk within a bounded time.

## 10. Garbage collection

- **Retention:** `vaultline forget --keep-daily 30` deletes snapshot records through Raft.
- **Decrement refcounts:** the leader walks the deleted snapshot's tree and proposes `DecRef` batches. Unchanged subtrees shared with other snapshots are counted once per snapshot root, so shared subtrees aren't double-counted.
- **Tombstone:** chunks with refcount 0 for longer than the grace period (default 1 h) are marked `tombstoned` in the FSM. From then on, any commit that references them is rejected. This closes the race where `HasChunks` said "exists", so the client skipped the upload, and GC then deleted the chunk.
- **Sweep:** each node deletes its local copies of tombstoned chunks, and the leader then removes their index entries.
- **Orphans** (uploaded but never committed): anti-entropy reports chunks that aren't in the index, and they are deleted after the grace period.

## 11. Cold tiering (S3)

- **Cold policy:** a chunk is cold if no snapshot from the last K days (default 7) references it. This is computed by the leader from the catalog and the refcount history.
- **Packing:** the tierer groups cold chunks into **pack files** of about 64 MB to cut S3 PUT costs and object count. A node that holds the chunks builds the pack and uploads it with `PUT s3://bucket/packs/<packID>`, along with a pack index (hash → offset, length).
- **Commit:** `TierMove{pack, entries[]}` through Raft sets `tier=cold` and records each chunk's location in its pack. Only after that commit do nodes delete their local replicas. S3's own durability replaces 3x replication for cold data.
- **Read:** an S3 range GET at the chunk's offset and length. An optional local LRU cache (`tiercache/`) keeps recently recalled chunks.
- **Rehydrate:** if new snapshots start referencing a cold chunk again, it isn't copied back unless it's read often. A configurable threshold controls this.
- **Pack compaction:** when more than 50% of a pack's chunks are dead, its live chunks are rewritten into a new pack. Stretch goal.
- **Cost model:** `bench/costmodel` takes the hot price per GB-month (e.g. EBS gp3), the S3 class price, request prices and the hot/cold split, and prints the projected monthly cost of all-hot vs. tiered storage. All prices are config inputs; none are hard-coded claims.

## 12. Observability

**Prometheus** (each node exposes `/metrics`):

- `vaultline_chunk_put_total{result}`, `vaultline_chunk_put_bytes_total`, `vaultline_chunk_put_duration_seconds` (histogram)
- `vaultline_chunk_get_total{result,source="local|replica|s3|cache"}`, `vaultline_chunk_get_duration_seconds`
- `vaultline_under_replicated_chunks`, `vaultline_repair_bytes_total`, `vaultline_scrub_corruptions_total`
- `vaultline_stored_bytes{tier}`, `vaultline_logical_bytes_total`, `vaultline_dedup_ratio`
- `vaultline_membership{state}`, plus Raft internals (`raft_*`, `raft_leader`, commit latency) bridged from go-metrics
- `vaultline_gc_swept_chunks_total`, `vaultline_tier_moved_bytes_total`

**OpenTelemetry tracing:** spans for `backup` → `chunk.upload` → `rpc.PutChunk` (on each replica) and `restore` → `chunk.fetch`. Trace context is passed in gRPC metadata and exported over OTLP to Jaeger.

**Grafana:** a provisioned dashboard showing cluster health, throughput, replication state, Raft, and tiering.

## 13. Testing strategy

- **Unit tests:** chunker properties, chunkstore durability (fsync, atomic rename), tree serialization, placement distribution, FSM commands (apply, then snapshot and restore the FSM).
- **`internal/faultnet`:** a fake network that wraps both the gRPC dialer (`grpc.WithContextDialer`) and Raft's `StreamLayer`. It can `Partition(a, b)`, `Isolate(n)`, add latency, and drop a percentage of traffic.
- **`internal/testcluster`:** starts N real nodes in one process (temp dirs, faultnet transport) and provides helpers such as `Kill(n)`, `Restart(n)`, `CorruptChunk(n, h)`, `WaitReplicated()`.
- **Chaos tests (`go test -tags chaos`):** kill a node mid-backup; kill the leader mid-commit; 3|2 partition during a backup and then heal; corrupt random chunks and restore; kill one node and restore. All run under `-race`.
- **Docker chaos:** `deploy/chaos.sh` uses `docker kill`, `docker network disconnect`, and byte-flipping in volumes against the compose cluster.

## 14. Repo layout

```
cmd/vaultline/          CLI: node, backup, restore, snapshots, forget, admin
proto/                  gRPC + protobuf definitions (buf)
internal/
  hash/                 BLAKE3 chunk IDs
  chunker/              FastCDC
  chunkstore/           local content-addressed store, verify on read, quarantine
  tree/                 tree nodes, deterministic encoding
  client/               backup/restore engine, parent index cache, worker pools
  rpc/                  gRPC server/client glue, epoch checks
  meta/                 Raft setup, FSM (bbolt), commands, leader forwarding
  placement/            rendezvous hashing
  repair/               anti-entropy + scrubber
  gc/                   refcount, tombstone, sweep
  tier/                 cold policy, packer, S3 client, read-through cache
  obs/                  metrics registry, OTel setup
  faultnet/             fault-injecting network
  testcluster/          in-process cluster harness + chaos tests
deploy/                 docker-compose (5 nodes, MinIO, Prometheus, Grafana, Jaeger), chaos.sh
bench/                  datagen, run30, restore-degraded, costmodel
docs/                   DESIGN.md, BENCHMARKS.md
```

## 15. Dependencies

| Concern | Choice |
|---|---|
| Consensus | `github.com/hashicorp/raft` + `raft-boltdb/v2` |
| FSM storage | `go.etcd.io/bbolt` |
| RPC | gRPC + protobuf, generated with `buf` |
| Hashing | `github.com/zeebo/blake3` |
| Compression | `github.com/klauspost/compress/zstd` |
| CLI | `github.com/spf13/cobra` |
| Metrics | `prometheus/client_golang` |
| Tracing | `go.opentelemetry.io/otel` + OTLP exporter |
| S3 | `aws-sdk-go-v2/service/s3` (pointed at MinIO locally) |
| Logging | `log/slog` |

## 16. Risks and open questions

- **Disk space for the full benchmark** (see §2). Develop at `--scale 0.1`.
- **One file per chunk** on the local store means about 500K+ inodes per node at full scale. That's acceptable for v0.1. Local pack files are the fallback if `ls`/fsync overhead becomes a bottleneck.
- **FSM size:** the chunk index of about 500K entries in bbolt is fine. Raft snapshots of the FSM use bbolt's `Tx.WriteTo`.
- **Leader bottleneck on commit verification:** batched `HasChunks` should handle it. If not, the client can attach replica acks to the commit instead.
- **Synthetic churn model:** the dedup ratio depends heavily on how the generator mutates data. The model (files touched per day, edit types, insert/append/delete mix) is documented in BENCHMARKS.md so the number can be reproduced and isn't tuned to hit a target.
