package ipns

import (
	"time"

	context "github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"

	"github.com/ipfs/go-ipfs/core"
	mdag "github.com/ipfs/go-ipfs/merkledag"
	nsys "github.com/ipfs/go-ipfs/namesys"
	ci "github.com/ipfs/go-ipfs/p2p/crypto"
	ft "github.com/ipfs/go-ipfs/unixfs"
)

// InitializeKeyspace sets the ipns record for the given key to
// point to an empty directory.
func InitializeKeyspace(n *core.IpfsNode, key ci.PrivKey) error {
	emptyDir := &mdag.Node{Data: ft.FolderPBData()}
	nodek, err := n.DAG.Add(emptyDir)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute)
	defer cancel()

	err = n.Pinning.Pin(ctx, emptyDir, false)
	if err != nil {
		return err
	}

	err = n.Pinning.Flush()
	if err != nil {
		return err
	}

	pub := nsys.NewRoutingPublisher(n.Routing)
	err = pub.Publish(n.Context(), key, nodek)
	if err != nil {
		return err
	}

	return nil
}
