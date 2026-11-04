# Dynamox for VS Code

This extension adds syntax highlighting, schema validation, context-aware completions, and formatting for `.dynamox` files. It bundles the Dynamox editor process, so installed users do not need to install the Dynamox CLI or Go.

## Local development

Go 1.24 or newer is required to build the extension from source. Compile the editor process for your current platform and run the completion tests:

```sh
npm run compile
npm test
```

Open this directory in VS Code and press **F5** to start an Extension Development Host. Use **Format Document** to apply the canonical formatting from `dynamox fmt`.

## Publish

The extension publisher ID is `dynamox`. Change the extension version in `package.json` in a reviewed commit before each release. Then run **Publish Dynamox VS Code Extension** from the Actions tab. The workflow publishes Windows, macOS, and Linux packages for x64 and arm64.

Configure Marketplace Trusted Publishing for publisher `dynamox` with:

- GitHub owner: `liberocks`
- Repository: `dynamox`
- Workflow file: `publish-vscode.yml` (located in `.github/workflows`)

The workflow uses GitHub Actions OIDC (`id-token: write`) and needs no Marketplace token in repository secrets. It has only a `workflow_dispatch` trigger, so it must be run manually from the repository's default branch.
