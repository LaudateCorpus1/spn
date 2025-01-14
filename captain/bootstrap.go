package captain

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/safing/portbase/formats/dsd"
	"github.com/safing/portbase/log"
	"github.com/safing/spn/conf"
	"github.com/safing/spn/hub"
	"github.com/safing/spn/navigator"
)

type BootstrapFile struct {
	Main BootstrapFileEntry
}

type BootstrapFileEntry struct {
	Hubs []string
}

var (
	bootstrapHubFlag  string
	bootstrapFileFlag string
)

func init() {
	flag.StringVar(&bootstrapHubFlag, "bootstrap-hub", "", "transport address of hub for bootstrapping with the hub ID in the fragment")
	flag.StringVar(&bootstrapFileFlag, "bootstrap-file", "", "bootstrap file containing bootstrap hubs - will be initialized if running a public hub and it doesn't exist")
}

// prepBootstrapHubFlag checks the bootstrap-hub argument if it is valid.
func prepBootstrapHubFlag() error {
	if bootstrapHubFlag != "" {
		_, err := hub.ParseBootstrapHub(bootstrapHubFlag, conf.MainMapName)
		return err
	}
	return nil
}

// processBootstrapHubFlag processes the bootstrap-hub argument.
func processBootstrapHubFlag() error {
	if bootstrapHubFlag != "" {
		return navigator.Main.AddBootstrapHubs([]string{bootstrapHubFlag})
	}
	return nil
}

// processBootstrapFileFlag processes the bootstrap-file argument.
func processBootstrapFileFlag() error {
	if bootstrapFileFlag == "" {
		return nil
	}

	_, err := os.Stat(bootstrapFileFlag)
	if err != nil {
		if os.IsNotExist(err) {
			return createBootstrapFile(bootstrapFileFlag)
		} else {
			return fmt.Errorf("failed to access bootstrap hub file: %w", err)
		}
	}

	return loadBootstrapFile(bootstrapFileFlag)
}

// bootstrapWithUpdates loads bootstrap hubs from the updates server and imports them.
func bootstrapWithUpdates() error {
	if bootstrapFileFlag != "" {
		return errors.New("using the bootstrap-file argument disables bootstrapping via the update system")
	}

	return updateSPNIntel(module.Ctx, nil)
}

// loadBootstrapFile loads a file with bootstrap hub entries and imports them.
func loadBootstrapFile(filename string) (err error) {
	// Load bootstrap file from disk and parse it.
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap file: %w", err)
	}
	bootstrapFile := &BootstrapFile{}
	_, err = dsd.Load(data, bootstrapFile)
	if err != nil {
		return fmt.Errorf("failed to parse bootstrap file: %w", err)
	}
	if len(bootstrapFile.Main.Hubs) == 0 {
		return errors.New("bootstrap holds no hubs for main map")
	}

	// Add Hubs to map.
	err = navigator.Main.AddBootstrapHubs(bootstrapFile.Main.Hubs)
	if err == nil {
		log.Infof("spn/captain: loaded bootstrap file %s", filename)
	}
	return err
}

// createBootstrapFile save a bootstrap hub file with an entry of the public identity.
func createBootstrapFile(filename string) error {
	if !conf.PublicHub() {
		log.Infof("spn/captain: skipped writing a bootstrap hub file, as this is not a public hub")
		return nil
	}

	// create bootstrap hub
	if len(publicIdentity.Hub.Info.Transports) == 0 {
		return errors.New("public identity has no transports available")
	}
	// parse first transport
	t, err := hub.ParseTransport(publicIdentity.Hub.Info.Transports[0])
	if err != nil {
		return fmt.Errorf("failed to parse transport of public identity: %w", err)
	}
	// add IP address
	if publicIdentity.Hub.Info.IPv4 != nil {
		t.Domain = publicIdentity.Hub.Info.IPv4.String()
	} else if publicIdentity.Hub.Info.IPv6 != nil {
		t.Domain = "[" + publicIdentity.Hub.Info.IPv6.String() + "]"
	} else {
		return errors.New("public identity has no IP address available")
	}
	// add Hub ID
	t.Option = publicIdentity.Hub.ID
	// put together
	bs := &BootstrapFile{
		Main: BootstrapFileEntry{
			Hubs: []string{t.String()},
		},
	}

	// serialize
	fileData, err := dsd.Dump(bs, dsd.JSON)
	if err != nil {
		return err
	}

	// save to disk
	err = ioutil.WriteFile(filename, fileData, 0664)
	if err != nil {
		return err
	}

	log.Infof("spn/captain: created bootstrap file %s", filename)
	return nil
}
