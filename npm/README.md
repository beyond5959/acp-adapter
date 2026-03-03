# npm Packaging for acp-adapter

This directory contains an npm workspace that publishes `acp-adapter` for:

- `darwin` + `x64`
- `darwin` + `arm64`
- `linux` + `x64`
- `linux` + `arm64`
- `win32` + `x64`
- `win32` + `arm64`

## Build binaries

From repository root:

```bash
npm --prefix npm run build:binaries
npm --prefix npm run check:binaries
```

## Pack locally

```bash
npm --prefix npm run pack:all
```

## Publish flow

1. Set one version for all npm packages:

```bash
npm --prefix npm run version:set -- 0.1.1
```

2. Build binaries.
3. Publish platform packages first, then publish `@beyond5959/acp-adapter`.

```bash
npm --prefix npm run publish:all
```

Users can run:

```bash
npx -y @beyond5959/acp-adapter
```
