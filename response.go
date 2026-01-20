package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func handlePreCompressedRange(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, traceID, method, host, url string, startTime time.Time) {
	compressedBody := getPreCompressedBody(responseBody, encoding)
	contentType := "application/octet-stream"

	ranges, err := parseRangeHeader(r.Header.Get("Range"), int64(len(compressedBody)))
	if err != nil {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(compressedBody)))
		fmt.Printf("Range 请求无效 - Trace-ID: %s, Error: %v %s\n", traceID, err, r.Header.Get("Range"))
		return
	}

	var md5Sum string
	if config.enableHash {
		md5Sum = calculateRangeMD5(compressedBody, ranges)
		fmt.Printf("预压缩 Range 响应 MD5 - Trace-ID: %s, 范围数: %d, MD5: %s\n", traceID, len(ranges), md5Sum)
	}

	w.Header().Set("Content-Encoding", encoding)
	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if len(ranges) == 1 {
		handleSingleRange(w, ranges[0], compressedBody, contentType, md5Sum)
	} else {
		handleMultiRange(w, ranges, compressedBody, contentType, md5Sum)
	}

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩 Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Encoding: %s, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handlePreCompressedResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, traceID, method, host, url string, startTime time.Time) {
	compressedBody := getPreCompressedBody(responseBody, encoding)
	contentType := "application/octet-stream"

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Encoding", encoding)
	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if !config.useChunkedTransfer {
		w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
	}

	if config.enableHash {
		md5Sum := calculateMD5(compressedBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("预压缩响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", traceID, len(compressedBody), md5Sum)
	}

	setConnectionHeader(w)
	w.WriteHeader(http.StatusOK)
	headerSendTime := time.Now()
	serveBodyWithDelay()
	sendData(w, compressedBody)

	closeConnectionIfNeeded(w)

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Encoding: %s, Start: %s, HeaderSent: %s, BodyComplete: %s, BodyLength: %d\n",
		traceID, host, url, method, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(compressedBody))
}

func handleRangeRequest(w http.ResponseWriter, r *http.Request, responseBody []byte, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	ranges, err := parseRangeHeader(r.Header.Get("Range"), int64(len(responseBody)))
	if err != nil {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(responseBody)))
		fmt.Printf("Range 请求无效 - Trace-ID: %s, Error: %v %s\n", traceID, err, r.Header.Get("Range"))
		return
	}

	var md5Sum string
	if config.enableHash {
		md5Sum = calculateRangeMD5(responseBody, ranges)
		fmt.Printf("Range 响应 MD5 - Trace-ID: %s, 范围数: %d, MD5: %s\n", traceID, len(ranges), md5Sum)
	}

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	}
	bodyStartTime := time.Now()
	fmt.Printf("Range 响应开始 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyStartTime.Format("2006-01-02 15:04:05.000"))

	if len(ranges) == 1 {
		handleSingleRange(w, ranges[0], responseBody, contentType, md5Sum)
	} else {
		handleMultiRange(w, ranges, responseBody, contentType, md5Sum)
	}

	bodyCompleteTime := time.Now()
	fmt.Printf("Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handleNormalResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		if encoding != "" {
			fmt.Printf("边压缩边响应 MD5 - Trace-ID: %s, 原始大小: %d, MD5: %s\n", traceID, len(responseBody), md5Sum)
		} else {
			fmt.Printf("MD5校验已启用，响应大小: %d, MD5: %s\n", len(responseBody), md5Sum)
		}
	}

	if !config.useChunkedTransfer {
		if encoding != "" {
			compressedBody := getPreCompressedBody(responseBody, encoding)
			w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		}
	}

	w.WriteHeader(http.StatusOK)
	headerSendTime := time.Now()
	serveBodyWithDelay()

	if encoding != "" {
		compressedBody := getPreCompressedBody(responseBody, encoding)
		sendData(w, compressedBody)
	} else {
		sendData(w, responseBody)
	}

	closeConnectionIfNeeded(w)

	bodyCompleteTime := time.Now()
	fmt.Printf("响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Start: %s, HeaderSent: %s, BodyComplete: %s, BodyLength: %d\n",
		traceID, host, url, method,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(responseBody))
}

func setConnectionHeader(w http.ResponseWriter) {
	if config.keepAliveProb > 0 && rand.Float64() <= config.keepAliveProb {
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Connection", "close")
	}
}

func closeConnectionIfNeeded(w http.ResponseWriter) {
	if config.closeConnAfterBodyProb > 0 && rand.Float64() <= config.closeConnAfterBodyProb {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}
}

func handleHeadResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("HEAD 响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", traceID, len(responseBody), md5Sum)
	}

	setConnectionHeader(w)
	w.WriteHeader(http.StatusOK)

	bodyCompleteTime := time.Now()
	fmt.Printf("HEAD 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Content-Length: %d, Start: %s, HeaderSent: %s, BodyComplete: %s\n",
		traceID, host, url, method, len(responseBody),
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}
