//go:build !nouac && !linux

package calling

func ResolveALSACardHintsFromPort(target ModemTarget) []string {
	_ = target
	return nil
}
