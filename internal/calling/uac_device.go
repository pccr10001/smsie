//go:build !nouac

package calling

import (
	"errors"
	"fmt"
	"math"
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

	deviceScore := func(name string) int {
		score := 0

		if hasAnyTargetHint(name) {
			score += 220
		}
		if hasGenericUSBHint(name) {
			score += 120
		}
		if keyword != "" && strings.Contains(name, keyword) {
			score += 80
		}

		if strings.Contains(name, "plughw") {
			score += 50
		}
		if strings.Contains(name, "hw:") {
			score += 40
		}
		if strings.Contains(name, "front") {
			score += 25
		}

		if strings.Contains(name, "sysdefault") {
			score -= 40
		} else if strings.Contains(name, "default") {
			score -= 20
		}

		if strings.Contains(name, "surround") ||
			strings.Contains(name, "rear") ||
			strings.Contains(name, "center_lfe") ||
			strings.Contains(name, "side") {
			score -= 120
		}
		if strings.Contains(name, "dmix") {
			score -= 70
		}
		if strings.Contains(name, "iec958") || strings.Contains(name, "hdmi") {
			score -= 60
		}
		if strings.Contains(name, "jack") || strings.Contains(name, "pulse") {
			score -= 60
		}
		if strings.Contains(name, "null") {
			score -= 200
		}

		return score
	}

	findPair := func(filter func(name string) bool) (*portaudio.DeviceInfo, *portaudio.DeviceInfo) {
		var in, out *portaudio.DeviceInfo
		bestIn := math.MinInt
		bestOut := math.MinInt
		for _, d := range devices {
			name := normalize(d.Name)
			if !filter(name) {
				continue
			}

			s := deviceScore(name)
			if d.MaxInputChannels > 0 && s > bestIn {
				bestIn = s
				in = d
			}
			if d.MaxOutputChannels > 0 && s > bestOut {
				bestOut = s
				out = d
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
