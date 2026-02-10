//go:build !nouac && linux

package calling

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveUSBIdentityFromPort(target ModemTarget) (USBIdentity, error) {
	port := strings.TrimSpace(target.PortName)
	if port == "" {
		return USBIdentity{}, fmt.Errorf("empty port name")
	}

	base := filepath.Base(port)
	if !strings.HasPrefix(base, "tty") {
		base = "tty" + base
	}

	ttyPath := filepath.Join("/sys/class/tty", base, "device")
	resolved, err := filepath.EvalSymlinks(ttyPath)
	if err != nil {
		return USBIdentity{}, fmt.Errorf("resolve tty path failed: %w", err)
	}

	usbPath, err := findUSBDevicePath(resolved)
	if err != nil {
		return USBIdentity{}, err
	}

	vid, err := readSysValue(filepath.Join(usbPath, "idVendor"))
	if err != nil {
		return USBIdentity{}, err
	}
	pid, err := readSysValue(filepath.Join(usbPath, "idProduct"))
	if err != nil {
		return USBIdentity{}, err
	}
	serial, _ := readSysValue(filepath.Join(usbPath, "serial"))

	return USBIdentity{VID: strings.ToUpper(vid), PID: strings.ToUpper(pid), Serial: serial}, nil
}

func findUSBDevicePath(start string) (string, error) {
	cur := start
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(cur, "idVendor")); err == nil {
			return cur, nil
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	return "", fmt.Errorf("usb device root not found from %s", start)
}

func readSysValue(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s failed: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}
