package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

// ---------------------------------------------------------------------------
// UnionFS  (top-level FS that yields the root directory)
// ---------------------------------------------------------------------------

type UnionFS struct {
	files     []string
	vName     string
	fileSizes []int64 // sizes of first N-1 files (read-only segment sizes)
	baseOff   int64   // sum of fileSizes
}

var _ fs.FS = (*UnionFS)(nil)

func NewUnionFS(files []string, vName string) (*UnionFS, error) {
	abs := make([]string, len(files))
	for i, f := range files {
		a, err := filepath.Abs(f)
		if err != nil {
			return nil, fmt.Errorf("abs %s: %w", f, err)
		}
		abs[i] = a
	}
	sizes := make([]int64, len(abs)-1)
	for i := 0; i < len(abs)-1; i++ {
		fi, err := os.Stat(abs[i])
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", abs[i], err)
		}
		sizes[i] = fi.Size()
	}
	return &UnionFS{
		files:     abs,
		vName:     "/" + vName,
		fileSizes: sizes,
		baseOff:   sum(sizes),
	}, nil
}

func mustNewUnionFS(files []string, vName string) *UnionFS {
	u, err := NewUnionFS(files, vName)
	if err != nil {
		panic(err)
	}
	return u
}

func (u *UnionFS) Root() (fs.Node, error) {
	return &RootDir{fs: u}, nil
}

// ---------------------------------------------------------------------------
// RootDir  (directory node containing only the virtual file)
// ---------------------------------------------------------------------------

type RootDir struct {
	fs *UnionFS
}

var _ fs.Node = (*RootDir)(nil)

func (d *RootDir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Mode = os.ModeDir | 0o755
	a.Inode = 1
	return nil
}

var _ fs.NodeStringLookuper = (*RootDir)(nil)

func (d *RootDir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	if name == d.fs.vName[1:] {
		return &VirtualFile{fs: d.fs}, nil
	}
	return nil, fuse.ENOENT
}

var _ fs.HandleReadDirAller = (*RootDir)(nil)

func (d *RootDir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	return []fuse.Dirent{
		{Name: "."},
		{Name: ".."},
		{Name: d.fs.vName[1:], Type: fuse.DT_File},
	}, nil
}

// ---------------------------------------------------------------------------
// VirtualFile  (the unioned file node — getattr, open, truncate)
// ---------------------------------------------------------------------------

type VirtualFile struct {
	fs *UnionFS
}

var _ fs.Node = (*VirtualFile)(nil)

func (vf *VirtualFile) Attr(ctx context.Context, a *fuse.Attr) error {
	fi, err := os.Stat(vf.fs.files[len(vf.fs.files)-1])
	if err != nil {
		return fuse.ENOENT
	}
	a.Mode = 0o644
	a.Size = uint64(vf.fs.baseOff + fi.Size())
	a.Mtime = fi.ModTime()
	return nil
}

var _ fs.NodeOpener = (*VirtualFile)(nil)

func (vf *VirtualFile) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	fds := make([]*os.File, len(vf.fs.files))
	for i, path := range vf.fs.files {
		var f *os.File
		var err error
		if i == len(vf.fs.files)-1 {
			if req.Flags&fuse.OpenWriteOnly != 0 || req.Flags&fuse.OpenReadWrite != 0 {
				f, err = os.OpenFile(path, os.O_RDWR, 0)
			} else {
				f, err = os.Open(path)
			}
		} else {
			f, err = os.Open(path)
		}
		if err != nil {
			for j := 0; j < i; j++ {
				fds[j].Close()
			}
			return nil, fuse.Errno(syscall.EIO)
		}
		fds[i] = f
	}

	return &FileHandle{files: fds, baseOff: vf.fs.baseOff, fileSizes: vf.fs.fileSizes}, nil
}

var _ fs.NodeSetattrer = (*VirtualFile)(nil)

func (vf *VirtualFile) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	if req.Valid.Size() {
		length := int64(req.Size)

		if length < vf.fs.baseOff {
			return fuse.Errno(syscall.EINVAL)
		}
		targetLen := length - vf.fs.baseOff

		f, err := os.OpenFile(vf.fs.files[len(vf.fs.files)-1], os.O_RDWR, 0)
		if err != nil {
			return fuse.Errno(syscall.EIO)
		}
		err = f.Truncate(targetLen)
		f.Close()
		if err != nil {
			return fuse.Errno(syscall.EIO)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// FileHandle  (open file handle — read, write, release)
// ---------------------------------------------------------------------------

type FileHandle struct {
	files     []*os.File
	baseOff   int64
	fileSizes []int64
}

var _ fs.Handle = (*FileHandle)(nil)
var _ fs.HandleReader = (*FileHandle)(nil)
var _ fs.HandleWriter = (*FileHandle)(nil)
var _ fs.HandleReleaser = (*FileHandle)(nil)

func (fh *FileHandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	if req.Size == 0 {
		return nil
	}

	resp.Data = make([]byte, 0, req.Size)
	remaining := int64(req.Size)
	cursor := req.Offset
	bound := int64(0)

	for i, f := range fh.files {
		var fSize int64
		if i == len(fh.files)-1 {
			fi, err := f.Stat()
			if err != nil {
				return fuse.Errno(syscall.EIO)
			}
			fSize = fi.Size()
		} else {
			fSize = fh.fileSizes[i]
		}

		if cursor < bound+fSize {
			fileOff := cursor - bound
			chunk := fSize - fileOff
			if remaining < chunk {
				chunk = remaining
			}

			buf := make([]byte, chunk)
			n, err := f.ReadAt(buf, fileOff)
			if n > 0 {
				resp.Data = append(resp.Data, buf[:n]...)
				remaining -= int64(n)
				cursor += int64(n)
			}
			if err != nil && err != io.EOF {
				return fuse.Errno(syscall.EIO)
			}
			if remaining <= 0 {
				break
			}
		}
		bound += fSize
	}
	return nil
}

func (fh *FileHandle) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	if req.Offset < fh.baseOff {
		return fuse.Errno(syscall.EROFS)
	}

	targetOff := req.Offset - fh.baseOff
	last := fh.files[len(fh.files)-1]

	n, err := last.WriteAt(req.Data, targetOff)
	if err != nil {
		return fuse.Errno(syscall.EIO)
	}
	resp.Size = n
	return nil
}

func (fh *FileHandle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	var firstErr error
	for _, f := range fh.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fuse.Errno(syscall.EIO)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func sum(s []int64) int64 {
	var t int64
	for _, v := range s {
		t += v
	}
	return t
}
