package corerepo

import (
	"fmt"
	"time"

	context "github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"

	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/merkledag"
	path "github.com/ipfs/go-ipfs/path"
	u "github.com/ipfs/go-ipfs/util"
)

func Pin(n *core.IpfsNode, paths []string, recursive bool) ([]u.Key, error) {

	dagnodes := make([]*merkledag.Node, 0)
	for _, fpath := range paths {
		dagnode, err := core.Resolve(n, path.Path(fpath))
		if err != nil {
			return nil, fmt.Errorf("pin: %s", err)
		}
		dagnodes = append(dagnodes, dagnode)
	}

	var out []u.Key
	for _, dagnode := range dagnodes {
		k, err := dagnode.Key()
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(context.TODO(), time.Minute)
		defer cancel()
		err = n.Pinning.Pin(ctx, dagnode, recursive)
		if err != nil {
			return nil, fmt.Errorf("pin: %s", err)
		}
		out = append(out, k)
	}

	err := n.Pinning.Flush()
	if err != nil {
		return nil, err
	}

	return out, nil
}

func Unpin(n *core.IpfsNode, paths []string, recursive bool) ([]u.Key, error) {

	dagnodes := make([]*merkledag.Node, 0)
	for _, fpath := range paths {
		dagnode, err := core.Resolve(n, path.Path(fpath))
		if err != nil {
			return nil, err
		}
		dagnodes = append(dagnodes, dagnode)
	}

	var unpinned []u.Key
	for _, dagnode := range dagnodes {
		k, _ := dagnode.Key()

		ctx, cancel := context.WithTimeout(context.TODO(), time.Minute)
		defer cancel()
		err := n.Pinning.Unpin(ctx, k, recursive)
		if err != nil {
			return nil, err
		}
		unpinned = append(unpinned, k)
	}

	err := n.Pinning.Flush()
	if err != nil {
		return nil, err
	}
	return unpinned, nil
}
