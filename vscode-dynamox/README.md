# Dynamox for VS Code

This extension adds syntax highlighting, schema validation, context-aware completions, and formatting for `.dynamox` files, including DynamoDB TTL and provisioned capacity declarations, native `Json` fields, and `@document` storage. It bundles the Dynamox editor process, so installed users do not need to install the Dynamox CLI or Go.

Use `@@ttl(field: expiresAt)` to mark a scalar `Int` model field for DynamoDB TTL. TTL values are Unix epoch seconds. Add positive `read` and `write` values to `@@table` to select provisioned capacity, then set both values on each GSI. Tables without capacity values use on-demand billing; LSIs share table capacity. DynamoDB TTL changes are asynchronous and limited to one update per table per hour.

## Local development

Go 1.24 or newer and Node.js 22.12 or newer are required to build and test the extension from source. The extension and its editor build helper are written in TypeScript. Install the locked development tools, compile the extension and editor process, then run the unit tests and coverage gate:

```sh
npm ci
npm run compile
npm test
```

The tests enforce per-file coverage thresholds for every TypeScript runtime module.

Open this directory in VS Code and press **F5** to start an Extension Development Host. Use **Format Document** to apply the canonical formatting from `dynamox fmt`.
