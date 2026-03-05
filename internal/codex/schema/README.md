# App Server Schema

Use the root `Makefile` target to refresh schema artifacts:

```bash
make schema
```

Generated files are expected to be committed under this directory.

`make schema` also runs `schema-check` and refreshes `SHA256SUMS` so schema
changes are hash-traceable in git history.
