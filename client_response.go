package main

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cache_press/pkg/buffer"

	"github.com/pierrec/xxHash/xxHash32"
)

type responseResult struct {
	readBytes     int64
	cacheHit      bool
	firstByteTime time.Duration
	responseTime  time.Duration
	err           error
	calculatedMD5 string
}

func readResponseBody(resp *http.Response, requestStartTime time.Time) responseResult {
	result := responseResult{}

	if resp.StatusCode > 300 {
		result.err = fmt.Errorf("请求失败: %s", resp.StatusCode)
		return result
	}

	result.firstByteTime = time.Since(requestStartTime)

	if config.clientSendCloseProb > 0 && rand.Float64() <= config.clientSendCloseProb {
		if tcpConn, ok := resp.Body.(interface{ Close() error }); ok {
			tcpConn.Close()
		}
		return result
	}

	xCacheHeader := resp.Header.Get("X-Wycdn-Cache-Status")
	result.cacheHit = strings.Contains(xCacheHeader, "HIT")

	var totalExpected int64
	if clStr := resp.Header.Get("Content-Length"); clStr != "" {
		totalExpected, _ = strconv.ParseInt(clStr, 10, 64)
	}

	serverMD5 := resp.Header.Get("X-Content-MD5")
	isRangeRequest := resp.Request != nil && resp.Request.Header.Get("Range") != ""
	shouldCalculateHash := serverMD5 != "" || (isRangeRequest && config.compareAddr != "")

	var bodyData []byte
	if shouldCalculateHash {
		bodyData = make([]byte, 0, 1024*1024)
	}

	isMultiRange := strings.Contains(resp.Header.Get("Content-Type"), "multipart/byteranges")
	var boundary string
	if isMultiRange {
		ct := resp.Header.Get("Content-Type")
		if idx := strings.Index(ct, "boundary="); idx != -1 {
			boundary = ct[idx+9:]
		}
	}

	const chunkSize = 35840
	chunkPtr := buffer.GetBytes(35840)
	chunk := *chunkPtr

	reader := resp.Body
	for {
		n, readErr := reader.Read(chunk)
		if n > 0 {
			result.readBytes += int64(n)
			if shouldCalculateHash {
				if isMultiRange && boundary != "" {
					data := chunk[:n]
					filteredData := filterMultipartBoundary(data, boundary)
					bodyData = append(bodyData, filteredData...)
				} else {
					bodyData = append(bodyData, chunk[:n]...)
				}
			}
		}

		if config.clientRecvHalfCloseProb > 0 && rand.Float64() <= config.clientRecvHalfCloseProb {
			if totalExpected > 0 {
				if result.readBytes >= totalExpected/2 {
					break
				}
			} else {
				if rand.Float64() <= 0.1 {
					break
				}
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				result.err = readErr
			}
			break
		}
	}
	buffer.PutBytes(chunkPtr)

	if shouldCalculateHash {
		calculatedMD5 := fmt.Sprintf("%x", xxHash32.Checksum(bodyData, 0))

		if config.testHashFailure {
			if len(calculatedMD5) > 0 {
				bytes := []byte(calculatedMD5)
				if bytes[len(bytes)-1] == '9' {
					bytes[len(bytes)-1] = '0'
				} else {
					bytes[len(bytes)-1] = '9'
				}
				calculatedMD5 = string(bytes)
			}
		}

		result.calculatedMD5 = calculatedMD5

		if !isRangeRequest && serverMD5 != "" {
			if calculatedMD5 != serverMD5 {
				fmt.Printf("MD5校验失败! 服务器MD5: %s, 客户端计算MD5: %s %s\n", serverMD5, calculatedMD5, string(bodyData))
				if !config.ignoreErr {
					os.Exit(1)
				}
			}
		}
	}

	result.responseTime = time.Since(requestStartTime)

	if config.clientRecvFullCloseProb > 0 && rand.Float64() <= config.clientRecvFullCloseProb {
		if resp.Body != nil {
			resp.Body.Close()
		}
	}

	return result
}

func filterMultipartBoundary(data []byte, boundary string) []byte {
	boundaryBytes := []byte("--" + boundary)
	result := make([]byte, 0, len(data))
	i := 0

	for i < len(data) {
		if i+len(boundaryBytes) <= len(data) {
			match := true
			for j := 0; j < len(boundaryBytes); j++ {
				if data[i+j] != boundaryBytes[j] {
					match = false
					break
				}
			}

			if match {
				i += len(boundaryBytes)

				for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
					i++
				}

				if i < len(data) && data[i] == '-' && i+1 < len(data) && data[i+1] == '-' {
					break
				}

				for i < len(data) && data[i] != '\n' {
					i++
				}
				if i < len(data) {
					i++
				}

				continue
			}
		}
		result = append(result, data[i])
		i++
	}

	return result
}
