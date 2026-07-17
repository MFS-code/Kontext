package main

import (
	"bytes"

	resultv1alpha1 "github.com/kontext-dev/kontext/pkg/result/v1alpha1"
)

type CapturedResult struct {
	Data          []byte
	Found         bool
	Truncated     bool
	OriginalBytes int
}

type Capture struct {
	format   CaptureFormat
	maxBytes int

	current          []byte
	currentTotal     int
	currentTruncated bool

	lastLine  CapturedResult
	candidate CapturedResult
}

func newCapture(format CaptureFormat, maxBytes int) *Capture {
	return &Capture{
		format:   format,
		maxBytes: maxBytes,
		current:  make([]byte, 0, min(maxBytes, 4096)),
	}
}

func (capture *Capture) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			capture.appendCurrent(data)
			break
		}
		capture.appendCurrent(data[:newline])
		capture.completeLine()
		data = data[newline+1:]
	}
	return written, nil
}

func (capture *Capture) Result() CapturedResult {
	if capture.currentTotal > 0 {
		capture.completeLine()
	}
	switch capture.format {
	case CaptureFormatLastLine:
		return cloneCapturedResult(capture.lastLine)
	case CaptureFormatKontextEnvelope:
		return cloneCapturedResult(capture.candidate)
	default:
		return CapturedResult{}
	}
}

func (capture *Capture) appendCurrent(data []byte) {
	capture.currentTotal += len(data)
	remaining := capture.maxBytes - len(capture.current)
	if remaining <= 0 {
		capture.currentTruncated = true
		return
	}
	if len(data) > remaining {
		capture.current = append(capture.current, data[:remaining]...)
		capture.currentTruncated = true
		return
	}
	capture.current = append(capture.current, data...)
}

func (capture *Capture) completeLine() {
	line := bytes.TrimSuffix(capture.current, []byte{'\r'})
	if len(bytes.TrimSpace(line)) > 0 {
		switch capture.format {
		case CaptureFormatLastLine:
			capture.lastLine = CapturedResult{
				Data:          append([]byte(nil), line...),
				Found:         true,
				Truncated:     capture.currentTruncated,
				OriginalBytes: capture.currentTotal,
			}
		case CaptureFormatKontextEnvelope:
			if payload, found := resultv1alpha1.ExtractEnvelopePayload(line); found {
				capture.candidate = CapturedResult{
					Data:          append([]byte(nil), payload...),
					Found:         true,
					Truncated:     capture.currentTruncated,
					OriginalBytes: capture.currentTotal,
				}
			}
		}
	}
	capture.current = capture.current[:0]
	capture.currentTotal = 0
	capture.currentTruncated = false
}

func cloneCapturedResult(result CapturedResult) CapturedResult {
	result.Data = append([]byte(nil), result.Data...)
	return result
}
