package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"

	b58 "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-base58"

	cmds "github.com/ipfs/go-ipfs/commands"
	core "github.com/ipfs/go-ipfs/core"
	nsys "github.com/ipfs/go-ipfs/namesys"
	crypto "github.com/ipfs/go-ipfs/p2p/crypto"
	path "github.com/ipfs/go-ipfs/path"
	u "github.com/ipfs/go-ipfs/util"
)

var errNotOnline = errors.New("This command must be run in online mode. Try running 'ipfs daemon' first.")

var publishCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Publish an object to IPNS",
		ShortDescription: `
IPNS is a PKI namespace, where names are the hashes of public keys, and
the private key enables publishing new (signed) values. In publish, the
default value of <name> is your own identity public key.
`,
		LongDescription: `
IPNS is a PKI namespace, where names are the hashes of public keys, and
the private key enables publishing new (signed) values. In publish, the
default value of <name> is your own identity public key.

Examples:

Publish an <ipfs-path> to your identity name:

  > ipfs name publish /ipfs/QmatmE9msSfkKxoffpHwNLNKgwZG8eT9Bud6YoPab52vpy
  published name QmbCMUZw6JFeZ7Wp9jkzbye3Fzp2GGcPgC3nmeUjfVF87n to QmatmE9msSfkKxoffpHwNLNKgwZG8eT9Bud6YoPab52vpy

Publish an <ipfs-path> to another public key (not implemented):

  > ipfs name publish QmbCMUZw6JFeZ7Wp9jkzbye3Fzp2GGcPgC3nmeUjfVF87n QmatmE9msSfkKxoffpHwNLNKgwZG8eT9Bud6YoPab52vpy
  published name QmbCMUZw6JFeZ7Wp9jkzbye3Fzp2GGcPgC3nmeUjfVF87n to QmatmE9msSfkKxoffpHwNLNKgwZG8eT9Bud6YoPab52vpy
`,
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("name", false, false, "The IPNS name to publish to. Defaults to your node's peerID"),
		cmds.StringArg("ipfs-path", true, false, "IPFS path of the obejct to be published at <name>").EnableStdin(),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		log.Debug("Begin Publish")
		n, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		if !n.OnlineMode() {
			err := n.SetupOfflineRouting()
			if err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}
		}

		args := req.Arguments()

		if n.Identity == "" {
			res.SetError(errors.New("Identity not loaded!"), cmds.ErrNormal)
			return
		}

		var pstr string

		switch len(args) {
		case 2:
			// name = args[0]
			pstr = args[1]
			res.SetError(errors.New("keychains not yet implemented"), cmds.ErrNormal)
		case 1:
			// name = n.Identity.ID.String()
			pstr = args[0]
		}

		node, err := n.Resolver.ResolvePath(path.FromString(pstr))
		if err != nil {
			res.SetError(fmt.Errorf("failed to resolve path: %v", err), cmds.ErrNormal)
			return
		}

		key, err := node.Key()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		// TODO n.Keychain.Get(name).PrivKey
		output, err := publish(n, n.PrivateKey, key.Pretty())
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}
		res.SetOutput(output)
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			v := res.Output().(*IpnsEntry)
			s := fmt.Sprintf("Published name %s to %s\n", v.Name, v.Value)
			return strings.NewReader(s), nil
		},
	},
	Type: IpnsEntry{},
}

func publish(n *core.IpfsNode, k crypto.PrivKey, ref string) (*IpnsEntry, error) {
	pub := nsys.NewRoutingPublisher(n.Routing)
	val := b58.Decode(ref)
	err := pub.Publish(n.Context(), k, u.Key(val))
	if err != nil {
		return nil, err
	}

	hash, err := k.GetPublic().Hash()
	if err != nil {
		return nil, err
	}

	return &IpnsEntry{
		Name:  u.Key(hash).String(),
		Value: ref,
	}, nil
}
