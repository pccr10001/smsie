//go:build !nouac

package calling

const (
	ulawBias = 0x84
	ulawClip = 32635
)

func encodeULaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, sample := range pcm {
		out[i] = linearToULaw(sample)
	}
	return out
}

func decodeULaw(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, sample := range data {
		out[i] = uLawToLinear(sample)
	}
	return out
}

func decodeALaw(data []byte) []int16 {
	out := make([]int16, len(data))
	for i, sample := range data {
		out[i] = aLawToLinear(sample)
	}
	return out
}

func linearToULaw(sample int16) byte {
	s := int(sample)
	sign := 0
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > ulawClip {
		s = ulawClip
	}
	s += ulawBias

	exponent := 7
	for expMask := 0x4000; exponent > 0 && (s&expMask) == 0; exponent-- {
		expMask >>= 1
	}
	mantissa := (s >> (exponent + 3)) & 0x0F

	return ^byte(sign | (exponent << 4) | mantissa)
}

func uLawToLinear(sample byte) int16 {
	sample = ^sample
	sign := sample & 0x80
	exponent := (sample >> 4) & 0x07
	mantissa := sample & 0x0F

	value := ((int(mantissa) << 3) + ulawBias) << exponent
	value -= ulawBias
	if sign != 0 {
		value = -value
	}
	if value > 32767 {
		value = 32767
	}
	if value < -32768 {
		value = -32768
	}
	return int16(value)
}

func aLawToLinear(sample byte) int16 {
	sample ^= 0x55

	sign := sample & 0x80
	exponent := (sample >> 4) & 0x07
	mantissa := sample & 0x0F

	value := int(mantissa) << 4
	if exponent == 0 {
		value += 8
	} else {
		value += 0x108
		value <<= exponent - 1
	}

	if sign == 0 {
		value = -value
	}
	if value > 32767 {
		value = 32767
	}
	if value < -32768 {
		value = -32768
	}
	return int16(value)
}
