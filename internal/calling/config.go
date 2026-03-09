//go:build !nouac

package calling

type Config struct {
	STUNServers []string
	UDPPortMin  uint16
	UDPPortMax  uint16
	Audio       AudioConfig
	SIP         SIPConfig
}

type AudioConfig struct {
	DeviceKeyword    string
	OutputDeviceName string
	SampleRate       int
	Channels         int
	BitsPerSample    int
	CaptureChunkMs   int
	PlaybackChunkMs  int
}

type SIPConfig struct {
	Enabled            bool
	ModemICCID         string
	ModemICCIDs        []string
	Username           string
	Password           string
	Proxy              string
	Port               int
	Domain             string
	Transport          string
	TLSSkipVerify      bool
	Register           bool
	AcceptIncoming     bool
	InviteTarget       string
	RegisterExpires    int
	LocalHost          string
	LocalPort          int
	RTPBindIP          string
	RTPPortMin         int
	RTPPortMax         int
	InviteTimeoutSec   int
	DTMFMethod         string
	DTMFDurationMillis int
}

func (a AudioConfig) CaptureSamples() int {
	if a.SampleRate <= 0 || a.CaptureChunkMs <= 0 {
		return 320
	}
	return a.SampleRate * a.CaptureChunkMs / 1000
}

func (a AudioConfig) PlaybackSamples() int {
	if a.SampleRate <= 0 || a.PlaybackChunkMs <= 0 {
		return 800
	}
	return a.SampleRate * a.PlaybackChunkMs / 1000
}
