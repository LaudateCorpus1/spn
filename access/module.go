package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/safing/spn/access/account"
	"github.com/tevino/abool"

	"github.com/safing/portbase/config"
	"github.com/safing/portbase/log"
	"github.com/safing/portbase/modules"
	"github.com/safing/spn/conf"
)

var (
	module *modules.Module

	accountUpdateTask *modules.Task

	tokenIssuerIsFailing     = abool.New()
	tokenIssuerRetryDuration = 10 * time.Minute
)

// Errors.
var (
	ErrDeviceIsLocked       = errors.New("device is locked")
	ErrDeviceLimitReached   = errors.New("device limit reached")
	ErrFallbackNotAvailable = errors.New("fallback tokens not available, token issuer is online")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrMayNotUseSPN         = errors.New("may not use SPN")
	ErrNotLoggedIn          = errors.New("not logged in")
)

func init() {
	module = modules.Register("access", prep, start, stop)
}

func prep() error {
	// Register API handlers.
	if conf.Client() {
		err := registerAPIEndpoints()
		if err != nil {
			return err
		}
	}

	return nil
}

func start() error {
	// Initialize zones.
	if err := initializeZones(); err != nil {
		return err
	}

	if conf.Client() {
		// Load tokens from database.
		loadTokens()

		// Register new task.
		accountUpdateTask = module.NewTask(
			"update account",
			UpdateAccount,
		).Repeat(24 * time.Hour)
		// First execution is done by the client manager in the captain module.
	}

	return nil
}

func stop() error {
	if conf.Client() {
		// Stop account update task.
		accountUpdateTask.Cancel()
		accountUpdateTask = nil

		// Store tokens to database.
		storeTokens()
	}

	// Reset zones.
	resetZones()

	return nil
}

func UpdateAccount(_ context.Context, task *modules.Task) error {
	// Retry sooner if the token issuer is failing.
	defer func() {
		if tokenIssuerIsFailing.IsSet() && task != nil {
			task.Schedule(time.Now().Add(tokenIssuerRetryDuration))
		}
	}()

	_, _, err := getUserProfile()
	if err != nil {
		return fmt.Errorf("failed to update user profile: %w", err)
	}

	err = getTokens()
	if err != nil {
		return fmt.Errorf("failed to get tokens: %w", err)
	}

	return nil
}

func enableSPN() {
	err := config.SetConfigOption("spn/enable", true)
	if err != nil {
		log.Warningf("access: failed to enable the SPN during login: %s", err)
	}
}

func disableSPN() {
	err := config.SetConfigOption("spn/enable", false)
	if err != nil {
		log.Warningf("access: failed to disable the SPN during logout: %s", err)
	}
}

func TokenIssuerIsFailing() bool {
	return tokenIssuerIsFailing.IsSet()
}

func tokenIssuerFailed() {
	if !tokenIssuerIsFailing.SetToIf(false, true) {
		return
	}
	if !module.Online() {
		return
	}

	accountUpdateTask.Schedule(time.Now().Add(tokenIssuerRetryDuration))
}

func (user *UserRecord) IsLoggedIn() bool {
	user.Lock()
	defer user.Unlock()

	switch user.State {
	case account.UserStateNone, account.UserStateLoggedOut:
		return false
	default:
		return true
	}
}

func (user *UserRecord) MayUseTheSPN() bool {
	user.Lock()
	defer user.Unlock()

	return user.User.MayUseSPN()
}
