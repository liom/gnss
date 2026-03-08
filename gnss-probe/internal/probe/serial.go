// serial.go implements the serial-mode GNSS probe flow (spec section 1.1).
package probe

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gnss-probe/internal/assist"
	"gnss-probe/internal/cache"
	"gnss-probe/internal/config"
	"gnss-probe/internal/device"
	"gnss-probe/internal/nmea"
	"gnss-probe/internal/result"

	goserial "go.bug.st/serial"
)

// runSerial executes the serial-mode probe and returns the result + exit code.
func runSerial(cfg *config.Config, devInfo *device.Info, autoDetected bool) (*result.Result, int) {
	hostname, _ := os.Hostname()
	startTime := time.Now()

	res := &result.Result{
		Probe:         "gnss",
		Timestamp:     startTime.Unix(),
		AgentHostname: hostname,
		AgentID:       cfg.AgentID,
	}

	res.Device = buildDeviceResult(devInfo, autoDetected, "serial")

	// --- Load position cache ---
	cachePath := getEnv("GPS_CACHE_PATH", cache.DefaultPath)
	lastFix, err := cache.Load(cachePath)
	if err != nil {
		log.Printf("[WARN] Cache read error: %v", err)
	}

	// --- Determine start type ---
	// Per spec 3.2 p.2: if VBAT present and last fix < 4h ago → hot/warm
	startType := "unknown"
	if cfg.AssumeColdStart {
		startType = "cold"
	} else if devInfo.HasVBAT && lastFix != nil && lastFix.Age() < 4*time.Hour {
		startType = "hot"
		log.Printf("[INFO] VBAT + cache age %s → hot start", lastFix.Age().Round(time.Second))
	} else if !devInfo.HasVBAT {
		log.Printf("[WARN] No VBAT detected, assuming cold start")
		startType = "cold"
	}

	// --- Open serial port with retry ---
	port, err := openWithRetry(devInfo.Port, cfg.Baudrate, 3)
	if err != nil {
		res.Error = fmt.Sprintf("serial open failed: %v", err)
		res.ErrorCode = result.ExitDeviceError
		return res, result.ExitDeviceError
	}
	defer port.Close()

	log.Printf("[INFO] Device: %s (%s %s)", devInfo.Port, devInfo.Manufacturer, devInfo.Chip)
	log.Printf("[INFO] Mode: serial, baudrate: %d", cfg.Baudrate)

	// --- Send assisted start if cache available ---
	if lastFix != nil {
		if err := assist.Send(port, lastFix, devInfo.Chip); err != nil {
			log.Printf("[WARN] Assisted start send failed: %v", err)
		} else {
			log.Printf("[INFO] Assisted start sent (cache age: %s)", lastFix.Age().Round(time.Second))
		}
	}

	// --- Read NMEA until fix or timeout ---
	state := nmea.NewState()
	if startType != "unknown" {
		state.StartType = startType
	}

	fixTimeout := cfg.TimeoutFor(startType)
	fixDeadline := time.Now().Add(fixTimeout)

	log.Printf("[INFO] Fix timeout: %s (start: %s)", fixTimeout, startType)

	scanner := bufio.NewScanner(port)
	readStart := time.Now()

	var readUntil time.Time // set once fix is acquired

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

		elapsed := time.Since(readStart)
		state.DetectStartFromSatProgression(elapsed)

		// Update timeout if start type was just detected
		if state.StartType != startType && startType == "unknown" {
			startType = state.StartType
			fixTimeout = cfg.TimeoutFor(startType)
			fixDeadline = readStart.Add(fixTimeout)
			log.Printf("[INFO] Start type detected: %s, timeout adjusted to %s", startType, fixTimeout)
		}

		if state.Fix && readUntil.IsZero() {
			readUntil = time.Now().Add(cfg.ReadDuration)
			log.Printf("[INFO] Fix acquired in %.1fs, satellites: %d, HDOP: %.1f",
				elapsed.Seconds(), state.Satellites, state.HDOP)
		}

		// Stop reading after ReadDuration post-fix
		if !readUntil.IsZero() && time.Now().After(readUntil) {
			break
		}

		// Fix timeout
		if time.Now().After(fixDeadline) {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[WARN] Serial read error: %v", err)
	}

	// --- Build GNSS result ---
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
		res.Location = &result.Location{
			Lat: state.Lat,
			Lon: state.Lon,
			Alt: state.Alt,
		}
		if !state.UTC.IsZero() {
			drift := time.Since(state.UTC).Seconds() * 1000
			res.Time = &result.Time{
				UTC:     state.UTC.Format(time.RFC3339),
				DriftMs: drift,
			}
		}
	}

	if !state.Fix {
		log.Printf("[WARN] Timeout reached, no fix acquired (satellites: %d)", state.Satellites)
		return res, result.ExitNoFix
	}

	// --- Save position cache ---
	if err := cache.Save(cachePath, &cache.Fix{
		Lat:       state.Lat,
		Lon:       state.Lon,
		Alt:       state.Alt,
		Timestamp: time.Now().Unix(),
		HDOP:      state.HDOP,
	}); err != nil {
		log.Printf("[WARN] Cache save failed: %v", err)
	} else {
		log.Printf("[INFO] Position cached to %s", cachePath)
	}

	log.Printf("[INFO] Provider: %s", state.Provider())
	return res, result.ExitSuccess
}

func resolveDevice(cfg *config.Config) (*device.Info, bool, error) {
	if cfg.Device != "auto" && !cfg.AutoScan {
		// Explicit device path
		info := device.Lookup(cfg.Device)
		if info == nil {
			info = &device.Info{Port: cfg.Device, Manufacturer: "unknown"}
		}
		return info, false, nil
	}

	candidates, err := device.Scan()
	if err != nil {
		return nil, false, err
	}

	for _, candidate := range candidates {
		if !device.NeedsNMEAProbe(candidate) {
			return candidate, true, nil
		}
		// NMEA probe for generic UART bridges
		if probeNMEA(candidate.Port, cfg.Baudrate, cfg.ScanTimeout) {
			return candidate, true, nil
		}
		log.Printf("[INFO] Auto-scan: %s skipped (no NMEA)", candidate.Port)
	}

	return nil, false, fmt.Errorf("no GNSS device found during auto-scan")
}

func probeNMEA(port string, baud int, timeout time.Duration) bool {
	p, err := goserial.Open(port, &goserial.Mode{BaudRate: baud})
	if err != nil {
		return false
	}
	defer p.Close()

	p.SetReadTimeout(timeout)
	scanner := bufio.NewScanner(p)
	deadline := time.Now().Add(timeout)

	for scanner.Scan() && time.Now().Before(deadline) {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "$G") || strings.HasPrefix(line, "$P") {
			return true
		}
	}
	return false
}

func openWithRetry(port string, baud, attempts int) (goserial.Port, error) {
	mode := &goserial.Mode{BaudRate: baud}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			log.Printf("[ERROR] Retry %d/%d in 1s...", i, attempts)
			time.Sleep(time.Second)
		}
		p, err := goserial.Open(port, mode)
		if err == nil {
			return p, nil
		}
		lastErr = err
		log.Printf("[ERROR] Failed to open %s: %v", port, err)
	}
	return nil, lastErr
}

func buildDeviceResult(info *device.Info, autoDetected bool, mode string) *result.Device {
	d := &result.Device{
		Port:         info.Port,
		Mode:         mode,
		AutoDetected: autoDetected,
		Manufacturer: info.Manufacturer,
		Chip:         info.Chip,
	}
	if info.VID != "" {
		d.USBVID = info.VID
		d.USBPID = info.PID
		hasVBAT := info.HasVBAT
		d.HasVBAT = &hasVBAT
	}
	return d
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
