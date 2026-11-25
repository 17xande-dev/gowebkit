# Variant manifest

`template/variants.txt` declares ownership for both variants. Each line is one
case-sensitive, slash-separated path relative to a variant directory. `#`
starts a comment anywhere on a line. Whitespace around the entry is ignored;
blank lines are ignored. A trailing `/` owns every descendant of a directory.
Without it, the entry owns only that exact file. Paths must be clean and
relative; there are no globs, escapes, or negations.

A declared path must exist in **both** variants (a directory entry needs at
least one file in each), because a variant-owned path is not compared and a
file one variant forgot would otherwise go unnoticed. When only one variant is
meant to have a path, prefix the entry: `postgres-only: sqlc.yaml` or
`sqlite-only: path`. The checker then requires the file in that variant and
absent from the other. An entry that matches no file at all is reported too.
Declare, for example, `go.mod` for dependency differences, `internal/db/` for a
database package, or individual files in a package with shared tests.

From the repository root:

```sh
(cd tools && GOWORK=off go run ./variantsync -root .. -check)
(cd tools && GOWORK=off go run ./variantsync -root ..)
```

SQLite is canonical for everything not declared. The checker reports missing,
changed, and unexpected shared files and these manifest problems (write mode
reports the manifest problems too, and fails). Write mode updates shared files and **removes
undeclared Postgres-only files**. Declare a Postgres-specific path before
syncing it. Both modes see tracked and unignored new files; ignored local
files are not synced or overwritten. Only matching Go imports and, if not
variant-owned, the `go.mod` module directive are rewritten.
