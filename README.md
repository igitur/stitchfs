# stitchfs

A Go FUSE filesystem that stitches multiple files end-to-end into a single writable virtual file. The first N-1 source files are read-only; the last file is writable.

## Prerequisites

- Go 1.26+
- `libfuse` (libfuse2 or libfuse3)
- `fusermount`

## Build

```
go build .
```

## Usage

```
./stitchfs <virtual_filename> <file1> <file2> ... <fileN>
```

The mountpoint is auto-derived — it's the directory containing the last (writable) source file. All files in that directory are transparently mirrored via passthrough.

## Example

Create three source files (10 bytes each):

```
echo -n "0123456789" > header.bin
echo -n "abcdefghij" > body.bin
echo -n "ABCDEFGHIJ" > appendix.bin
```

Build and mount (mountpoint = current directory):

```
go build .
./stitchfs story.bin header.bin body.bin appendix.bin &
```

The virtual file appears alongside the source files in the current directory:

```
$ cat story.bin
0123456789abcdefghijABCDEFGHIJ
```

Read across the boundary between header.bin and body.bin (offset 10):

```
$ dd if=story.bin bs=1 skip=8 count=4 2>/dev/null
89ab
```

Write to the last file (appendix.bin, at offset 20—second half):

```
$ echo -n "XXXXX" | dd of=story.bin bs=1 seek=25 conv=notrunc 2>/dev/null
$ cat appendix.bin
ABCDEXXXXX
```

Writing to a read-only segment is rejected:

```
$ echo -n "X" | dd of=story.bin bs=1 seek=5 conv=notrunc 2>&1
dd: error writing 'story.bin': Read-only file system
```

Truncate shrinks only the writable segment (appendix.bin):

```
$ truncate -s 20 story.bin     # keep only header + body (20 bytes)
$ ls -l appendix.bin
-rw-r--r-- 1 user user 0 ... appendix.bin
```

## Unmount

```
fusermount -u <directory-of-last-source-file>
```

## Passthrough

The mountpoint is the directory containing the last (writable) source file. All real files in that directory are transparently mirrored alongside the virtual file — forwarded to the real filesystem for reads, writes, truncates, and chmod. Subdirectories are mirrored recursively.

If your source files live in `/my/data/`:

```
./stitchfs story.bin /my/data/a.bin /my/data/b.bin /my/data/c.bin
```

Then `/my/data/` is the mountpoint and contains `story.bin` alongside every real file.

## How it works

The virtual file's byte range is partitioned across the source files in order. The sum of the first N-1 file sizes defines a `base offset`. Reads and writes below this offset map to the read-only files; reads and writes at or beyond it map to the last (writable) file.

```
[ file1 ] [ file2 ] [ file3 (writable) ]
  read-only             writable
  ↑                     ↑
  offset 0              base offset
```
