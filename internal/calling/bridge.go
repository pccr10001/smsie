//go:build !nouac

package calling

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gordonklaus/portaudio"
)

type AudioBridge struct {
	logger *log.Logger
	cfg    AudioConfig

	inStream  *portaudio.Stream
	outStream *portaudio.Stream
	inBuf     []int16
	outBuf    []int16

	webrtcOut *int16Ring

	captureFrameCh chan []int16

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewAudioBridge(cfg AudioConfig, target ModemTarget, logger *log.Logger) (*AudioBridge, error) {
	if cfg.SampleRate <= 0 || cfg.Channels != 1 || cfg.BitsPerSample != 16 {
		return nil, errors.New("audio config must be mono/16bit/valid rate")
	}

	if err := portaudio.Initialize(); err != nil {
		return nil, err
	}

	device, err := pickUACAudioDevice(cfg, target)
	if err != nil {
		_ = portaudio.Terminate()
		return nil, err
	}

	logger.Printf("UAC input device: %s", device.In.Name)
	logger.Printf("UAC output device: %s", device.Out.Name)

	inParams := portaudio.HighLatencyParameters(device.In, nil)
	inParams.SampleRate = float64(cfg.SampleRate)
	inParams.Input.Channels = 1
	inParams.FramesPerBuffer = cfg.CaptureSamples()
	inBuf := make([]int16, inParams.FramesPerBuffer)
	inStream, err := portaudio.OpenStream(inParams, inBuf)
	if err != nil {
		_ = portaudio.Terminate()
		return nil, err
	}

	outParams := portaudio.HighLatencyParameters(nil, device.Out)
	outParams.SampleRate = float64(cfg.SampleRate)
	outParams.Output.Channels = 1
	outParams.FramesPerBuffer = cfg.PlaybackSamples()
	outBuf := make([]int16, outParams.FramesPerBuffer)
	outStream, err := portaudio.OpenStream(outParams, &outBuf)
	if err != nil {
		_ = inStream.Close()
		_ = portaudio.Terminate()
		return nil, err
	}

	b := &AudioBridge{
		logger:         logger,
		cfg:            cfg,
		inStream:       inStream,
		outStream:      outStream,
		inBuf:          inBuf,
		outBuf:         outBuf,
		webrtcOut:      newInt16Ring(cfg.SampleRate * 3),
		captureFrameCh: make(chan []int16, 128),
		stopCh:         make(chan struct{}),
	}

	return b, nil
}

func (b *AudioBridge) Start() error {
	if err := b.inStream.Start(); err != nil {
		return err
	}
	if err := b.outStream.Start(); err != nil {
		_ = b.inStream.Stop()
		return err
	}

	b.wg.Add(2)
	go b.captureLoop(b.inBuf)
	go b.playbackLoop(b.outBuf)
	return nil
}

func (b *AudioBridge) CaptureFrames() <-chan []int16 {
	return b.captureFrameCh
}

func (b *AudioBridge) Close() error {
	var closeErr error
	b.stopOnce.Do(func() {
		close(b.stopCh)
		b.webrtcOut.Close()
		b.wg.Wait()
		close(b.captureFrameCh)

		_ = b.inStream.Stop()
		_ = b.outStream.Stop()
		if err := b.inStream.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		if err := b.outStream.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		if err := portaudio.Terminate(); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (b *AudioBridge) PushFromWebRTC(samples []int16) {
	if len(samples) == 0 {
		return
	}
	b.webrtcOut.Write(samples)
}

func (b *AudioBridge) captureLoop(buf []int16) {
	defer b.wg.Done()

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		if err := b.inStream.Read(); err != nil {
			b.logger.Printf("capture read error: %v", err)
			time.Sleep(20 * time.Millisecond)
			continue
		}

		frame := make([]int16, len(buf))
		copy(frame, buf)

		select {
		case b.captureFrameCh <- frame:
		default:
		}
	}
}

func (b *AudioBridge) playbackLoop(buf []int16) {
	defer b.wg.Done()

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		n, ok := b.webrtcOut.ReadPartial(buf)
		if !ok {
			return
		}
		for i := n; i < len(buf); i++ {
			buf[i] = 0
		}

		if err := b.outStream.Write(); err != nil {
			b.logger.Printf("playback write error: %v", err)
			time.Sleep(20 * time.Millisecond)
		}
	}
}
