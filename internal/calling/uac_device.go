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

	usbList, _ := EnumerateByVIDPID(identity.VID, identity.PID)
	hasTargetUSBAudio := false
	for _, d := range usbList {
		if d.HasAudio {
			hasTargetUSBAudio = true
			break
		}
	}

	var inDevice, outDevice *portaudio.DeviceInfo
	for _, d := range devices {
		name := normalize(d.Name)

		if keyword != "" && !strings.Contains(name, keyword) {
			continue
		}

		if hasTargetUSBAudio {
			if !strings.Contains(name, normalize(identity.VID)) && !strings.Contains(name, normalize(identity.PID)) {
				// Best effort: if device name doesn't include VID/PID hints, still allow keyword matching fallback.
			}
		}

		if inDevice == nil && d.MaxInputChannels > 0 {
			inDevice = d
		}
		if outDevice == nil && d.MaxOutputChannels > 0 {
			outDevice = d
		}
	}

	if inDevice == nil || outDevice == nil {
		if len(devices) == 0 {
			return nil, errors.New("no audio devices from PortAudio")
		}
		return nil, fmt.Errorf("cannot find UAC audio device for port=%s (vid=%s pid=%s keyword=%q)", target.PortName, identity.VID, identity.PID, cfg.DeviceKeyword)
	}

	return &uacAudioDevice{In: inDevice, Out: outDevice}, nil
}
