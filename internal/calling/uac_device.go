//go:build !nouac

package calling

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gordonklaus/portaudio"
)

type uacAudioDevice struct {
	In  *portaudio.DeviceInfo
	Out *portaudio.DeviceInfo
}

func pickUACAudioDevice(cfg AudioConfig, target ModemTarget) (*uacAudioDevice, error) {
	devices, err := portaudio.Devices()
	if err != nil {
		return nil, err
	}

	normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	keyword := normalize(cfg.DeviceKeyword)

	identity, idErr := ResolveUSBIdentityFromPort(target)
	if idErr != nil {
		return nil, idErr
	}

	usbList, enumErr := EnumerateByVIDPID(identity.VID, identity.PID)
	if enumErr != nil {
		usbList = nil
	}

	targetHints := map[string]struct{}{}
	addHint := func(v string) {
		n := normalize(v)
		if n == "" {
			return
		}
		targetHints[n] = struct{}{}
	}
	addHint(identity.VID)
	addHint(identity.PID)
	for _, h := range ResolveALSACardHintsFromPort(target) {
		addHint(h)
	}

	hasTargetUSBAudio := false
	for _, d := range usbList {
		if d.HasAudio {
			hasTargetUSBAudio = true
			addHint(d.Product)
			break
		}
	}

	hasAnyTargetHint := func(name string) bool {
		for hint := range targetHints {
			if strings.Contains(name, hint) {
				return true
			}
		}
		return false
	}

	hasGenericUSBHint := func(name string) bool {
		return strings.Contains(name, "usb") || strings.Contains(name, "ac interface") || strings.Contains(name, "android")
	}

	findPair := func(filter func(name string) bool) (*portaudio.DeviceInfo, *portaudio.DeviceInfo) {
		var in, out *portaudio.DeviceInfo
		for _, d := range devices {
			name := normalize(d.Name)
			if !filter(name) {
				continue
			}
			if in == nil && d.MaxInputChannels > 0 {
				in = d
			}
			if out == nil && d.MaxOutputChannels > 0 {
				out = d
			}
			if in != nil && out != nil {
				break
			}
		}
		return in, out
	}

	var inDevice, outDevice *portaudio.DeviceInfo

	if keyword != "" && hasTargetUSBAudio {
		inDevice, outDevice = findPair(func(name string) bool {
			return strings.Contains(name, keyword) && hasAnyTargetHint(name)
		})
	}
	if (inDevice == nil || outDevice == nil) && hasTargetUSBAudio {
		inDevice, outDevice = findPair(func(name string) bool {
			return hasAnyTargetHint(name)
		})
	}
	if inDevice == nil || outDevice == nil {
		inDevice, outDevice = findPair(func(name string) bool {
			return keyword != "" && strings.Contains(name, keyword) && hasGenericUSBHint(name)
		})
	}
	if inDevice == nil || outDevice == nil {
		inDevice, outDevice = findPair(func(name string) bool {
			return hasGenericUSBHint(name)
		})
	}
	if (inDevice == nil || outDevice == nil) && keyword != "" {
		inDevice, outDevice = findPair(func(name string) bool {
			return strings.Contains(name, keyword)
		})
	}
	if inDevice == nil || outDevice == nil {
		inDevice, outDevice = findPair(func(name string) bool {
			return true
		})
	}

	if inDevice == nil || outDevice == nil {
		if len(devices) == 0 {
			return nil, errors.New("no audio devices from PortAudio")
		}
		return nil, fmt.Errorf("cannot find UAC audio device for port=%s (vid=%s pid=%s keyword=%q)", target.PortName, identity.VID, identity.PID, cfg.DeviceKeyword)
	}

	return &uacAudioDevice{In: inDevice, Out: outDevice}, nil
}
