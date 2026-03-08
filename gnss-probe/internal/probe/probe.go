// Package probe dispatches to serial or modem mode based on GPS_MODE and device VID.
package probe

import (
	"log"
	"os"

	"gnss-probe/internal/config"
	"gnss-probe/internal/device"
	"gnss-probe/internal/modem"
	"gnss-probe/internal/result"
)

// Run resolves the device, selects serial or modem mode, and executes the probe.
func Run(cfg *config.Config) (*result.Result, int) {
	devInfo, autoDetected, err := resolveDevice(cfg)
	if err != nil {
		h, _ := os.Hostname()
		res := &result.Result{
			Probe:         "gnss",
			AgentHostname: h,
			AgentID:       cfg.AgentID,
			Error:         err.Error(),
			ErrorCode:     result.ExitDeviceError,
		}
		return res, result.ExitDeviceError
	}

	mode := resolveMode(cfg, devInfo)
	log.Printf("[INFO] GPS_MODE resolved to: %s (device: %s %s)", mode, devInfo.Manufacturer, devInfo.Chip)

	switch mode {
	case "modem":
		return modem.Run(cfg, devInfo)
	default:
		return runSerial(cfg, devInfo, autoDetected)
	}
}

// resolveMode determines serial vs modem per spec section 10.6.
func resolveMode(cfg *config.Config, devInfo *device.Info) string {
	switch cfg.Mode {
	case "modem":
		return "modem"
	case "serial":
		return "serial"
	default: // "auto"
		if devInfo.IsModem {
			return "modem"
		}
		return "serial"
	}
}
