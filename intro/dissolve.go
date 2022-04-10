package intro

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/DataDrake/cli-ng/v2/cmd"
	"mirage.com/tun_int"
)

// Dissolve brings down a Mirage interface and removes it from the system.
var Dissolve = cmd.Sub{
	Name:  "dissolve",
	Alias: "d",
	Short: "Bring Down A Mirage Interface.",
	Args:  &DissolveArgs{},
	Run:   DissolveRun,
}

// DissolveArgs handles the specific arguments for the dissolve command.
type DissolveArgs struct {
	InterfaceName string
}

// DissolveRun handles the execution of the dissolve command.
func DissolveRun(r *cmd.Root, c *cmd.Sub) {
	// Parse Command Args
	args := c.Args.(*DissolveArgs)

	// Parse Global Config Flag for Custom Config Path
	configPath := r.Flags.(*GlobalFlags).Config
	if configPath == "" {
		configPath = "/etc/mirage/" + args.InterfaceName + ".yaml"
	}

	// Read lock from file system to stop process.
	lockPath := filepath.Join(filepath.Dir(configPath), args.InterfaceName+".lock")
	out, err := os.ReadFile(lockPath)
	checkErr(err)

	pid, err := strconv.Atoi(string(out))
	checkErr(err)

	process, err := os.FindProcess(pid)
	checkErr(err)

	err0 := process.Signal(os.Interrupt)

	err1 := tun_int.Delete(args.InterfaceName)

	// Different types of systems may need the tun devices destroyed first or
	// the process to exit first don't worry as long as one of these two has
	// succeeded.
	if err0 != nil && err1 != nil {
		checkErr(err0)
		checkErr(err1)
	}

	fmt.Println("[+] deleted mirage " + args.InterfaceName + " daemon")
}

