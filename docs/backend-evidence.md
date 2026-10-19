# Backend and toolchain evidence

These are bounded observations from official documentation checked on 2026-10-20, plus explicitly labeled design inferences. They are not live integration results. Remote freshness after that check is unknown. No credentials or cloud resources were accessed.

## UUIDv7 ordering

Observed: UUIDv7 places a millisecond Unix timestamp in its high bits. RFC 9562 specifies byte-wise sorting and discusses optional monotonicity mechanisms and distributed generation. It does not establish a distributed database commit sequence.

Design inference: immutable UUIDv7-like record IDs can be the portable creation-order key. Same-millisecond IDs from separate processes and clock skew must not be described as exact chronological or commit ordering.

Source: [RFC 9562, sections 5.7, 6.2, 6.4, 6.11](https://www.rfc-editor.org/rfc/rfc9562.html).

## Go build constraints and module dependencies

Observed: build constraints select files using Boolean expressions over tags. The Go module reference states that `go mod tidy` considers all build tags, except `ignore`.

Design inference: a provider profile must be encoded explicitly in file constraints; passing `aws` does not itself enable an unrelated `s3` tag. Keeping drivers in separate modules can isolate dependency manifests. Tags alone do not provide that isolation. Tags on an optional assembly package cannot remove an unconditional driver import elsewhere in the application.

Sources: [Build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints), [go mod tidy](https://go.dev/ref/mod#go-mod-tidy). Local `go help buildconstraint` and `go help mod tidy` were also inspected; no library build was performed.

## bbolt process model

Observed: bbolt uses a file lock, permits one read-write transaction at a time, and documents the restriction on sharing a database file across processes.

Design inference: bbolt is a local persistence target. Multiple Lambda instances require a shared remote backend rather than independent local bbolt files. Goroutine safety and distributed access are separate capabilities.

Source: [bbolt README, Opening a database and Transactions](https://github.com/etcd-io/bbolt).

## DynamoDB conditions and ordering

Observed: DynamoDB supports conditional item mutations. Query selects a partition key and orders by sort key. GSI updates are asynchronous and GSI reads are eventually consistent.

Design inference: a collection partition with UUIDv7-like sort keys is a candidate for creation-order listing without a mutable update-time GSI. Collection partition throughput and consistency still require validation.

Sources: [UpdateItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateItem.html), [Query](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html), [Global secondary indexes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/GSI.html).

## Amazon S3 object operations

Observed: conditional writes support create-if-absent with `If-None-Match` and replacement conditioned on ETag with `If-Match`. General purpose bucket listing is lexicographic by key; directory buckets have different ordering. S3 documents atomic updates to one key and no atomic update spanning keys.

Design inference: one object per immutable record ID avoids a separate mutable ordering index for ascending creation-order listing. A reverse-order requirement needs a separately chosen key encoding or listing strategy. Safe deletion, pagination during concurrent changes, and ETag/record-revision handling require a driver contract and tests. Do not extrapolate S3 behavior to every compatible service.

Sources: [Conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html), [ListObjectsV2](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html), [Consistency model](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html#ConsistencyModel).

## Google Cloud Storage preconditions

Observed: object operations expose generation preconditions. `ifGenerationMatch=0` on creation permits the operation only when no live object exists; a matching generation can guard mutations.

Design inference: generation tokens are a candidate implementation of per-record optimistic concurrency. Exact endpoints, listing behavior, token refresh, and error mapping remain to be tested.

Source: [Request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions), [Objects insert](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/insert).

## Firebase product choice

Observed: Firestore exposes REST CRUD and accepts Firebase ID tokens or Google OAuth tokens. Realtime Database separately supports ETag-based conditional REST `PUT` and `DELETE` requests.

Design inference: Firebase cannot be treated as a single storage protocol. Both products are candidates until the intended product is selected. REST availability alone does not prove their full listing and concurrency contracts match this library.

Sources: [Firestore REST API](https://firebase.google.com/docs/firestore/use-rest-api), [Realtime Database conditional requests](https://firebase.google.com/docs/database/rest/save-data#section-conditional-requests).

## Cloudflare R2 conditional writes

Observed: R2 exposes an S3-compatible HTTP API. Its compatibility documentation states that `PutObject` conditional headers are evaluated atomically against committed bucket state.

Design inference: R2 can share an object-storage abstraction with S3 while retaining service-specific checks. Conditional deletion, pagination, and error behavior must be verified independently.

Sources: [R2 API surfaces](https://developers.cloudflare.com/r2/api/), [S3 API compatibility](https://developers.cloudflare.com/r2/api/s3/api/).

## Cloudflare D1 access path

Observed: D1 exposes a REST SQL query endpoint with bound parameters. Cloudflare recommends the built-in REST API for administrative use because global API rate limits apply, and documents using a Worker as an application API proxy.

Design inference: direct REST can be an optional low-traffic path, with its operational limits documented. A Worker gateway is a separate deployment option; adding a library build tag does not deploy a Worker. Single-statement revision-conditional SQL mutations are a candidate CAS implementation, not a tested result.

Sources: [Query D1 Database](https://developers.cloudflare.com/api/resources/d1/subresources/database/methods/query/), [Build an API with a proxy Worker](https://developers.cloudflare.com/d1/tutorials/build-an-api-to-access-d1/).

## Cloudflare KV concurrency

Observed: Cloudflare documents eventual visibility across locations and competing writes overwriting one another, with the last write taking precedence. Its documented native writes do not supply the per-record CAS contract proposed here.

Design inference: KV alone must not be advertised as supporting conflict-safe distributed mutations. A limited-capability adapter or an external authoritative coordinator is required. Serializing writers alone does not turn KV readers into strongly consistent readers.

Source: [Writing KV pairs, concurrent writes](https://developers.cloudflare.com/kv/api/write-key-value-pairs/#concurrent-writes-to-the-same-key).

## SDK and SQL dependency boundaries

Observed: AWS SDK for Go v2 service clients are separate Go modules. Go's SQL API uses database-specific drivers.

Design inference: measure final executable size, dependency graph, and startup separately. A thin HTTP client is a preference, not a demonstrated size improvement. Authentication, credential refresh, signing, retries, and error handling still need implementation or selected dependencies. General RDB support cannot assume a native HTTP interface.

Sources: [AWS SDK v2 modularization](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/migrate-gosdk.html#modularization), [Go SQL drivers](https://go.dev/wiki/SQLDrivers).
