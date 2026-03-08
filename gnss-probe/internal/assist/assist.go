// Package assist sends cached position/time to the receiver to convert
// cold start into assisted warm start (spec section 3.4).
//
// Supported chips:
//   - u-blox NEO: $PUBX,04 (time sync)
//   - MediaTek MT33xx: $PMTK740 (position) + $PMTK741 (time)
//   - Generic: $PSRF104 (SiRF, best-effort)
package assist

import (
	"fmt"
	"io"
	"time"

	"gnss-probe/internal/cache"
)

// Send writes assisted start sentences to the serial port.
// chip is the chip model string from device whitelist (e.g. "NEO-M8", "MT3329/MT3339").
func Send(w io.Writer, fix *cache.Fix, chip string) error {
	now := time.Now().UTC()
	switch {
	case isUblox(chip):
		return sendUblox(w, now)
	case isMediatek(chip):
		return sendMediatek(w, fix, now)
	default:
		return sendGeneric(w, fix, now)
	}
}

// sendUblox sends $PUBX,04 to set receiver time (u-blox NEO series).
func sendUblox(w io.Writer, now time.Time) error {
	body := fmt.Sprintf("PUBX,04,%s,%s,0.00,0000,0,0.000,0.000,0",
		now.Format("150405.00"),
		now.Format("020106"),
	)
	_, err := fmt.Fprintf(w, "$%s*%02X\r\n", body, checksum(body))
	return err
}

// sendMediatek sends $PMTK740 (position hint) + $PMTK741 (time hint).
func sendMediatek(w io.Writer, fix *cache.Fix, now time.Time) error {
	posBody := fmt.Sprintf("PMTK740,%.6f,%.6f,%.1f", fix.Lat, fix.Lon, fix.Alt)
	if _, err := fmt.Fprintf(w, "$%s*%02X\r\n", posBody, checksum(posBody)); err != nil {
		return err
	}
	timeBody := fmt.Sprintf("PMTK741,%d,%02d,%02d,%02d,%02d,%02d",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
	)
	_, err := fmt.Fprintf(w, "$%s*%02X\r\n", timeBody, checksum(timeBody))
	return err
}

// sendGeneric sends $PSRF104 (SiRF III position init).
func sendGeneric(w io.Writer, fix *cache.Fix, now time.Time) error {
	body := fmt.Sprintf("PSRF104,%.6f,%.6f,%.1f,0,%d,%d,12,8",
		fix.Lat, fix.Lon, fix.Alt, gpsTOW(now), gpsWeek(now),
	)
	_, err := fmt.Fprintf(w, "$%s*%02X\r\n", body, checksum(body))
	return err
}

func isUblox(chip string) bool {
	return len(chip) >= 3 && chip[:3] == "NEO"
}

func isMediatek(chip string) bool {
	return len(chip) >= 5 && (chip[:5] == "MT332" || chip[:5] == "MT333")
}

// checksum computes XOR of all bytes in the sentence body (between $ and *).
func checksum(body string) byte {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return cs
}

var gpsEpoch = time.Date(1980, 1, 6, 0, 0, 0, 0, time.UTC)

func gpsWeek(t time.Time) int {
	return int(t.Sub(gpsEpoch).Hours() / (24 * 7))
}

func gpsTOW(t time.Time) int {
	weekStart := gpsEpoch.Add(time.Duration(gpsWeek(t)) * 7 * 24 * time.Hour)
	return int(t.Sub(weekStart).Seconds())
}
