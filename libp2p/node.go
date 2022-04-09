package libp2p

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2pquic "github.com/libp2p/go-libp2p-quic-transport"
	"github.com/libp2p/go-tcp-transport"
	ma "github.com/multiformats/go-multiaddr"
)

// Protocol is a descriptor for the Hyprspace P2P Protocol.
const Protocol = "/mirage/0.0.1"

// CreateNode creates an internal Libp2p nodes and returns it and it's DHT Discovery service.
func CreateNode(ctx context.Context, inputKey string, port int, handler network.StreamHandler) (node host.Host, dhtOut *dht.IpfsDHT, err error) {
	// Unmarshal Private Key
	privateKey, err := crypto.UnmarshalPrivateKey([]byte(inputKey))
	if err != nil {
		return
	}

	ip6quic := fmt.Sprintf("/ip6/::/udp/%d/quic", port)
	ip4quic := fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic", port)

	ip6tcp := fmt.Sprintf("/ip6/::/tcp/%d", port)
	ip4tcp := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)

	// Create libp2p node
	node, err = libp2p.New(
		libp2p.ListenAddrStrings(ip6quic, ip4quic, ip6tcp, ip4tcp),
		libp2p.Identity(privateKey),
		libp2p.DefaultSecurity,
		libp2p.NATPortMap(),
		libp2p.DefaultMuxers,
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.FallbackDefaults,
	)
	if err != nil {
		return
	}

	// Setup Hyprspace Stream Handler
	node.SetStreamHandler(Protocol, handler)

	// Create DHT Subsystem
	dhtOut = dht.NewDHTClient(ctx, node, datastore.NewMapDatastore())

	return node, dhtOut, nil
}
