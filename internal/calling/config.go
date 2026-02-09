package calling

type Config struct {
	STUNServers []string
	UDPPortMin  uint16
	UDPPortMax  uint16
	Audio       AudioConfig
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
