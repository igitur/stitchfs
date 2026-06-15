# AGENTS.md

## Project overview
A Go FUSE filesystem that concatenates multiple files into a single writable virtual file. The first N-1 files are read-only segments; the last file is writable. When `passthroughDir` is set, all real files from the source directory are transparently mirrored alongside the stitched file.

## Commands
- `go build .` — build
- `go test ./...` — run tests
- `./stitchfs <virtual_filename> <file1> ... <fileN>` — mount
- `fusermount -u <dirname>` — unmount (the directory containing the last, writable, source file)

## Architecture
Three source files:
- `main.go` — CLI entrypoint, arg parsing, `fuse.Mount` + `fs.Serve`
- `stitch.go` — types and `bazil.org/fuse/fs` interface implementations
- `stitch_test.go` — unit tests

Five key types:
- `RootDir` — directory node, serves `Attr`, `Lookup`, `ReadDirAll`
- `VirtualFile` — stitched file node, serves `Attr`, `Open`, `Setattr` (truncate)
- `FileHandle` — open handle for stitched file, serves `Read`, `Write`, `Release`
- `PassThrough` — real file/dir node, serves `Attr`, `Open`, `Lookup`, `ReadDirAll`, `Setattr`
- `PassThroughHandle` — open handle for real files, serves `Read`, `Write`, `Release`

## Gotchas
- Linux-only — `bazil.org/fuse` speaks the Linux FUSE protocol via `/dev/fuse`
- Writes to offsets < `baseOff` return `EROFS`
- Truncate limited to writable segment (rejects size < `baseOff` with `EINVAL`)
- Each `FileHandle` owns its `[]*os.File` descriptors; they're opened once in `Open` and closed in `Release`
- `Read` uses `ReadAt` for position-independent access; handles `io.EOF` as non-fatal
- Mountpoint is auto-derived from `filepath.Dir` of the last (writable) source file (mounted with `AllowNonEmptyMount`)
- `passthroughDir` is auto-set to `filepath.Dir` of the last source file (same as mountpoint); empty string disables passthrough entirely
- `PassThrough.Open` returns the node itself as handle when `req.Dir == true` so directory readdir works
