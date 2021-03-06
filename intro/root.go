package intro

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DataDrake/cli-ng/v2/cmd"
)

var appVersion string = "0.0.1"

//GlobalFlags contains the flags for commands.
type GlobalFlags struct {
	Config string `short:"c" long:"config" desc:"Specify a custom config path."`
}

// Root is the main command.
var Root *cmd.Root

func init() {
	Root = &cmd.Root{
		Name:    "mirage",
		Short:   "mirage",
		Version: appVersion,
		Flags:   &GlobalFlags{},
	}

	cmd.Register(&cmd.Help)
	cmd.Register(&Init)
	cmd.Register(&Form)
	cmd.Register(&Dissolve)
}

func checkErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// Spinner is an array of the progression of the spinner.
var Spinner = []string{"|", "/", "-", "\\"}

// SpinnerWait displays the actual spinner
func SpinnerWait(done chan int, message string, wg *sync.WaitGroup) {
	ticker := time.NewTicker(time.Millisecond * 128)
	frameCounter := 0
	for {
		select {
		case <-done:
			wg.Done()
			return
		default:
			<-ticker.C
			ind := frameCounter % len(Spinner)
			fmt.Printf("\r[%v] "+message, Spinner[ind])
			frameCounter++
		}
	}
}
