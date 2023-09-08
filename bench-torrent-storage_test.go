package squirrel

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"

	qt "github.com/frankban/quicktest"
)

const defaultPieceSize = 2 << 20

func BenchmarkRandRead(b *testing.B) {
	var piece [defaultPieceSize]byte
	b.SetBytes(defaultPieceSize)
	for i := 0; i < b.N; i++ {
		readRandSlow(piece[:])
	}
}

func BenchmarkRandReadSparse(b *testing.B) {
	var piece [defaultPieceSize]byte
	b.SetBytes(defaultPieceSize)
	for i := 0; i < b.N; i++ {
		readRandSparse(piece[:])
	}
}

type offIterFunc = func() (uint32, bool)

type readHashAndTagFunc = func(
	cache *Cache,
	key []byte,
	offIter offIterFunc,
	pieceSize int,
	// A buffer for use between runs at least as big as the piece. Optional to use it. Not requiring
	// it might be a feature of an implementation.
	buf []byte,
	hash io.Writer,
) error

type pieceWriteFunc = func(cache *Cache, key []byte, off uint32, b []byte, pieceSize uint32) error

const (
	logTorrentStorageBenchmarkDbPaths = false
)

func benchmarkTorrentStorage(
	b *testing.B,
	cacheOpts NewCacheOpts,
	pieceWrite pieceWriteFunc,
	readAndHash readHashAndTagFunc,
) {
	if logTorrentStorageBenchmarkDbPaths {
		b.Logf("db path: %q", cacheOpts.Path)
	}
	const chunkSize = 1 << 14
	const pieceSize = 2 << 20
	//const chunkSize = 20
	//const pieceSize = 2560
	var key [20]byte
	readRandSlow(key[:])
	benchCache(
		b,
		cacheOpts,
		func(cache *Cache) error {
			return nil
		},
		func(cache *Cache) error {
			var piece [pieceSize]byte
			readRandSparse(piece[:])
			h0 := newFastestHash()
			h0.Write(piece[:])
			makeOffIter := func() func() (uint32, bool) {
				nextOff := uint32(0)
				return func() (off uint32, ok bool) {
					if nextOff >= pieceSize {
						return 0, false
					}
					off = nextOff
					ok = true
					nextOff += chunkSize
					return
				}
			}
			offIter := makeOffIter()
			for {
				off, ok := offIter()
				if !ok {
					break
				}
				err := pieceWrite(cache, key[:], off, piece[off:off+chunkSize], pieceSize)
				if err != nil {
					panic(err)
				}
			}
			h1 := newFastestHash()
			err := readAndHash(cache, key[:], makeOffIter(), pieceSize, piece[:], h1)
			if err != nil {
				panic(err)
			}
			if h0.Sum32() != h1.Sum32() {
				b.Fatal("hashes don't match")
			}
			return nil
		},
	)
	b.SetBytes(pieceSize)
	b.ReportMetric(float64((pieceSize+chunkSize-1)/chunkSize*b.N)/b.Elapsed().Seconds(), "chunks/s")
}

func writeChunksSeparately(cache *Cache, key []byte, off uint32, b []byte, pieceSize uint32) error {
	key = binary.BigEndian.AppendUint32(key, off)
	return cache.Put(string(key), b)
}

func writeToOneBigPiece(cache *Cache, key []byte, off uint32, b []byte, pieceSize uint32) error {
	blob := cache.BlobWithLength(string(key), int64(pieceSize))
	n, err := blob.WriteAt(b, int64(off))
	if err != nil {
		panic(err)
	}
	if n != len(b) {
		panic(n)
	}
	return err
}

func readAndHashSeparateChunks(
	cache *Cache,
	key []byte,
	offIter offIterFunc,
	pieceSize int,
	buf []byte,
	hash io.Writer) error {
	for {
		off, ok := offIter()
		if !ok {
			break
		}
		chunkKey := binary.BigEndian.AppendUint32(key, off)
		n, err := cache.ReadFull(string(chunkKey), buf)
		if err != nil {
			panic(err)
		}
		hash.Write(buf[:n])
		err = cache.SetTag(string(chunkKey), "verified", true)
		if err != nil {
			panic(err)
		}
	}
	return nil
}

func readHashAndTagOneBigPiece(
	cache *Cache,
	key []byte,
	offIter offIterFunc,
	pieceSize int,
	buf []byte,
	hash io.Writer,
) (err error) {
	blob, err := cache.OpenPinned(string(key))
	if err != nil {
		panic(err)
	}
	io.Copy(hash, io.NewSectionReader(blob, 0, blob.Length()))
	blob.Close()
	return
}

func BenchmarkTorrentStorage(b *testing.B) {
	c := qt.New(b)
	newCacheOpts := func() NewCacheOpts {
		cacheOpts := defaultCacheOpts(c)
		// Can't start a transaction while blobs are cached.
		cacheOpts.NoCacheBlobs = true
		cacheOpts.SetJournalMode = "wal"
		//cacheOpts.Path = newCachePath(c, "testdbs")
		//cacheOpts.Path = ""
		//cacheOpts.MmapSize = 64 << 20
		//cacheOpts.MmapSizeOk = true
		//cacheOpts.Capacity = 4 << 20
		cacheOpts.SetLockingMode = "exclusive"
		// The triggers are problematic as they're not handling large blob counts properly.
		cacheOpts.NoTriggers = true
		return cacheOpts
	}
	benchmarkTorrentStorageVaryingChunksPiecesTransactions(b, newCacheOpts)
}

func benchmarkTorrentStorageVaryingChunksPiecesTransactions(
	b *testing.B,
	newCacheOpts func() NewCacheOpts,
) {
	b.Run("IndividualChunks", func(b *testing.B) {
		benchmarkTorrentStorage(
			b,
			newCacheOpts(),
			writeChunksSeparately,
			readAndHashSeparateChunks,
		)
	})
	b.Run("IndividualChunksTransaction", func(b *testing.B) {
		cacheOpts := newCacheOpts()
		if !cacheOpts.NoCacheBlobs {
			b.Skip("can't use transactions with cached blobs")
		}
		benchmarkTorrentStorage(
			b,
			cacheOpts,
			writeChunksSeparately,
			func(
				cache *Cache,
				key []byte,
				offIter offIterFunc,
				pieceSize int,
				buf []byte,
				hash io.Writer,
			) (err error) {
				err = errors.Join(
					cache.Tx(
						func() bool {
							err = readAndHashSeparateChunks(cache, key, offIter, pieceSize, buf, hash)
							return true
						}),
					err)
				return
			},
		)
	})
	b.Run("OneBigPiece", func(b *testing.B) {
		benchmarkTorrentStorage(
			b,
			newCacheOpts(),
			writeToOneBigPiece,
			readHashAndTagOneBigPiece,
		)
	})
}
