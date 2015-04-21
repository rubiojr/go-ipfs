package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	cmds "github.com/ipfs/go-ipfs/commands"
	peer "github.com/ipfs/go-ipfs/p2p/peer"
	iaddr "github.com/ipfs/go-ipfs/util/ipfsaddr"

	ma "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multiaddr"
	context "github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"
)

type stringList struct {
	Strings []string
}

type addrMap struct {
	Addrs map[string][]string
}

var SwarmCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "swarm inspection tool",
		Synopsis: `
ipfs swarm peers                - List peers with open connections
ipfs swarm addrs                - List known addresses. Useful to debug.
ipfs swarm connect <address>    - Open connection to a given address
ipfs swarm disconnect <address> - Close connection to a given address
`,
		ShortDescription: `
ipfs swarm is a tool to manipulate the network swarm. The swarm is the
component that opens, listens for, and maintains connections to other
ipfs peers in the internet.
`,
	},
	Subcommands: map[string]*cmds.Command{
		"peers":      swarmPeersCmd,
		"addrs":      swarmAddrsCmd,
		"connect":    swarmConnectCmd,
		"disconnect": swarmDisconnectCmd,
	},
}

var swarmPeersCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "List peers with open connections",
		ShortDescription: `
ipfs swarm peers lists the set of peers this node is connected to.
`,
	},
	Run: func(req cmds.Request, res cmds.Response) {

		log.Debug("ipfs swarm peers")
		n, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		if n.PeerHost == nil {
			res.SetError(errNotOnline, cmds.ErrClient)
			return
		}

		conns := n.PeerHost.Network().Conns()
		addrs := make([]string, len(conns))
		for i, c := range conns {
			pid := c.RemotePeer()
			addr := c.RemoteMultiaddr()
			addrs[i] = fmt.Sprintf("%s/ipfs/%s", addr, pid.Pretty())
		}

		sort.Sort(sort.StringSlice(addrs))
		res.SetOutput(&stringList{addrs})
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: stringListMarshaler,
	},
	Type: stringList{},
}

var swarmAddrsCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "List known addresses. Useful to debug.",
		ShortDescription: `
ipfs swarm addrs lists all addresses this node is aware of.
`,
	},
	Run: func(req cmds.Request, res cmds.Response) {

		n, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		if n.PeerHost == nil {
			res.SetError(errNotOnline, cmds.ErrClient)
			return
		}

		addrs := make(map[string][]string)
		ps := n.PeerHost.Network().Peerstore()
		for _, p := range ps.Peers() {
			s := p.Pretty()
			for _, a := range ps.Addrs(p) {
				addrs[s] = append(addrs[s], a.String())
			}
			sort.Sort(sort.StringSlice(addrs[s]))
		}

		res.SetOutput(&addrMap{Addrs: addrs})
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			m, ok := res.Output().(*addrMap)
			if !ok {
				return nil, errors.New("failed to cast map[string]string")
			}

			// sort the ids first
			ids := make([]string, 0, len(m.Addrs))
			for p := range m.Addrs {
				ids = append(ids, p)
			}
			sort.Sort(sort.StringSlice(ids))

			var buf bytes.Buffer
			for _, p := range ids {
				paddrs := m.Addrs[p]
				buf.WriteString(fmt.Sprintf("%s (%d)\n", p, len(paddrs)))
				for _, addr := range paddrs {
					buf.WriteString("\t" + addr + "\n")
				}
			}
			return &buf, nil
		},
	},
	Type: addrMap{},
}

var swarmConnectCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Open connection to a given address",
		ShortDescription: `
'ipfs swarm connect' opens a connection to a peer address. The address format
is an ipfs multiaddr:

ipfs swarm connect /ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
`,
	},
	Arguments: []cmds.Argument{
		cmds.StringArg("address", true, true, "address of peer to connect to").EnableStdin(),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		ctx := context.TODO()

		n, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		addrs := req.Arguments()

		if n.PeerHost == nil {
			res.SetError(errNotOnline, cmds.ErrClient)
			return
		}

		pis, err := peersWithAddresses(addrs)
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		output := make([]string, len(pis))
		for i, pi := range pis {
			output[i] = "connect " + pi.ID.Pretty()

			err := n.PeerHost.Connect(ctx, pi)
			if err != nil {
				output[i] += " failure: " + err.Error()
			} else {
				output[i] += " success"
			}
		}

		res.SetOutput(&stringList{output})
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: stringListMarshaler,
	},
	Type: stringList{},
}

var swarmDisconnectCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Close connection to a given address",
		ShortDescription: `
'ipfs swarm disconnect' closes a connection to a peer address. The address format
is an ipfs multiaddr:

ipfs swarm disconnect /ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
`,
	},
	Arguments: []cmds.Argument{
		cmds.StringArg("address", true, true, "address of peer to connect to").EnableStdin(),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.Context().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		addrs := req.Arguments()

		if n.PeerHost == nil {
			res.SetError(errNotOnline, cmds.ErrClient)
			return
		}

		iaddrs, err := parseAddresses(addrs)
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		output := make([]string, len(iaddrs))
		for i, addr := range iaddrs {
			taddr := addr.Transport()
			output[i] = "disconnect " + addr.ID().Pretty()

			found := false
			conns := n.PeerHost.Network().ConnsToPeer(addr.ID())
			for _, conn := range conns {
				if !conn.RemoteMultiaddr().Equal(taddr) {
					log.Debug("it's not", conn.RemoteMultiaddr(), taddr)
					continue
				}

				if err := conn.Close(); err != nil {
					output[i] += " failure: " + err.Error()
				} else {
					output[i] += " success"
				}
				found = true
				break
			}

			if !found {
				output[i] += " failure: conn not found"
			}
		}
		res.SetOutput(&stringList{output})
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: stringListMarshaler,
	},
	Type: stringList{},
}

func stringListMarshaler(res cmds.Response) (io.Reader, error) {
	list, ok := res.Output().(*stringList)
	if !ok {
		return nil, errors.New("failed to cast []string")
	}

	var buf bytes.Buffer
	for _, s := range list.Strings {
		buf.WriteString(s)
		buf.WriteString("\n")
	}
	return &buf, nil
}

// parseAddresses is a function that takes in a slice of string peer addresses
// (multiaddr + peerid) and returns slices of multiaddrs and peerids.
func parseAddresses(addrs []string) (iaddrs []iaddr.IPFSAddr, err error) {
	iaddrs = make([]iaddr.IPFSAddr, len(addrs))
	for i, saddr := range addrs {
		iaddrs[i], err = iaddr.ParseString(saddr)
		if err != nil {
			return nil, cmds.ClientError("invalid peer address: " + err.Error())
		}
	}
	return
}

// peersWithAddresses is a function that takes in a slice of string peer addresses
// (multiaddr + peerid) and returns a slice of properly constructed peers
func peersWithAddresses(addrs []string) (pis []peer.PeerInfo, err error) {
	iaddrs, err := parseAddresses(addrs)
	if err != nil {
		return nil, err
	}

	for _, iaddr := range iaddrs {
		pis = append(pis, peer.PeerInfo{
			ID:    iaddr.ID(),
			Addrs: []ma.Multiaddr{iaddr.Transport()},
		})
	}
	return pis, nil
}
