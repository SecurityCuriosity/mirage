package intro

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataDrake/cli-ng/v2/cmd"
	"mirage.com/configs"
	"mirage.com/libp2p"
	tun "mirage.com/tun_int"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/nxadm/tail"
)

var (
	// iface is the tun device used to pass packets between
	// Hyprspace and the user's machine.
	tunDev *tun.TUN
	// RevLookup allow quick lookups of an incoming stream
	// for security before accepting or responding to any data.
	RevLookup map[string]string
	// activeStreams is a map of active streams to a peer
	activeStreams map[string]network.Stream
)

// Form creates and brings up a TUN Interface.
var Form = cmd.Sub{
	Name:  "form",
	Alias: "form",
	Short: "Create and Bring Up a TUN Interface.",
	Args:  &FormArgs{},
	Flags: &FormFlags{},
	Run:   FormRun,
}

// FormArgs handles the specific arguments for the Form command.
type FormArgs struct {
	InterfaceName string
}

// FormFlags handles the specific flags for the Form command.
type FormFlags struct {
	Foreground bool `short:"f" long:"foreground" desc:"Don't Create Background Daemon."`
}

// FormRun handles the execution of the Form command.
func FormRun(r *cmd.Root, c *cmd.Sub) {
	// Parse Command Args
	args := c.Args.(*FormArgs)

	// Parse Command Flags
	flags := c.Flags.(*FormFlags)

	// Parse Global Config Flag for Custom Config Path
	configPath := r.Flags.(*GlobalFlags).Config
	if configPath == "" {
		configPath = "/etc/mirage/" + args.InterfaceName + ".yaml"
	}

	// Read in configuration from file.
	cfg, err := configs.Read(configPath)
	checkErr(err)

	// Daemonize so that the process does not lock the terminal
	if !flags.Foreground {
		if err := createDaemon(cfg); err != nil {
			fmt.Println("[+] Failed to Create Mirage Daemon")
			fmt.Println(err)
		} else {
			fmt.Println("[+] Successfully Created Mirage Daemon")
		}
		return
	}

	// Setup reverse lookup hash map for authentication.
	RevLookup = make(map[string]string, len(cfg.Peers))
	for ip, id := range cfg.Peers {
		RevLookup[id.ID] = ip
	}

	fmt.Println("[+] Creating TUN Device")

	// Create new TUN device
	tunDev, err = tun.New(
		cfg.Interface.Name,
		tun.Address(cfg.Interface.Address),
		tun.MTU(1420),
	)

	if err != nil {
		checkErr(err)
	}

	// Setup System Context
	ctx := context.Background()

	fmt.Println("[+] Creating LibP2P Node")

	// Check that the listener port is available.
	port, err := verifyPort(cfg.Interface.ListenPort)
	checkErr(err)

	// Create P2P Node
	host, dht, err := libp2p.CreateNode(
		ctx,
		cfg.Interface.PrivateKey,
		port,
		streamHandler,
	)
	checkErr(err)

	// Setup Peer Table for Quick Packet --> Dest ID lookup
	peerTable := make(map[string]peer.ID)
	for ip, id := range cfg.Peers {
		peerTable[ip], err = peer.Decode(id.ID)
		checkErr(err)
	}

	// **WIP** Should allow for discovery of new nodes that come online...work in progress	
	fmt.Println("[+] Setting Up Node Discovery via DHT")

	// Setup P2P Discovery
	go libp2p.Discover(ctx, host, dht, peerTable)
	go prettyDiscovery(ctx, host, peerTable)

	// Configure path for lock - this is stop multiple processes from interacting with a single file
	lockPath := filepath.Join(filepath.Dir(cfg.Path), cfg.Interface.Name+".lock")

	// Register the application to listen for SIGINT/SIGTERM
	go signalExit(host, lockPath)

	// Write lock to filesystem to indicate an existing running daemon.
	err = os.WriteFile(lockPath, []byte(fmt.Sprint(os.Getpid())), os.ModePerm)
	checkErr(err)

	// Bring Up TUN Device
	err = tunDev.Up()
	if err != nil {
		checkErr(errors.New("unable to bring up tun device"))
	}

	fmt.Println("[+] Network Setup Complete...Waiting on Node Discovery")

	// + ----------------------------------------+
	// | Listen For New Packets on TUN Interface |
	// + ----------------------------------------+

	// Initialize active streams map and packet byte array.
	activeStreams = make(map[string]network.Stream)
	var packet = make([]byte, 1420)
	for {
		// Read in a packet from the tun device.
		plen, err := tunDev.Iface.Read(packet)
		if err != nil {
			log.Println(err)
			continue
		}

		// Decode the packet's destination address
		dst := net.IPv4(packet[16], packet[17], packet[18], packet[19]).String()

		// Check if we already have an open connection to the destination peer.
		stream, ok := activeStreams[dst]
		if ok {
			// Write out the packet's length to the libp2p stream to ensure
			// we know the full size of the packet at the other end.
			err = binary.Write(stream, binary.LittleEndian, uint16(plen))
			if err == nil {
				// Write the packet out to the libp2p stream.
				// If everyting succeeds continue on to the next packet.
				_, err = stream.Write(packet[:plen])
				if err == nil {
					continue
				}
			}
			// If we encounter an error when writing to a stream we should
			// close that stream and delete it from the active stream map.
			stream.Close()
			delete(activeStreams, dst)
		}

		// Check if the destination of the packet is a known peer to
		// the interface.
		if peer, ok := peerTable[dst]; ok {
			stream, err = host.NewStream(ctx, peer, libp2p.Protocol)
			if err != nil {
				continue
			}
			// Write packet length
			err = binary.Write(stream, binary.LittleEndian, uint16(plen))
			if err != nil {
				stream.Close()
				continue
			}
			// Write the packet
			_, err = stream.Write(packet[:plen])
			if err != nil {
				stream.Close()
				continue
			}

			// If all succeeds when writing the packet to the stream
			// we should reuse this stream by adding it active streams map.
			activeStreams[dst] = stream
		}
	}
}

// singalExit registers two syscall handlers on the system  so that if
// an SIGINT or SIGTERM occur on the system hyprspace can gracefully
// shutdown and remove the filesystem lock file.
func signalExit(host host.Host, lockPath string) {
	// Wait for a SIGINT or SIGTERM signal
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	// Shut the node down
	err := host.Close()
	checkErr(err)

	// Remove daemon lock from file system.
	err = os.Remove(lockPath)
	checkErr(err)

	fmt.Println("Received signal, shutting down...")

	// Exit the application.
	os.Exit(0)
}

// createDaemon handles creating an independent background process
// Not 100% on how all of this works - tons of googling
func createDaemon(cfg *configs.Config) error {
	path, err := os.Executable()
	checkErr(err)

	// Generate log path
	logPath := filepath.Join(filepath.Dir(cfg.Path), cfg.Interface.Name+".log")

	// Create Pipe to monitor for daemon output.
	f, err := os.Create(logPath)
	checkErr(err)

	// Create Sub Process
	process, err := os.StartProcess(
		path,
		append(os.Args, "--foreground"),
		&os.ProcAttr{
			Dir:   ".",
			Env:   os.Environ(),
			Files: []*os.File{nil, f, f},
		},
	)
	checkErr(err)

	// Listen to the child process's log output to determine
	// when the daemon is setup and connected to a set of peers.
	count := 0
	deadlineHit := false
	countChan := make(chan int)
	go func(out chan<- int) {
		numConnected := 0
		t, err := tail.TailFile(logPath, tail.Config{Follow: true})
		if err != nil {
			out <- numConnected
			return
		}
		for line := range t.Lines {
			fmt.Println(line.Text)
			if strings.HasPrefix(line.Text, "[+] Connection to") {
				numConnected++
				if numConnected >= len(cfg.Peers) {
					break
				}
			}
		}
		out <- numConnected
	}(countChan)

	// Block until all clients are connected or for a maximum of 30s.
	select {
	case _, deadlineHit = <-time.After(30 * time.Second):
	case count = <-countChan:
	}

	// Release the created daemon
	err = process.Release()
	checkErr(err)

	// Check if the daemon exited prematurely
	if !deadlineHit && count < len(cfg.Peers) {
		return errors.New("failed to create daemon")
	}
	return nil
}

func streamHandler(stream network.Stream) {
	// If the remote node ID isn't in the list of known nodes don't respond.
	if _, ok := RevLookup[stream.Conn().RemotePeer().Pretty()]; !ok {
		stream.Reset()
		return
	}
	var packet = make([]byte, 1420)
	var packetSize = make([]byte, 2)
	for {
		// Read the incoming packet's size as a binary value.
		_, err := stream.Read(packetSize)
		if err != nil {
			stream.Close()
			return
		}

		// Decode the incoming packet's size from binary.
		size := binary.LittleEndian.Uint16(packetSize)

		// Read in the packet until completion.
		var plen uint16 = 0
		for plen < size {
			tmp, err := stream.Read(packet[plen:size])
			plen += uint16(tmp)
			if err != nil {
				stream.Close()
				return
			}
		}
		tunDev.Iface.Write(packet[:size])
	}
}

func prettyDiscovery(ctx context.Context, node host.Host, peerTable map[string]peer.ID) {
	// Build a temporary map of peers to limit querying to only those
	// not connected.
	tempTable := make(map[string]peer.ID, len(peerTable))
	for ip, id := range peerTable {
		tempTable[ip] = id
	}
	for len(tempTable) > 0 {
		for ip, id := range tempTable {
			stream, err := node.NewStream(ctx, id, libp2p.Protocol)
			if err != nil && (strings.HasPrefix(err.Error(), "failed to dial") ||
				strings.HasPrefix(err.Error(), "no addresses")) {
				// Attempt to connect to peers slowly when they aren't found.
				time.Sleep(5 * time.Second)
				continue
			}
			if err == nil {
				fmt.Printf("[+] Connection to %s Successful. Network Ready.\n", ip)
				stream.Close()
			}
			delete(tempTable, ip)
		}
	}
}

func verifyPort(port int) (int, error) {
	var ln net.Listener
	var err error

	// If a user manually sets a port don't try to automatically
	// find an open port.
	if port != 7788 {
		ln, err = net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return port, errors.New("could not create node, listen port already in use by something else")
		}
	} else {
		// Automatically look for an open port when a custom port isn't
		// selected by a user.
		for {
			ln, err = net.Listen("tcp", ":"+strconv.Itoa(port))
			if err == nil {
				break
			}
			if port >= 65535 {
				return port, errors.New("failed to find open port")
			}
			port++
		}
	}
	if ln != nil {
		ln.Close()
	}
	return port, nil
}
