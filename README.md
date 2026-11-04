# dynamox

[![Implementation statement coverage: 100%](https://img.shields.io/badge/implementation%20coverage-100%25-brightgreen)](https://github.com/liberocks/dynamox/actions/workflows/ci.yml)

dynamox is a schema-first DynamoDB ORM for Go. Models can share a physical table when their primary key declarations agree. Generated methods only query a primary key or a declared secondary index; other fields remain strongly typed model fields and can be read after an item is loaded.

## Schema

Save a schema as `schema.dynamox`:

```prisma
type Preferences {
  theme String
  digest Boolean
  alternate ThemeOption?
}

type ThemeOption {
  value String
}

model User {
  id String @id @default(uuidv7)
  name String
  preferences Preferences @jsonString
  settings Preferences @jsonString
  nickname String?
  posts Post[] @relation(fields: [id], references: [authorId], index: "ByAuthor")

  @@table(name: "users")
}

model Post {
  id String @id @default(objectid)
  authorId String
  editorId String?
  title String
  createdAt DateTime
  expiresAt Int?
  editTimes DateTime[]
  author User @relation(fields: [authorId], references: [id])
  editor User? @relation(fields: [editorId], references: [id])

  @@table(name: "posts")
  @@index(name: "ByAuthor", partition: authorId, sort: createdAt, type: GSI)
  @@ttl(field: expiresAt)
}

model SequenceItem {
  id Int @id @default(serial)
  value String

  @@table(name: "sequence_items")
}
```

Supported types are `String`, `Int`, `Float`, `Boolean`, `DateTime`, `Bytes`, and `Json`. Add `?` for optional fields and `[]` for lists. `DateTime` uses Go `time.Time` and is stored as RFC 3339 text. A named `type` field marked `@document` is stored as a native DynamoDB map or list; nested named types are stored natively as well. Nil optional typed documents and nested fields are absent. `Json` represents heterogeneous native DynamoDB values, including maps, lists, numbers, booleans, strings, and null. An optional `Json?` field uses a pointer: nil omits the field, while a pointer to a nil `runtime.JSON` stores DynamoDB NULL. Empty maps and lists remain empty collections. JSON numbers decode as `attributevalue.Number` to retain precision. Unsupported values, cycles, and non-finite numbers return encoding errors. Named `type` fields marked `@jsonString` continue to be encoded as JSON strings. Enums are generated as named Go string types.

```prisma
type Choice {
  label String
  value String
}
type Question {
  prompt String
  choices Choice[]
  providerData Json
}
model Quiz {
  id String @id
  questions Question[] @document
  rawProviderPayload Json?
  legacySettings Question @jsonString
  @@table(name: "quizzes")
}
```

Primary keys can generate an ID when the field is omitted: `@default(uuidv1)`, `@default(uuidv4)`, `@default(uuidv7)`, and `@default(objectid)` require a `String` key; `@default(serial)` requires an `Int` key. Supplied non-empty string IDs and non-zero integer IDs are preserved. Generated `Create<Model>` methods return `(Model, error)`, so callers receive the generated ID. Integer serial allocation uses atomic DynamoDB updates in the shared `dynamox_sequences` table; `dynamox sync` creates that table automatically when needed. Serial IDs increase per model but can have gaps if a later item write fails. Because zero means “not supplied” for an `Int` key, integer serial IDs cannot be explicitly set to zero.

The relation's `references` field must be the target model's primary partition key for a to-one relation, or a declared target index partition key for a to-many relation. Relation reads are explicit and page through to-many results.

TTL attributes must name one scalar `Int` field per model; store expiration times as Unix epoch seconds. Tables use on-demand billing by default. To provision a table and its GSIs, provide positive read and write capacity units as a pair:

```prisma
model Event {
  id String @id
  customerId String
  expiresAt Int?

  @@table(name: "events", read: 5, write: 2)
  @@index(name: "ByCustomer", partition: customerId, type: GSI, read: 2, write: 1)
  @@ttl(field: expiresAt)
}
```

Each GSI on a provisioned table needs its own capacity pair. An LSI shares the table's throughput and does not accept capacity arguments.

## CLI

Use dynamox as a dependency from your application repository. Add the Go module to the application and install the standalone CLI once:

```sh
go get github.com/liberocks/dynamox@latest
go install github.com/liberocks/dynamox/cmd/dynamox@latest
```

Run the installed `dynamox` command from the application directory that contains `schema.dynamox` (or pass the schema's path). The Go install bin directory must be on `PATH`.

```sh
dynamox fmt ./schema.dynamox
dynamox fmt --check ./schema.dynamox
dynamox validate ./schema.dynamox
dynamox generate --out ./db ./schema.dynamox
dynamox sync --profile my-aws-profile --region us-east-1 ./schema.dynamox
```

Use the same release version for `go get` and `go install` when pinning dynamox in a reproducible application setup. The version-suffixed `go install` installs the CLI without adding its command package to the application's dependencies.

- `fmt` writes canonical indentation and spacing while retaining comments. `fmt --check` exits nonzero if formatting would change the file.
- `validate` checks schema names, field types, keys, indexes, and relation read paths without contacting AWS.
- `generate` validates first, then writes `client_gen.go` into the output directory (or to the exact output file when the path is not a directory).
- `sync` creates missing tables with the declared billing mode and all-attribute index projections. For existing tables, it first checks key and index structure, then reconciles billing mode, table/GSI capacity, and TTL. If a structural difference exists, it reports the drift without applying changes. DynamoDB applies TTL changes asynchronously and limits each table to one TTL configuration update per hour; changing the TTL attribute requires separate disable and enable updates, so rerun `sync` after the reported transition and wait if DynamoDB reports its one-hour rate limit. Use `--endpoint http://localhost:8000` for DynamoDB Local.

## VS Code

The [`vscode-dynamox`](./vscode-dynamox) extension adds syntax highlighting, completions, live schema diagnostics, and formatting for `.dynamox` files, including `Json` and `@document`. It bundles the editor engine and is published manually through the **Publish Dynamox VS Code Extension** GitHub Actions workflow. Configure Marketplace Trusted Publishing for the `dynamox` publisher with the repository and workflow details in the extension README.

## AWS IAM permissions

`fmt`, `validate`, and `generate` are local operations and need no AWS permissions. `sync` needs permission to inspect declared tables, create missing tables, and update billing mode, capacity, and TTL settings on structurally matching existing tables. It does not list every table or delete tables, so it does not need `dynamodb:ListTables` or `dynamodb:DeleteTable`.

For a deployment role that runs `dynamox sync`, allow these actions only on the model table ARNs from `@@table(...)`. Include `dynamox_sequences` when the schema uses `@default(serial)`:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "DynamoxSyncTables",
    "Effect": "Allow",
    "Action": [
      "dynamodb:CreateTable",
      "dynamodb:DescribeTable",
      "dynamodb:UpdateTable",
      "dynamodb:DescribeTimeToLive",
      "dynamodb:UpdateTimeToLive"
    ],
    "Resource": [
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_1>",
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_2>",
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dynamox_sequences"
    ]
  }]
}
```

Replace `REGION`, `ACCOUNT_ID`, and the model table placeholders. Omit the sequence table ARN if the schema has no serial IDs. If schema synchronization belongs to a separate deployment role, the application role does not need these provisioning actions.

For an application using all generated CRUD, scan, query, relation, and transaction helpers, grant data actions on its model tables. Add `Query` on each secondary index ARN used by the schema. Add `UpdateItem` on `dynamox_sequences` only when the schema uses `@default(serial)`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DynamoxModelTableData",
      "Effect": "Allow",
      "Action": [
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:Query",
        "dynamodb:Scan"
      ],
      "Resource": [
        "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_1>",
        "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_2>"
      ]
    },
    {
      "Sid": "DynamoxSecondaryIndexQueries",
      "Effect": "Allow",
      "Action": "dynamodb:Query",
      "Resource": [
        "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<TABLE_WITH_INDEX>/index/<INDEX_NAME>"
      ]
    },
    {
      "Sid": "DynamoxSerialIDs",
      "Effect": "Allow",
      "Action": "dynamodb:UpdateItem",
      "Resource": "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dynamox_sequences"
    }
  ]
}
```

Replace the placeholders and list each model table and queried index. Omit the index statement when there are no secondary-index queries, and omit the serial statement when there are no serial IDs. If the application uses only some generated operations, remove unused actions such as `Scan` or `DeleteItem`. DynamoDB authorizes transaction helper reads and writes through their underlying `GetItem`, `PutItem`, `UpdateItem`, and `DeleteItem` actions. See AWS's [table and index IAM examples](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/iam-policy-specific-table-indexes.html), [transaction authorization guide](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis-iam.html), and [DynamoDB service authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html).

## Benchmarks

CI runs the runtime and generated-client benchmarks against [Moto](https://github.com/getmoto/moto), an open-source DynamoDB emulator, without AWS credentials or an AWS account. They cover Get and consistent Get/Query, Create, conditional Put and Update, `ALL_NEW` updates, filtered scans, secondary-index queries, generated relations, transaction reads and writes, cross-client conditional publishing, and native document/`Json` encode and decode. The document codec benchmarks isolate Go serialization; database benchmarks include Go, the AWS SDK, HTTP, and emulator overhead. All timings are useful for comparing changes in the same environment, not for estimating AWS service latency or throughput.

To run them locally, install Moto and start its [standalone server](https://docs.getmoto.org/en/latest/docs/server_mode.html):

```sh
python3 -m pip install 'moto[server]==5.2.4'
moto_server -H 127.0.0.1 -p 8000
```

In another terminal, run:

```sh
DYNAMODB_ENDPOINT=http://127.0.0.1:8000 go test -run '^$' -bench '^BenchmarkDynamox' -benchmem ./runtime ./examples/blog/dynamoxgen ./examples/overmost/transaction
```

## Generated client

```go
cfg, err := config.LoadDefaultConfig(ctx)
if err != nil { return err }
client := dynamoxgen.NewClient(dynamodb.NewFromConfig(cfg))

user, found, err := client.GetUser(ctx, dynamoxgen.UserKey{ID: "u-1"})
created, err := client.CreateUser(ctx, dynamoxgen.User{Name: "New user"}) // created.ID contains a UUIDv7
page, err := client.QueryPostUsingByAuthor(ctx, "u-1", &dynamoxgen.PostByAuthorSort{
    Operator: runtime.GreaterEqual,
    Value: time.Now().Add(-24 * time.Hour),
}, runtime.PageRequest{Limit: 25})
withPosts, err := client.GetUserWithPosts(ctx, dynamoxgen.UserKey{ID: "u-1"}, runtime.PageRequest{Limit: 25})

var transactionalUser dynamoxgen.User
readUser, err := client.ReadUser(dynamoxgen.UserKey{ID: "u-1"}, &transactionalUser)
post := dynamoxgen.Post{ID: "p-1", AuthorID: "u-1", Title: "Hello"}
writePost, err := client.WritePost(post)
err = client.TransactGet(ctx, readUser)
err = client.TransactWrite(ctx, writePost)
```

### Generated types

The generated package contains one Go struct for each model and named Go types for schema `type` and `enum` blocks. Model scalar fields map to `string`, `int64`, `float64`, `bool`, `time.Time`, `[]byte`, or `runtime.JSON`; optional fields become pointers and list fields become slices. Custom types remain named structs, and enums are named string types with constants for their declared values.

Relation declarations describe how generated relation loaders fetch related rows; relation fields are not stored on the model struct. For each model, dynamox also emits a `<Model>Key` struct containing its primary key fields and a `<Model>Update` struct containing typed `Set<Field>` values. Optional fields additionally have `Clear<Field>` flags, so an update can distinguish leaving a value unchanged from removing it. Primary-key fields are not update fields.

Fields marked `@document` use their declared Go struct or slice type while storing native DynamoDB maps and lists. `Json` fields use `runtime.JSON` and preserve native DynamoDB types; optional JSON fields distinguish absence from explicit NULL. `@jsonString` retains its existing JSON text representation. DateTime fields use `time.Time` in Go and UTC RFC 3339 Nano strings in DynamoDB. Generated encoders return errors rather than dropping failed conversions.

### Generated client methods

The generated `Client` wraps the AWS SDK for Go v2 DynamoDB client supplied to `NewClient`. `NewClientWithIDGenerator` remains available, and `NewClientWithOptions` accepts `runtime.ClientOptions` for physical table-name overrides and a custom ID generator. The map keys are schema table names; names not overridden keep their schema defaults. Overrides apply to CRUD, queries, scans, relations, serial counters, and transaction operations. Construction does not contact DynamoDB or provision tables. Model ID defaults are applied by `Create<Model>` when the key is omitted.

For each model, the client exposes:

- `Create<Model>`, `Get<Model>`, `Update<Model>`, and `Delete<Model>` for typed item operations. Additive `WithOptions` methods accept conditions or read consistency. `Put<Model>` performs a full-item upsert, and `Update<Model>Returning` returns the decoded `ALL_NEW` result from the same update request.
- `Scan<Model>` for a paginated table scan, and `Scan<Model>WithOptions` for consistency and filter expressions.
- `Query<Model>By<PrimaryKey>` and `Query<Model>Using<Index>` only for the primary key and declared indexes. Each query requires partition-key equality; indexes with a sort key also get a typed sort-condition struct supporting `=`, `<`, `<=`, `>`, `>=`, and `begins_with`. Non-index fields intentionally have no query method.
- `Read<Model>`, `Write<Model>`, `Update<Model>Operation`, and `Delete<Model>Operation` helpers for composing transactions.

Queries and scans take `runtime.PageRequest` (`Limit`, `Cursor`, direction, and optional consistency) and return `runtime.Page[Model]` with `Items` and the next `Cursor`. Scan filters use `runtime.Expression{Text, Names, Values}` and preserve DynamoDB pagination: a filtered page can be empty and still carry a cursor. Consistent reads are rejected for GSIs; primary-table and LSI reads support them. Query filters never create implicit scans.

Conditions use DynamoDB expression syntax and `runtime.Expression` placeholders. Values are Go values and Dynamox encodes them. For example: `runtime.Expression{Text: "#status = :unpublished", Names: map[string]string{"#status": "status"}, Values: map[string]any{":unpublished": "unpublished"}}`. Pass the expression with `runtime.WriteOptions{Condition: ...}`. Update placeholders avoid names and values already used by the condition.

`TransactGet` and `TransactWrite` accept operations from any generated package sharing the same DynamoDB connection, so separate admin and learner schemas can participate in one atomic transaction. `Create<Model>Operation` retains create-if-absent behavior; `Write<Model>` performs a full-item replacement. AWS transaction cancellation errors remain available through `errors.As`.

`examples/overmost` contains separate admin and learner packages and a publishing transaction that conditionally updates an admin draft while creating learner content. Models sharing `@@table` can contribute distinct indexes. Conflicting keys, capacity, TTL, or same-name index declarations fail validation. `dynamox sync` merges and provisions each physical table once. Applications using Terraform or CloudFormation can use table-name overrides without provisioning permissions.
