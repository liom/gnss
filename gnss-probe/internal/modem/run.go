// run.go implements the modem-mode probe flow (spec section 10.3):
// FindPorts → AT init → read NMEA → AT+QGPSEND → save cache
package modem

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gnss-probe/internal/cache"
	"gnss-probe/internal/config"
	"gnss-probe/internal/device"
	"gnss-probe/internal/nmea"
	"gnss-probe/internal/result"

	goserial "go.bug.st/serial"
)

func Run(cfg *config.Config, devInfo *device.Info) (*result.Result, int) {
	hostname, _ := os.Hostname()
	startTime := time.Now()

	res := &result.Result{
		Probe:         "gnss",
		Timestamp:     startTime.Unix(),
		AgentHostname: hostname,
		AgentID:       cfg.AgentID,
		Device: &result.Device{
			Port:         devInfo.Port,
			Mode:         "modem",
			AutoDetected: devInfo.Port == "",
			USBVID:       devInfo.VID,
			USBPID:       devInfo.PID,
			Manufacturer: devInfo.Manufacturer,
			Chip:         devInfo.Chip,
		},
	}

	// --- Resolve AT and NMEA ports ---
	var ports *Ports
	var err error

	if cfg.ModemATPort != "" && cfg.ModemNMEAPort != "" {
		ports = &Ports{AT: cfg.ModemATPort, NMEA: cfg.ModemNMEAPort}
		log.Printf("[INFO] Modem ports from ENV: AT=%s NMEA=%s", ports.AT, ports.NMEA)
	} else {
		ports, err = FindPorts(devInfo.VID)
		if err != nil {
			res.Error = fmt.Sprintf("modem port detection failed: %v", err)
			res.ErrorCode = result.ExitDeviceError
			return res, result.ExitDeviceError
		}
	}
	log.Printf("[INFO] Mode: modem, AT port: %s, NMEA port: %s", ports.AT, ports.NMEA)

	// --- Open AT port ---
	atPort, err := Open(ports.AT, cfg.ModemBaud)
	if err != nil {
		res.Error = fmt.Sprintf("AT port open failed: %v", err)
		res.ErrorCode = result.ExitDeviceError
		return res, result.ExitDeviceError
	}
	defer func() {
		// Always send AT+QGPSEND on exit (spec 10.3)
		if _, err := AT(atPort, "AT+QGPSEND", 3*time.Second); err != nil {
			log.Printf("[WARN] AT+QGPSEND: %v", err)
		} else {
			log.Printf("[INFO] Sending AT+QGPSEND... OK")
		}
		atPort.Close()
	}()

	// --- AT initialisation sequence (spec 10.3) ---
	if err := initGNSS(atPort, cfg); err != nil {
		res.Error = err.Error()
		res.ErrorCode = result.ExitDeviceError
		return res, result.ExitDeviceError
	}

	// --- XTRA (spec 10.4) ---
	xtraUsed := false
	if cfg.XTRAEnable {
		xtraUsed = enableXTRA(atPort, cfg)
	}
	_ = xtraUsed // will be added to JSON in future

	// --- Determine start type from cache ---
	cachePath := getEnv("GPS_CACHE_PATH", cache.DefaultPath)
	lastFix, err := cache.Load(cachePath)
	if err != nil {
		log.Printf("[WARN] Cache read error: %v", err)
	}

	startType := "cold"
	if xtraUsed {
		startType = "hot"
		log.Printf("[INFO] Start type: hot (XTRA assisted)")
	} else if lastFix != nil && lastFix.Age() < 4*time.Hour {
		startType = "warm"
		log.Printf("[INFO] Start type: warm (cache age %s)", lastFix.Age().Round(time.Second))
	}

	// --- Open NMEA port ---
	nmeaPort, err := Open(ports.NMEA, cfg.ModemBaud)
	if err != nil {
		res.Error = fmt.Sprintf("NMEA port open failed: %v", err)
		res.ErrorCode = result.ExitDeviceError
		return res, result.ExitDeviceError
	}
	defer nmeaPort.Close()

	// --- Read NMEA ---
	state := nmea.NewState()
	state.StartType = startType

	fixTimeout := cfg.TimeoutFor(startType)
	fixDeadline := time.Now().Add(fixTimeout)
	log.Printf("[INFO] Fix timeout: %s (%s start)", fixTimeout, startType)

	scanner := bufio.NewScanner(nmeaPort)
	readStart := time.Now()
	var readUntil time.Time

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if cfg.DebugNMEA {
			log.Printf("[DEBUG] %s", line)
		}
		if err := state.Feed(line); err != nil {
			log.Printf("[WARN] NMEA parse: %v", err)
			continue
		}

		if state.Fix && readUntil.IsZero() {
			elapsed := time.Since(readStart)
			log.Printf("[INFO] Fix acquired in %.1fs, satellites: %d, HDOP: %.1f",
				elapsed.Seconds(), state.Satellites, state.HDOP)
			readUntil = time.Now().Add(cfg.ReadDuration)
		}
		if !readUntil.IsZero() && time.Now().After(readUntil) {
			break
		}
		if time.Now().After(fixDeadline) {
			break
		}
	}

	// --- Build result ---
	gnss := &result.GNSS{
		Fix:         state.Fix,
		Provider:    state.Provider(),
		Satellites:  state.Satellites,
		StartType:   state.StartType,
		TimeToFixMs: state.TimeToFix(readStart),
	}
	if state.Fix {
		gnss.HDOP = state.HDOP
	}
	res.GNSS = gnss

	if state.Fix {
		res.Location = &result.Location{Lat: state.Lat, Lon: state.Lon, Alt: state.Alt}
		if !state.UTC.IsZero() {
			drift := time.Since(state.UTC).Seconds() * 1000
			res.Time = &result.Time{UTC: state.UTC.Format(time.RFC3339), DriftMs: drift}
		}

		if err := cache.Save(cachePath, &cache.Fix{
			Lat: state.Lat, Lon: state.Lon, Alt: state.Alt,
			Timestamp: time.Now().Unix(), HDOP: state.HDOP,
		}); err != nil {
			log.Printf("[WARN] Cache save failed: %v", err)
		} else {
			log.Printf("[INFO] Position cached to %s", cachePath)
		}
	}

	if !state.Fix {
		log.Printf("[WARN] Timeout reached, no fix (satellites: %d)", state.Satellites)
		return res, result.ExitNoFix
	}

	log.Printf("[INFO] Provider: %s", state.Provider())
	return res, result.ExitSuccess
}

// initGNSS sends the mandatory AT init sequence (spec 10.3).
func initGNSS(port goserial.Port, cfg *config.Config) error {
	t := cfg.ModemInitTimeout

	// AT+QGPS=1 — mandatory, retry once on ERROR (may already be running)
	if _, err := AT(port, "AT+QGPS=1", t); err != nil {
		if !strings.Contains(err.Error(), "ERROR") {
			return fmt.Errorf("AT+QGPS=1: %w", err)
		}
		log.Printf("[INFO] AT+QGPS=1 returned ERROR (GNSS already running), continuing")
	} else {
		log.Printf("[INFO] Sending AT+QGPS=1... OK")
	}

	// Recommended config — non-fatal if any fails
	optional := []string{
		`AT+QGPSCFG="nmeasrc",1`,
		`AT+QGPSCFG="gpsnmeatype",31`,
		`AT+QGPSCFG="glonassnmeatype",1`,
		`AT+QGPSCFG="galileonmeatype",1`,
		`AT+QGPSCFG="beidounmeatype",1`,
		`AT+QGPSCFG="autogps",0`,
	}
	for _, cmd := range optional {
		if _, err := AT(port, cmd, 2*time.Second); err != nil {
			log.Printf("[WARN] %v", err)
		}
	}
	return nil
}

// enableXTRA attempts to enable XTRA assisted GPS (spec 10.4).
// Returns true if XTRA was successfully enabled.
func enableXTRA(port goserial.Port, cfg *config.Config) bool {
	if _, err := AT(port, "AT+QGPSXTRA=1", 10*time.Second); err != nil {
		log.Printf("[WARN] XTRA unavailable, falling back to cold start: %v", err)
		return false
	}
	log.Printf("[INFO] XTRA enabled")

	if cfg.XTRATimeSync {
		now := time.Now().UTC()
		cmd := fmt.Sprintf(`AT+QGPSXTRATIME=0,"%s",1,1,3.5`,
			now.Format("2006/01/02,15:04:05"),
		)
		if _, err := AT(port, cmd, 5*time.Second); err != nil {
			log.Printf("[WARN] XTRA time sync failed: %v", err)
		} else {
			log.Printf("[INFO] XTRA time sync... OK")
		}
	}
	return true
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
