//go:build !windows && !linux

package calling

import "fmt"

func ResolveUSBIdentityFromPort(target ModemTarget) (USBIdentity, error) {
	_ = target
	return USBIdentity{}, fmt.Errorf("port-to-usb resolver is only implemented on windows/linux")
}
