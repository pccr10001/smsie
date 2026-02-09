//go:build windows

package calling

import (
	"fmt"

	"github.com/google/gousb"
)

func ResolveUSBIdentityFromPort(target ModemTarget) (USBIdentity, error) {
	if target.VID == "" || target.PID == "" {
		return USBIdentity{}, fmt.Errorf("missing VID/PID for port %s", target.PortName)
	}

	ctx := gousb.NewContext()
	defer ctx.Close()

	vid, err := parseHexID(target.VID)
	if err != nil {
		return USBIdentity{}, fmt.Errorf("invalid vid: %w", err)
	}
	pid, err := parseHexID(target.PID)
	if err != nil {
		return USBIdentity{}, fmt.Errorf("invalid pid: %w", err)
	}

	opened, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return desc.Vendor == gousb.ID(vid) && desc.Product == gousb.ID(pid)
	})
	if err != nil {
		return USBIdentity{}, err
	}
	defer func() {
		for _, d := range opened {
			_ = d.Close()
		}
	}()

	if len(opened) == 0 {
		return USBIdentity{}, fmt.Errorf("usb device not found for vid=%s pid=%s", target.VID, target.PID)
	}

	chosen := opened[0]
	serial := ""
	if s, serr := chosen.SerialNumber(); serr == nil {
		serial = s
	}

	if target.Serial != "" {
		for _, d := range opened {
			if s, serr := d.SerialNumber(); serr == nil && s == target.Serial {
				chosen = d
				serial = s
				break
			}
		}
	}

	return USBIdentity{
		VID:     fmt.Sprintf("%04X", uint16(chosen.Desc.Vendor)),
		PID:     fmt.Sprintf("%04X", uint16(chosen.Desc.Product)),
		Serial:  serial,
		Bus:     int(chosen.Desc.Bus),
		Address: int(chosen.Desc.Address),
	}, nil
}
