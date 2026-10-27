// Derived from github.com/ichioda/x-client-transaction-id (MIT).
// Copyright (c) 2026 George Santiago; see LICENSE.
package transaction

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type cubic struct{ c []float64 }

func (cu *cubic) value(t float64) float64 {
	var start, mid, end = 0.0, 0.0, 1.0
	if t <= 0 {
		if cu.c[0] > 0 {
			return (cu.c[1] / cu.c[0]) * t
		}
		if cu.c[1] == 0 && cu.c[2] > 0 {
			return (cu.c[3] / cu.c[2]) * t
		}
		return 0
	}
	if t >= 1 {
		if cu.c[2] < 1 {
			return 1 + ((cu.c[3]-1)/(cu.c[2]-1))*(t-1)
		}
		if cu.c[2] == 1 && cu.c[0] < 1 {
			return 1 + ((cu.c[1]-1)/(cu.c[0]-1))*(t-1)
		}
		return 1
	}
	for start < end {
		mid = (start + end) / 2
		xEst := bezier(cu.c[0], cu.c[2], mid)
		if math.Abs(t-xEst) < 0.00001 {
			return bezier(cu.c[1], cu.c[3], mid)
		}
		if xEst < t {
			start = mid
		} else {
			end = mid
		}
	}
	return bezier(cu.c[1], cu.c[3], mid)
}

func bezier(a, b, m float64) float64 {
	return 3*a*(1-m)*(1-m)*m + 3*b*(1-m)*m*m + m*m*m
}

func solve(value, min, max float64, floor bool) float64 {
	r := value*(max-min)/255 + min
	if floor {
		return math.Floor(r)
	}
	return math.Round(r*100) / 100
}

func interpolateNum(from, to, f float64) float64 {
	return from*(1-f) + to*f
}

func animationKey(frameRow []int, targetTime float64) string {
	fromColor := []float64{float64(frameRow[0]), float64(frameRow[1]), float64(frameRow[2]), 1}
	toColor := []float64{float64(frameRow[3]), float64(frameRow[4]), float64(frameRow[5]), 1}
	toRotation := solve(float64(frameRow[6]), 60, 360, true)

	curves := make([]float64, 0, len(frameRow)-7)
	for i, v := range frameRow[7:] {
		odd := 0.0
		if i%2 != 0 {
			odd = -1.0
		}
		curves = append(curves, solve(float64(v), odd, 1, false))
	}
	val := (&cubic{curves}).value(targetTime)

	var parts []string
	for i := range 3 {
		c := interpolateNum(fromColor[i], toColor[i], val)
		c = math.Max(0, math.Min(255, c))
		parts = append(parts, strconv.FormatInt(int64(math.Round(c)), 16))
	}

	rotation := interpolateNum(0, toRotation, val)
	rad := rotation * math.Pi / 180
	for _, v := range []float64{math.Cos(rad), -math.Sin(rad), math.Sin(rad), math.Cos(rad)} {
		r := math.Round(v*100) / 100
		if r < 0 {
			r = -r
		}
		h := floatToHex(r)
		switch {
		case strings.HasPrefix(h, "."):
			parts = append(parts, strings.ToLower("0"+h))
		case h == "":
			parts = append(parts, "0")
		default:
			parts = append(parts, h)
		}
	}
	parts = append(parts, "0", "0")
	joined := strings.Join(parts, "")
	return strings.NewReplacer(".", "", "-", "").Replace(joined)
}

func floatToHex(x float64) string {
	var digits []byte
	for q := x; int(q) > 0; {
		iq := int(q / 16)
		r := int(q - float64(iq)*16)
		digits = append([]byte{hexDigit(r)}, digits...)
		q = float64(iq)
	}
	frac := x - float64(int(x))
	if frac == 0 {
		return string(digits)
	}
	digits = append(digits, '.')
	for frac > 0 {
		frac *= 16
		i := int(frac)
		frac -= float64(i)
		digits = append(digits, hexDigit(i))
	}
	return string(digits)
}

func hexDigit(n int) byte {
	if n > 9 {
		return byte('A' + n - 10)
	}
	return byte('0' + n)
}

func deriveAnimationKey(keyBytes []byte, frames [][][]int, firstIndex int, remaining []int) (string, error) {
	if firstIndex < 0 || firstIndex >= len(keyBytes) || len(keyBytes) < 6 || len(frames) != 4 {
		return "", fmt.Errorf("invalid transaction animation materials")
	}
	for _, i := range remaining {
		if i < 0 || i >= len(keyBytes) {
			return "", fmt.Errorf("transaction key index %d is out of range", i)
		}
	}
	for _, frame := range frames {
		for _, row := range frame {
			if len(row) != 11 {
				return "", fmt.Errorf("transaction animation row has %d values; expected 11", len(row))
			}
		}
	}
	rowIndex := int(keyBytes[firstIndex]) % 16
	frameTime := 1
	for _, i := range remaining {
		frameTime *= int(keyBytes[i]) % 16
	}
	frameTime = int(math.Round(float64(frameTime)/10) * 10)
	frame := frames[int(keyBytes[5])%4]
	if rowIndex >= len(frame) {
		return "", fmt.Errorf("row index %d out of range (frame has %d rows)", rowIndex, len(frame))
	}
	return animationKey(frame[rowIndex], float64(frameTime)/4096), nil
}
