package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func main() {
	args := os.Args[1:]
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <virtual_filename> <file1> ... <fileN>\n", os.Args[0])
		os.Exit(1)
	}

	vFile := args[0]
	sourceFiles := args[1:]

	ufs := mustNewUnionFS(sourceFiles, filepath.Base(vFile))

	mountpoint, err := filepath.Abs(filepath.Dir(vFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mountpoint: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", mountpoint, err)
		os.Exit(1)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	if sameDir(mountpoint, cwd) {
		fmt.Fprintf(os.Stderr, "refusing to mount on current directory; place the virtual file in a subdirectory\n")
		os.Exit(1)
	}

	conn, err := fuse.Mount(mountpoint, fuse.AllowNonEmptyMount())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mount: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interrupt
		fmt.Fprintf(os.Stderr, "\nunmounting %s...\n", mountpoint)
		fuse.Unmount(mountpoint)
	}()

	fmt.Printf("serving on %s (Ctrl-C to stop)\n", mountpoint)

	if err := fs.Serve(conn, ufs); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return
	}
}
