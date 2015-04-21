// package blockstore implements a thin wrapper over a datastore, giving a
// clean interface for Getting and Putting block objects.
package blockstore

import (
	"errors"

	ds "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore"
	dsns "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/namespace"
	dsq "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/query"
	mh "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multihash"
	context "github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"
	blocks "github.com/ipfs/go-ipfs/blocks"
	eventlog "github.com/ipfs/go-ipfs/thirdparty/eventlog"
	u "github.com/ipfs/go-ipfs/util"
)

var log = eventlog.Logger("blockstore")

// BlockPrefix namespaces blockstore datastores
var BlockPrefix = ds.NewKey("blocks")

var ValueTypeMismatch = errors.New("The retrieved value is not a Block")

var ErrNotFound = errors.New("blockstore: block not found")

// Blockstore wraps a ThreadSafeDatastore
type Blockstore interface {
	DeleteBlock(u.Key) error
	Has(u.Key) (bool, error)
	Get(u.Key) (*blocks.Block, error)
	Put(*blocks.Block) error

	AllKeysChan(ctx context.Context) (<-chan u.Key, error)
}

func NewBlockstore(d ds.ThreadSafeDatastore) Blockstore {
	dd := dsns.Wrap(d, BlockPrefix)
	return &blockstore{
		datastore: dd,
	}
}

type blockstore struct {
	datastore ds.Datastore
	// cant be ThreadSafeDatastore cause namespace.Datastore doesnt support it.
	// we do check it on `NewBlockstore` though.
}

func (bs *blockstore) Get(k u.Key) (*blocks.Block, error) {
	maybeData, err := bs.datastore.Get(k.DsKey())
	if err == ds.ErrNotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	bdata, ok := maybeData.([]byte)
	if !ok {
		return nil, ValueTypeMismatch
	}

	return blocks.NewBlockWithHash(bdata, mh.Multihash(k))
}

func (bs *blockstore) Put(block *blocks.Block) error {
	// Has is cheaper than
	k := block.Key().DsKey()
	exists, err := bs.datastore.Has(k)
	if err != nil && exists {
		return nil // already stored.
	}
	return bs.datastore.Put(k, block.Data)
}

func (bs *blockstore) Has(k u.Key) (bool, error) {
	return bs.datastore.Has(k.DsKey())
}

func (s *blockstore) DeleteBlock(k u.Key) error {
	return s.datastore.Delete(k.DsKey())
}

// AllKeysChan runs a query for keys from the blockstore.
// this is very simplistic, in the future, take dsq.Query as a param?
//
// AllKeysChan respects context
func (bs *blockstore) AllKeysChan(ctx context.Context) (<-chan u.Key, error) {

	// KeysOnly, because that would be _a lot_ of data.
	q := dsq.Query{KeysOnly: true}
	// datastore/namespace does *NOT* fix up Query.Prefix
	q.Prefix = BlockPrefix.String()
	res, err := bs.datastore.Query(q)
	if err != nil {
		return nil, err
	}

	// this function is here to compartmentalize
	get := func() (k u.Key, ok bool) {
		select {
		case <-ctx.Done():
			return k, false
		case e, more := <-res.Next():
			if !more {
				return k, false
			}
			if e.Error != nil {
				log.Debug("blockstore.AllKeysChan got err:", e.Error)
				return k, false
			}

			// need to convert to u.Key using u.KeyFromDsKey.
			k = u.KeyFromDsKey(ds.NewKey(e.Key))
			log.Debug("blockstore: query got key", k)

			// key must be a multihash. else ignore it.
			_, err := mh.Cast([]byte(k))
			if err != nil {
				return "", true
			}

			return k, true
		}
	}

	output := make(chan u.Key)
	go func() {
		defer func() {
			res.Process().Close() // ensure exit (signals early exit, too)
			close(output)
		}()

		for {
			k, ok := get()
			if !ok {
				return
			}
			if k == "" {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case output <- k:
			}
		}
	}()

	return output, nil
}
