# dynamox

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
  editTimes DateTime[]
  author User @relation(fields: [authorId], references: [id])
  editor User? @relation(fields: [editorId], references: [id])

  @@table(name: "posts")
  @@index(name: "ByAuthor", partition: authorId, sort: createdAt, type: GSI)
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
- `sync` creates missing tables with on-demand billing and all-attribute index projections. Existing tables are checked for key and index drift; dynamox reports differences without applying changes. Use `--endpoint http://localhost:8000` for DynamoDB Local.

## AWS IAM permissions

`fmt`, `validate`, and `generate` are local operations and need no AWS permissions. `sync` needs permission to inspect the tables declared in the schema and create any that are missing. It does not list every table in the account, update existing tables, or delete tables, so it does not need `dynamodb:ListTables`, `dynamodb:UpdateTable`, or `dynamodb:DeleteTable`.

For a deployment role that runs `dynamox sync`, allow these actions only on the model table ARNs from `@@table(...)`. Include `dynamox_sequences` when the schema uses `@default(serial)`:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "DynamoxSyncTables",
    "Effect": "Allow",
    "Action": ["dynamodb:CreateTable", "dynamodb:DescribeTable"],
    "Resource": [
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_1>",
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/<MODEL_TABLE_2>",
      "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dynamox_sequences"
    ]
  }]
}
```

Replace `REGION`, `ACCOUNT_ID`, and the model table placeholders. Omit the sequence table ARN if the schema has no serial IDs. If table creation belongs to a separate deployment role, the application role does not need `CreateTable` or `DescribeTable`.

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

Generated CRUD methods include `Create<Model>`, `Get<Model>`, `Update<Model>`, `Delete<Model>`, and paginated `Scan<Model>`. `Create<Model>` returns the saved model and any generated key. Query methods are emitted only for the table primary key and declared indexes. Update inputs use `Set<Field>` pointers; optional fields also have `Clear<Field>` flags. Transaction helpers compose generated read and write operations with `TransactGet` and `TransactWrite`.

The module uses AWS SDK for Go v2. The caller supplies the SDK client, so region, credentials, retries, and endpoint configuration remain controlled by the application.
