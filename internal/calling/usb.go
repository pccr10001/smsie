//go:build !nouac

package calling

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/gousb"
)

type USBDeviceInfo struct {
	Bus      int    `json:"bus"`
	Address  int    `json:"address"`
	VID      string `json:"vid"`
	PID      string `json:"pid"`
	Product  string `json:"product"`
	HasAudio bool   `json:"has_audio"`
}

func EnumerateByVIDPID(vidHex, pidHex string) ([]USBDeviceInfo, error) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	vid, err := parseHexID(vidHex)
	if err != nil {
		return nil, fmt.Errorf("invalid vid: %w", err)
	}
	pid, err := parseHexID(pidHex)
	if err != nil {
		return nil, fmt.Errorf("invalid pid: %w", err)
	}

	opened, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return desc.Vendor == gousb.ID(vid) && desc.Product == gousb.ID(pid)
	})
	if err != nil {
		return nil, err
	}

	devices := make([]USBDeviceInfo, 0, len(opened))
	for _, d := range opened {
		hasAudio := false
		for _, cfg := range d.Desc.Configs {
			for _, intf := range cfg.Interfaces {
				for _, alt := range intf.AltSettings {
					if alt.Class == gousb.ClassAudio {
						hasAudio = true
						break
					}
				}
				if hasAudio {
					break
				}
			}
			if hasAudio {
				break
			}
		}

		product := fmt.Sprintf("0x%04X", uint16(d.Desc.Product))
		if p, perr := d.Product(); perr == nil && strings.TrimSpace(p) != "" {
			product = strings.TrimSpace(p)
		}

		devices = append(devices, USBDeviceInfo{
			Bus:      int(d.Desc.Bus),
			Address:  int(d.Desc.Address),
			VID:      fmt.Sprintf("%04X", uint16(d.Desc.Vendor)),
			PID:      fmt.Sprintf("%04X", uint16(d.Desc.Product)),
			Product:  product,
			HasAudio: hasAudio,
		})
		_ = d.Close()
	}

	return devices, nil
}

func parseHexID(s string) (uint64, error) {
	v := strings.TrimSpace(strings.ToLower(s))
	v = strings.TrimPrefix(v, "0x")
	if v == "" {
		return 0, fmt.Errorf("empty value")
	}
	return strconv.ParseUint(v, 16, 16)
}
