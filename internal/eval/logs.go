package eval

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type LogFetcher interface {
	Fetch(context.Context, string, string, string) (LogCollection, error)
}

type LogCollection struct {
	Data      []byte
	Truncated bool
}

const MaxLogTailLines int64 = MaxEventCount + 1

type PodLogStreamer func(
	context.Context,
	string,
	string,
	*corev1.PodLogOptions,
) (io.ReadCloser, error)

type KubernetesLogFetcher struct {
	Client kubernetes.Interface
	Stream PodLogStreamer
}

func (fetcher KubernetesLogFetcher) Fetch(
	ctx context.Context,
	namespace, podName, container string,
) (LogCollection, error) {
	tailLines := MaxLogTailLines
	limitBytes := int64(MaxLogBytes + 1)
	options := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tailLines,
		LimitBytes: &limitBytes,
	}
	streamLogs := fetcher.Stream
	if streamLogs == nil {
		streamLogs = func(
			ctx context.Context,
			namespace string,
			podName string,
			options *corev1.PodLogOptions,
		) (io.ReadCloser, error) {
			return fetcher.Client.CoreV1().Pods(namespace).GetLogs(podName, options).Stream(ctx)
		}
	}
	stream, err := streamLogs(ctx, namespace, podName, options)
	if err != nil {
		return LogCollection{}, err
	}
	defer stream.Close()
	tail := newBoundedTail(MaxLogBytes)
	if _, err := io.Copy(tail, stream); err != nil {
		return LogCollection{}, err
	}
	return LogCollection{
		Data:      append([]byte(nil), tail.Bytes()...),
		Truncated: tail.Truncated() || logLineCount(tail.Bytes()) >= MaxLogTailLines,
	}, nil
}

type boundedTail struct {
	data      []byte
	limit     int
	truncated bool
}

func newBoundedTail(limit int) *boundedTail {
	return &boundedTail{data: make([]byte, 0, limit), limit: limit}
}

func (tail *boundedTail) Write(data []byte) (int, error) {
	originalLength := len(data)
	if tail.limit <= 0 {
		tail.truncated = tail.truncated || len(data) > 0
		return originalLength, nil
	}
	if len(data) >= tail.limit {
		tail.data = append(tail.data[:0], data[len(data)-tail.limit:]...)
		tail.truncated = true
		return originalLength, nil
	}
	overflow := len(tail.data) + len(data) - tail.limit
	if overflow > 0 {
		copy(tail.data, tail.data[overflow:])
		tail.data = tail.data[:len(tail.data)-overflow]
		tail.truncated = true
	}
	tail.data = append(tail.data, data...)
	return originalLength, nil
}

func (tail *boundedTail) Bytes() []byte {
	return tail.data
}

func (tail *boundedTail) Truncated() bool {
	return tail.truncated
}

func logLineCount(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	count := int64(1)
	for _, value := range data {
		if value == '\n' {
			count++
		}
	}
	if data[len(data)-1] == '\n' {
		count--
	}
	return count
}
