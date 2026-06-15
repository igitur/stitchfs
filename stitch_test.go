package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"bazil.org/fuse"
	"bazil.org/fuse/fs/fstestutil"
)

func tempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "stitchfs-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// sum
// ---------------------------------------------------------------------------

func TestSum(t *testing.T) {
	if s := sum([]int64{1, 2, 3}); s != 6 {
		t.Errorf("expected 6, got %d", s)
	}
	if s := sum([]int64{}); s != 0 {
		t.Errorf("expected 0, got %d", s)
	}
	if s := sum([]int64{-1, 1}); s != 0 {
		t.Errorf("expected 0, got %d", s)
	}
}

// ---------------------------------------------------------------------------
// NewUnionFS
// ---------------------------------------------------------------------------

func TestNewUnionFS(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("AAA"))
	writeFile(t, dir, "b.bin", []byte("BBBBB"))
	writeFile(t, dir, "c.bin", []byte("CC"))

	u, err := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
		filepath.Join(dir, "c.bin"),
	}, "out.dat")
	if err != nil {
		t.Fatal(err)
	}

	if u.vName != "/out.dat" {
		t.Errorf("vName = %q, want /out.dat", u.vName)
	}
	if len(u.fileSizes) != 2 {
		t.Fatalf("fileSizes len = %d, want 2", len(u.fileSizes))
	}
	if u.fileSizes[0] != 3 || u.fileSizes[1] != 5 {
		t.Errorf("fileSizes = %v, want [3 5]", u.fileSizes)
	}
	if u.baseOff != 8 {
		t.Errorf("baseOff = %d, want 8", u.baseOff)
	}
	if len(u.files) != 3 {
		t.Errorf("files len = %d, want 3", len(u.files))
	}
	for i, p := range u.files {
		if !filepath.IsAbs(p) {
			t.Errorf("files[%d] = %q, want absolute path", i, p)
		}
	}
}

func TestNewUnionFSMissingFile(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "exists.bin", []byte("A"))
	_, err := NewUnionFS([]string{
		"/nonexistent/file.bin",
		filepath.Join(dir, "exists.bin"),
	}, "out.dat")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// RootDir
// ---------------------------------------------------------------------------

func TestRootDirAttr(t *testing.T) {
	u := &UnionFS{}
	d := &RootDir{fs: u}
	a := &fuse.Attr{}
	if err := d.Attr(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if a.Mode&os.ModeDir == 0 {
		t.Error("expected directory mode bit")
	}
	if a.Inode != 1 {
		t.Errorf("inode = %d, want 1", a.Inode)
	}
}

func TestRootDirLookup(t *testing.T) {
	u := &UnionFS{vName: "/story.dat"}
	d := &RootDir{fs: u}

	node, err := d.Lookup(context.Background(), "story.dat")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, ok := node.(*VirtualFile); !ok {
		t.Errorf("expected *VirtualFile, got %T", node)
	}

	_, err = d.Lookup(context.Background(), "other.dat")
	if err != fuse.ENOENT {
		t.Errorf("expected fuse.ENOENT, got %v", err)
	}
}

func TestRootDirReadDirAll(t *testing.T) {
	u := &UnionFS{vName: "/story.dat"}
	d := &RootDir{fs: u}
	ents, err := d.ReadDirAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 3 {
		t.Fatalf("got %d entries, want 3", len(ents))
	}
	if ents[0].Name != "." || ents[1].Name != ".." {
		t.Error("missing . or .. entries")
	}
	if ents[2].Name != "story.dat" || ents[2].Type != fuse.DT_File {
		t.Errorf("file entry = %+v, want story.dat DT_File", ents[2])
	}
}

// ---------------------------------------------------------------------------
// VirtualFile
// ---------------------------------------------------------------------------

func TestVirtualFileAttr(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("AAAA"))
	writeFile(t, dir, "b.bin", []byte("BB"))

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	a := &fuse.Attr{}
	if err := vf.Attr(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if a.Mode != 0o644 {
		t.Errorf("mode = %o, want 0644", a.Mode)
	}
	if a.Size != 6 {
		t.Errorf("size = %d, want 6", a.Size)
	}
	// mtime should be set from the last file
	if a.Mtime.IsZero() {
		t.Error("mtime is zero")
	}
}

func TestVirtualFileOpen(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("X"))
	writeFile(t, dir, "b.bin", []byte("Y"))

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	req := &fuse.OpenRequest{}
	resp := &fuse.OpenResponse{}

	h, err := vf.Open(context.Background(), req, resp)
	if err != nil {
		t.Fatal(err)
	}
	fh, ok := h.(*FileHandle)
	if !ok {
		t.Fatalf("expected *FileHandle, got %T", h)
	}
	if len(fh.files) != 2 {
		t.Errorf("files len = %d, want 2", len(fh.files))
	}

	// Open with missing file
	u2, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		"/nonexistent/file",
	}, "out.dat")
	vf2 := &VirtualFile{fs: u2}
	_, err = vf2.Open(context.Background(), req, resp)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// FileHandle Read
// ---------------------------------------------------------------------------

func TestFileHandleRead(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("0123456789"))          // 10 bytes
	writeFile(t, dir, "b.bin", []byte("abcdefghij"))          // 10 bytes
	writeFile(t, dir, "c.bin", []byte("ABCDEFGHIJ"))          // 10 bytes

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
		filepath.Join(dir, "c.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	h, err := vf.Open(context.Background(), &fuse.OpenRequest{}, &fuse.OpenResponse{})
	if err != nil {
		t.Fatal(err)
	}
	fh := h.(*FileHandle)
	ctx := context.Background()

	tests := []struct {
		name    string
		offset  int64
		size    int64
		want    string
		wantLen int
	}{
		{"full read", 0, 30, "0123456789abcdefghijABCDEFGHIJ", 30},
		{"cross a->b", 8, 4, "89ab", 4},
		{"cross b->c", 18, 4, "ijAB", 4},
		{"at boundary", 10, 2, "ab", 2},
		{"start at b", 10, 5, "abcde", 5},
		{"zero size", 0, 0, "", 0},
		{"past EOF", 28, 5, "IJ", 2},
		{"way past EOF", 50, 5, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &fuse.ReadRequest{Offset: tt.offset, Size: int(tt.size)}
			resp := &fuse.ReadResponse{}
			if err := fh.Read(ctx, req, resp); err != nil {
				t.Fatal(err)
			}
			if string(resp.Data) != tt.want {
				t.Errorf("data = %q, want %q", resp.Data, tt.want)
			}
			if len(resp.Data) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(resp.Data), tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FileHandle Write
// ---------------------------------------------------------------------------

func TestFileHandleWrite(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("AAAA"))  // 4 bytes
	writeFile(t, dir, "b.bin", []byte("BBBB"))  // 4 bytes, writable

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	h, err := vf.Open(context.Background(), &fuse.OpenRequest{Flags: fuse.OpenWriteOnly}, &fuse.OpenResponse{})
	if err != nil {
		t.Fatal(err)
	}
	fh := h.(*FileHandle)
	ctx := context.Background()

	// Write to writable segment (offset = baseOff = 4)
	req := &fuse.WriteRequest{Offset: 4, Data: []byte("X")}
	resp := &fuse.WriteResponse{}
	if err := fh.Write(ctx, req, resp); err != nil {
		t.Fatal(err)
	}
	if resp.Size != 1 {
		t.Errorf("size = %d, want 1", resp.Size)
	}

	// Verify the last file was modified
	data, _ := os.ReadFile(filepath.Join(dir, "b.bin"))
	if string(data) != "XBBB" {
		t.Errorf("b.bin = %q, want XBBB", data)
	}

	// Write to read-only segment (offset < baseOff)
	reqRO := &fuse.WriteRequest{Offset: 2, Data: []byte("X")}
	respRO := &fuse.WriteResponse{}
	err = fh.Write(ctx, reqRO, respRO)
	if err == nil {
		t.Fatal("expected EROFS error")
	}
	errno, ok := err.(fuse.Errno)
	if !ok || errno != fuse.Errno(syscall.EROFS) {
		t.Errorf("expected EROFS, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FileHandle Release
// ---------------------------------------------------------------------------

func TestFileHandleRelease(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("A"))
	writeFile(t, dir, "b.bin", []byte("B"))

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	h, _ := vf.Open(context.Background(), &fuse.OpenRequest{}, &fuse.OpenResponse{})
	fh := h.(*FileHandle)
	ctx := context.Background()

	// Grab a file descriptor before release
	f0 := fh.files[0]

	// Release
	if err := fh.Release(ctx, &fuse.ReleaseRequest{}); err != nil {
		t.Fatal(err)
	}

	// Try to read from the closed file
	_, err := f0.ReadAt([]byte{0}, 0)
	if err == nil {
		t.Fatal("expected error reading closed file")
	}
}

// ---------------------------------------------------------------------------
// VirtualFile Setattr (truncate)
// ---------------------------------------------------------------------------

func TestVirtualFileSetattrTruncate(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("AAAA"))  // 4 bytes
	writeFile(t, dir, "b.bin", []byte("BBBB"))  // 4 bytes, writable

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	ctx := context.Background()

	// Valid truncate to baseOff+2 (6 bytes total, last file → 2 bytes)
	req := &fuse.SetattrRequest{Size: 6, Valid: fuse.SetattrSize}
	resp := &fuse.SetattrResponse{}
	if err := vf.Setattr(ctx, req, resp); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "b.bin"))
	if string(data) != "BB" {
		t.Errorf("b.bin = %q, want BB", data)
	}

	// Truncate to baseOff exactly (last file → 0 bytes)
	req2 := &fuse.SetattrRequest{Size: 4, Valid: fuse.SetattrSize}
	if err := vf.Setattr(ctx, req2, resp); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "b.bin"))
	if len(data) != 0 {
		t.Errorf("b.bin len = %d, want 0", len(data))
	}

	// Invalid truncate below baseOff
	req3 := &fuse.SetattrRequest{Size: 3, Valid: fuse.SetattrSize}
	err := vf.Setattr(ctx, req3, resp)
	if err == nil {
		t.Fatal("expected EINVAL error")
	}
	errno, ok := err.(fuse.Errno)
	if !ok || errno != fuse.Errno(syscall.EINVAL) {
		t.Errorf("expected EINVAL, got %v", err)
	}
}

func TestVirtualFileSetattrNoSize(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, dir, "a.bin", []byte("A"))
	writeFile(t, dir, "b.bin", []byte("B"))

	u, _ := NewUnionFS([]string{
		filepath.Join(dir, "a.bin"),
		filepath.Join(dir, "b.bin"),
	}, "out.dat")

	vf := &VirtualFile{fs: u}
	// Setattr without size (e.g. mode change) should be a no-op with no error
	req := &fuse.SetattrRequest{}
	if err := vf.Setattr(context.Background(), req, &fuse.SetattrResponse{}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end mount
// ---------------------------------------------------------------------------

func TestEndToEndMount(t *testing.T) {
	parentDir := tempDir(t)
	writeFile(t, parentDir, "a.bin", []byte("Hello from A!\n"))

	curDir := tempDir(t)
	writeFile(t, curDir, "b.bin", []byte("Hello from B!\n"))

	u, err := NewUnionFS([]string{
		filepath.Join(parentDir, "a.bin"),
		filepath.Join(curDir, "b.bin"),
	}, "out.dat")
	if err != nil {
		t.Fatal(err)
	}

	mnt, err := fstestutil.MountedT(t, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mnt.Close()

	vPath := filepath.Join(mnt.Dir, "out.dat")

	data, err := os.ReadFile(vPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Hello from A!\nHello from B!\n"; string(data) != want {
		t.Errorf("read = %q, want %q", data, want)
	}

	f, err := os.OpenFile(vPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := f.Truncate(u.baseOff); err != nil {
		t.Fatal(err)
	}

	n, err := f.WriteAt([]byte("Modified B!\n"), u.baseOff)
	if err != nil {
		t.Fatal(err)
	}
	if n != len("Modified B!\n") {
		t.Errorf("wrote %d bytes, want %d", n, len("Modified B!\n"))
	}

	data, err = os.ReadFile(vPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Hello from A!\nModified B!\n"; string(data) != want {
		t.Errorf("read after write = %q, want %q", data, want)
	}

	data, err = os.ReadFile(filepath.Join(curDir, "b.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Modified B!\n" {
		t.Errorf("b.bin on disk = %q, want %q", data, "Modified B!\n")
	}

	_, err = f.WriteAt([]byte("X"), 0)
	if err == nil {
		t.Fatal("expected error writing to read-only region")
	}
}
