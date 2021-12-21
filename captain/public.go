package captain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/safing/portbase/rng"

	"github.com/safing/portbase/metrics"

	"github.com/safing/spn/conf"
	"github.com/safing/spn/docks"
	"github.com/safing/spn/hub"
	"github.com/safing/spn/navigator"
	"github.com/safing/spn/terminal"

	"github.com/safing/portbase/database"
	"github.com/safing/portbase/log"
	"github.com/safing/portbase/modules"
	"github.com/safing/spn/cabin"
)

const (
	maintainStatusRegularInterval        = 15 * time.Minute
	maintainStatusShortInterval          = 1 * time.Minute
	maintainStatusShortIntervalAddRandom = 2 * time.Minute
)

var (
	publicIdentity    *cabin.Identity
	publicIdentityKey = "core:spn/public/identity"

	publicIdentityUpdateTask *modules.Task
	statusUpdateTask         *modules.Task
)

func loadPublicIdentity() (err error) {
	var changed bool

	publicIdentity, changed, err = cabin.LoadIdentity(publicIdentityKey)
	switch err {
	case nil:
		// load was successful
		log.Infof("spn/captain: loaded public hub identity %s", publicIdentity.Hub.ID)
	case database.ErrNotFound:
		// does not exist, create new
		publicIdentity, err = cabin.CreateIdentity(module.Ctx, conf.MainMapName)
		if err != nil {
			return fmt.Errorf("failed to create new identity: %w", err)
		}
		publicIdentity.SetKey(publicIdentityKey)
		changed = true

		log.Infof("spn/captain: created new public hub identity %s", publicIdentity.ID)
	default:
		// loading error, abort
		return fmt.Errorf("failed to load public identity: %w", err)
	}

	// Save to database if the identity changed.
	if changed {
		err = publicIdentity.Save()
		if err != nil {
			return fmt.Errorf("failed to save new/updated identity to database: %w", err)
		}
	}

	// Set available networks.
	conf.SetHubNetworks(
		publicIdentity.Hub.Info.IPv4 != nil,
		publicIdentity.Hub.Info.IPv6 != nil,
	)

	// Set Home Hub before updating the hub on the map, as this would trigger a
	// recalculation without a Home Hub.
	ok := navigator.Main.SetHome(publicIdentity.ID, nil)
	// Always update the navigator in any case in order to sync the reference to
	// the active struct of the identity.
	navigator.Main.UpdateHub(publicIdentity.Hub)
	// Setting the Home Hub will have failed if the identidy was only just
	// created - try again if it failed.
	if !ok {
		ok = navigator.Main.SetHome(publicIdentity.ID, nil)
		if !ok {
			return errors.New("failed to set self as home hub")
		}
	}

	return nil
}

func prepPublicIdentityMgmt() error {
	publicIdentityUpdateTask = module.NewTask(
		"maintain public identity",
		maintainPublicIdentity,
	)

	statusUpdateTask = module.NewTask(
		"maintain public status",
		maintainPublicStatus,
	).Repeat(maintainStatusRegularInterval)

	return module.RegisterEventHook(
		"config",
		"config change",
		"update public identity from config",
		func(_ context.Context, _ interface{}) error {
			// trigger update in 5 minutes
			publicIdentityUpdateTask.Schedule(time.Now().Add(5 * time.Minute))
			return nil
		},
	)
}

func maintainPublicIdentity(ctx context.Context, task *modules.Task) error {
	changed, err := publicIdentity.MaintainAnnouncement(false)
	if err != nil {
		return fmt.Errorf("failed to maintain announcement: %w", err)
	}

	if !changed {
		return nil
	}

	// Update on map.
	navigator.Main.UpdateHub(publicIdentity.Hub)
	log.Debug("spn/captain: updated own hub on map after announcement change")

	// export announcement
	announcementData, err := publicIdentity.ExportAnnouncement()
	if err != nil {
		return fmt.Errorf("failed to export announcement: %w", err)
	}

	// forward to other connected Hubs
	gossipRelayMsg("", GossipHubAnnouncementMsg, announcementData)

	// manage docks in order to react to possibly changed transports
	if managePiersTask != nil {
		managePiersTask.Queue()
	}

	return nil
}

func maintainPublicStatus(ctx context.Context, task *modules.Task) error {
	// Schedule maintenance soon again if something failed.
	var tryAgainSoon bool
	defer func() {
		if tryAgainSoon {
			maintainStatusSoon(
				maintainStatusShortInterval,
				maintainStatusShortIntervalAddRandom,
			)
		}
	}()

	// Maintain cranes.
	cranes := docks.GetAllAssignedCranes()
	for _, crane := range cranes {
		// Ignore private or stopped cranes.
		if !crane.Public() || crane.Stopped() {
			continue
		}

		// Maintain crane.
		tErr := maintainCrane(ctx, crane)
		switch {
		case tErr == nil:
			// All good, continue.
		case errors.Is(tErr, terminal.ErrStopping):
			// Check if we should stop.
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		case errors.Is(tErr, terminal.ErrTryAgainLater):
			// Someone else is alredy running a test, try again later.
			tryAgainSoon = true
		case tErr.IsError():
			// Some other error happened.
			log.Errorf("captain: maintaining crane %s failed: %s", crane.ID, tErr)
		}
	}

	// Get current lanes.
	lanes := make([]*hub.Lane, 0, len(cranes))
	for _, crane := range cranes {
		// Ignore private or stopped cranes.
		if !crane.Public() || crane.Stopped() {
			continue
		}

		// Add crane lane.
		lanes = append(lanes, &hub.Lane{
			ID:       crane.ConnectedHub.ID,
			Capacity: crane.GetLaneCapacity(),
			Latency:  crane.GetLaneLatency(),
		})
	}
	// Sort Lanes for comparing.
	hub.SortLanes(lanes)

	// Get system load and convert to fixed steps.
	var load int
	loadAvg, ok := metrics.LoadAvg15()
	switch {
	case !ok:
		load = -1
	case loadAvg >= 1:
		load = 100
	case loadAvg >= 0.95:
		load = 95
	case loadAvg >= 0.8:
		load = 80
	default:
		load = 0
	}
	if loadAvg >= 0.8 {
		log.Warningf("spn/captain: publishing 15m system load average of %.2f as %d", loadAvg, load)
	}

	// Run maintenance with the new data.
	changed, err := publicIdentity.MaintainStatus(lanes, load, false)
	if err != nil {
		return fmt.Errorf("failed to maintain status: %w", err)
	}

	if !changed {
		return nil
	}

	// Update on map.
	navigator.Main.UpdateHub(publicIdentity.Hub)
	log.Debug("spn/captain: updated own hub on map after status change")

	// export status
	statusData, err := publicIdentity.ExportStatus()
	if err != nil {
		return fmt.Errorf("failed to export status: %w", err)
	}

	// forward to other connected Hubs
	gossipRelayMsg("", GossipHubStatusMsg, statusData)

	log.Infof(
		"spn/captain: updated status with load %d and current lanes: %v",
		publicIdentity.Hub.Status.Load,
		publicIdentity.Hub.Status.Lanes,
	)
	return nil
}

func maintainCrane(ctx context.Context, crane *docks.Crane) *terminal.Error {
	// Don't maintain private or stopped cranes.
	if !crane.Public() || crane.Stopped() {
		return nil
	}

	// Measure latency if needed.
	if time.Now().After(crane.LaneLatencyExpiresAt()) {
		// Start op.
		latOp, tErr := docks.NewLatencyTestOp(crane.Controller)
		if tErr != nil {
			return tErr
		}

		// Wait for op to complete.
		select {
		case tErr = <-latOp.Result():
			if tErr.IsError() {
				return tErr
			}
		case <-ctx.Done():
			return terminal.ErrStopping
		}
	}

	// Measure capacity if needed.
	if time.Now().After(crane.LaneCapacityExpiresAt()) {
		// Start op.
		capOp, tErr := docks.NewCapacityTestOp(crane.Controller)
		if tErr != nil {
			return tErr
		}

		// Wait for op to complete.
		select {
		case tErr = <-capOp.Result():
			if tErr.IsError() {
				return tErr
			}
		case <-ctx.Done():
			return terminal.ErrStopping
		}
	}

	return nil
}

func maintainStatusSoon(tryIn, addRandom time.Duration) {
	n, err := rng.Number(uint64(addRandom) * 2)
	if err != nil {
		log.Warningf("captain: failed to get random number for scheduling status maintenance: %s", err)
	} else {
		tryIn += time.Duration(n)
	}

	statusUpdateTask.Schedule(time.Now().Add(tryIn))
}
