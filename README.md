# dynamox

[![Statement coverage: 100%](https://img.shields.io/badge/statement%20coverage-100%25-brightgreen)](https://github.com/liberocks/dynamox/actions/workflows/ci.yml)

dynamox is a schema-first DynamoDB ORM for Go. Each schema model maps to one physical DynamoDB table. Generated methods only query a primary key or a declared secondary index; other fields remain strongly typed model fields and can be read after an item is loaded.

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

Supported scalar types are `String`, `Int`, `Float`, `Boolean`, `DateTime`, and `Bytes`. Add `?` for optional fields and `[]` for lists. `DateTime` uses Go `time.Time` and is stored as RFC 3339 text. Named `type` fields marked `@jsonString` are encoded as JSON strings; invalid stored JSON returns a field-specific decode error. Enums are generated as named Go string types.

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

The [`vscode-dynamox`](./vscode-dynamox) extension adds syntax highlighting, completions, live schema diagnostics, and formatting for `.dynamox` files. It bundles the editor engine and is released manually through the **Publish Dynamox VS Code Extension** GitHub Actions workflow. Configure Marketplace Trusted Publishing for the `dynamox` publisher with the repository and workflow details in the extension README.

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

CI runs the runtime and generated-client benchmarks against [Moto](https://github.com/getmoto/moto), an open-source DynamoDB emulator, without AWS credentials or an AWS account. They cover Get, Create, Update, secondary-index Query, Scan, loading a user's posts through the generated relation method, and generated transactional reads and writes. Their timings include Go, the AWS SDK, HTTP, and emulator overhead; they are useful for comparing changes in the same environment, not for estimating AWS service latency or throughput.

To run them locally, install Moto and start its [standalone server](https://docs.getmoto.org/en/latest/docs/server_mode.html):

```sh
python3 -m pip install 'moto[server]==5.2.4'
moto_server -H 127.0.0.1 -p 8000
```

In another terminal, run:

```sh
DYNAMODB_ENDPOINT=http://127.0.0.1:8000 go test -run '^$' -bench '^BenchmarkDynamox' -benchmem ./runtime ./examples/blog/dynamoxgen
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

The generated package contains one Go struct for each model and named Go types for schema `type` and `enum` blocks. Model scalar fields map to `string`, `int64`, `float64`, `bool`, `time.Time`, or `[]byte`; optional fields become pointers and list fields become slices. Custom types remain named structs, and enums are named string types with constants for their declared values.

Relation declarations describe how generated relation loaders fetch related rows; relation fields are not stored on the model struct. For each model, dynamox also emits a `<Model>Key` struct containing its primary key fields and a `<Model>Update` struct containing typed `Set<Field>` values. Optional fields additionally have `Clear<Field>` flags, so an update can distinguish leaving a value unchanged from removing it. Primary-key fields are not update fields.

Fields marked `@jsonString` use their declared Go type in the model while storing JSON text in DynamoDB. The generated encoder and decoder handle `encoding/json` conversion, including optional and list values. Invalid stored JSON returns an error identifying the model and field. DateTime fields use `time.Time` in Go and UTC RFC 3339 Nano strings in DynamoDB.

### Generated client methods

The generated `Client` wraps the AWS SDK for Go v2 DynamoDB client supplied to `NewClient`. `NewClientWithIDGenerator` is also generated for applications that want to provide their own ID generator; otherwise model defaults such as UUIDv7, ObjectID, or serial IDs are applied by `Create<Model>` when the key is omitted. The create method returns the saved model, including its generated key.

For each model, the client exposes:

- `Create<Model>`, `Get<Model>`, `Update<Model>`, and `Delete<Model>` for typed item operations. `Get<Model>` returns the model, a found flag, and an error.
- `Scan<Model>` for an unfiltered, paginated table scan.
- `Query<Model>By<PrimaryKey>` and `Query<Model>Using<Index>` only for the primary key and declared indexes. Each query requires partition-key equality; indexes with a sort key also get a typed sort-condition struct supporting `=`, `<`, `<=`, `>`, `>=`, and `begins_with`. Non-index fields intentionally have no query method.
- `Read<Model>`, `Write<Model>`, `Update<Model>Operation`, and `Delete<Model>Operation` helpers for composing transactions.

Queries and scans take `runtime.PageRequest` (`Limit`, `Cursor`, and scan direction) and return `runtime.Page[Model]` with `Items` and the next `Cursor`. Pass that cursor to the next request to fetch another page. Relation loaders such as `GetUserWithPosts` first read the model and then use the declared indexed path; to-many relations return a page and can therefore be paginated.

`TransactGet` and `TransactWrite` accept the generated read/write operations and execute them through DynamoDB transactions. The application retains control over AWS region, credentials, retries, and endpoint configuration because it supplies the SDK client.
