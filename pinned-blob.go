package squirrel

import (
	"errors"
	"github.com/go-llsqlite/adapter"
)

// Wraps a specific sqlite.Blob instance, when we don't want to dive into the cache to refetch blobs.
type PinnedBlob struct {
	blob *sqlite.Blob
	c    *Cache
}

func (pb PinnedBlob) Reopen(name string) error {
	pb.c.l.Lock()
	defer pb.c.l.Unlock()
	rowid, _, ok, err := rowidForBlob(pb.c.conn, name)
	// If we fail between here and the reopen, the blob handle remains on the existing row.
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("rowid for name not found")
	}
	// If this fails, the blob handle is aborted.
	return pb.blob.Reopen(rowid)
}

// This is very cheap for this type.
func (pb PinnedBlob) Length() int64 {
	return pb.blob.Size()
}

// Requires only that we lock the sqlite conn.
func (pb PinnedBlob) ReadAt(b []byte, off int64) (int, error) {
	pb.c.l.Lock()
	defer pb.c.l.Unlock()
	return blobReadAt(pb.blob, b, off)
}

func (pb PinnedBlob) Close() error {
	if pb.c.reclaimsBlobs() {
		return nil
	}
	return pb.blob.Close()
}

func (pb PinnedBlob) WriteAt(b []byte, off int64) (int, error) {
	return blobWriteAt(pb.blob, b, off)
}
