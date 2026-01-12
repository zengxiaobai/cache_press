package main

import (
	"bytes"
	"cache_press/pkg/buffer"
	"compress/gzip"
	"fmt"
	"net/http"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/pierrec/xxHash/xxHash32"
)

var (
	compressedCache      = make(map[string][]byte)
	compressedCacheMutex sync.RWMutex
)

func getPreCompressedBody(responseBody []byte, encoding string) []byte {
	cacheKey := fmt.Sprintf("%d_%s", len(responseBody), encoding)

	compressedCacheMutex.RLock()
	compressedBody, ok := compressedCache[cacheKey]
	compressedCacheMutex.RUnlock()

	if ok {
		return compressedBody
	}

	var buf bytes.Buffer
	if encoding == "br" {
		bw := brotli.NewWriter(&buf)
		_, _ = bw.Write(responseBody)
		_ = bw.Close()
	} else if encoding == "gzip" {
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write(responseBody)
		_ = gw.Close()
	}

	compressedBody = buf.Bytes()

	compressedCacheMutex.Lock()
	defer compressedCacheMutex.Unlock()
	if _, ok := compressedCache[cacheKey]; !ok {
		compressedCache[cacheKey] = compressedBody
	}

	return compressedBody
}

func streamCompressedBody(w http.ResponseWriter, responseBody []byte, encoding string) error {
	if encoding == "br" {
		bw := brotli.NewWriter(w)
		_, err := bw.Write(responseBody)
		if err != nil {
			return err
		}
		return bw.Close()
	} else if encoding == "gzip" {
		gw := gzip.NewWriter(w)
		_, err := gw.Write(responseBody)
		if err != nil {
			return err
		}
		return gw.Close()
	}
	return nil
}

func calculateMD5(data []byte) string {
	return fmt.Sprintf("%x", xxHash32.Checksum(data, 0))
}

func calculateRangeMD5(body []byte, ranges []Range) string {
	if len(ranges) == 1 {
		return calculateMD5(body[ranges[0].Start : ranges[0].End+1])
	}

	contentLength := int64(len(body))
	contentType := "application/octet-stream"

	buf := buffer.GetIoBuffer(2048)
	defer buffer.PutIoBuffer(buf)

	for _, r := range ranges {
		buf.WriteString(fmt.Sprintf("Content-Type: %s\r\n", contentType))
		buf.WriteString(fmt.Sprintf("Content-Range: bytes %d-%d/%d\r\n", r.Start, r.End, contentLength))
		buf.WriteString("\r\n")
		buf.Write(body[r.Start : r.End+1])
		buf.WriteString("\r\n")
	}

	return calculateMD5(buf.Bytes())
}
