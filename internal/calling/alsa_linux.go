//go:build !nouac && linux

package calling

import (
	"os"
	"path/filepath"
	"strings"
)

func ResolveALSACardHintsFromPort(target ModemTarget) []string {
	hints := map[string]struct{}{}
	add := func(v string) {
		x := strings.TrimSpace(strings.ToLower(v))
		if x == "" {
			return
		}
		hints[x] = struct{}{}
	}

	port := strings.TrimSpace(target.PortName)
	if port == "" {
		return nil
	}

	base := filepath.Base(port)
	if !strings.HasPrefix(base, "tty") {
		base = "tty" + base
	}

	ttyPath := filepath.Join("/sys/class/tty", base, "device")
	resolved, err := filepath.EvalSymlinks(ttyPath)
	if err != nil {
		return nil
	}

	usbPath, err := findUSBDevicePath(resolved)
	if err != nil {
		return nil
	}

	audioRoot := filepath.Join(usbPath, "sound")
	entries, err := os.ReadDir(audioRoot)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		name := strings.TrimSpace(e.Name())
		if !strings.HasPrefix(name, "card") {
			continue
		}
		add(name)

		idPath := filepath.Join("/sys/class/sound", name, "id")
		if b, readErr := os.ReadFile(idPath); readErr == nil {
			add(string(b))
		}
	}

	result := make([]string, 0, len(hints))
	for h := range hints {
		result = append(result, h)
	}
	return result
}
