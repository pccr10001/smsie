package calling

type ModemTarget struct {
	PortName string
	VID      string
	PID      string
	Serial   string
}

type USBIdentity struct {
	VID     string
	PID     string
	Serial  string
	Bus     int
	Address int
}
