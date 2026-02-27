package main

import (
	"bytes"
	"cache_press/pkg/buffer"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var (
	respCache      = make(map[int][]byte)
	respCacheMutex sync.RWMutex
)

func serverGetRespSize(r *http.Request) int {
	sizeHeader := r.Header.Get("x-press-size")

	var responseSize int
	if sizeHeader != "" {
		parsedSize, err := strconv.Atoi(sizeHeader)
		if err == nil {
			responseSize = parsedSize
		} else {
			responseSize = 1024
		}
	} else {
		responseSize = 1024
	}
	return responseSize
}

func serveHeaderWithDelay() {
	if config.delayRespHdr > 0 {
		delay := config.delayRespHdr
		if config.delayRespHdrRandom > 0 {
			delay += rand.Intn(config.delayRespHdrRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

func serveBodyWithDelay() {
	if config.delayRespBody > 0 {
		delay := config.delayRespBody
		if config.delayRespBodyRandom > 0 {
			delay += rand.Intn(config.delayRespBodyRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

func getTraceID(r *http.Request) string {
	if r.Header.Get(config.ReqIDHdrName) != "" {
		return r.Header.Get(config.ReqIDHdrName)
	}
	return "unknown"
}

func createRandomRespBody(size int) []byte {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()_+-=[]{}|;:,.<>?"
	charsetLen := len(charset)

	buf := buffer.GetIoBuffer(size)
	defer buffer.PutIoBuffer(buf)

	for i := 0; i < size; i++ {
		buf.WriteByte(charset[rand.Intn(charsetLen)])
	}

	return buf.Bytes()
}

func createRespBodyCont(size int) []byte {
	if config.useRandomContent {
		return createRandomRespBody(size)
	}

	const pattern = "1234567890abcdefghij"
	patternLen := len(pattern)

	buf := buffer.GetIoBuffer(size)
	defer buffer.PutIoBuffer(buf)

	fullTimes := size / patternLen
	remainder := size % patternLen

	for i := 0; i < fullTimes; i++ {
		buf.WriteString(pattern)
	}

	if remainder > 0 {
		buf.WriteString(pattern[:remainder])
	}

	return buf.Bytes()
}

func genRespBody(responseSize int) []byte {
	var responseBody []byte
	if config.cacheResp {
		respCacheMutex.RLock()
		var ok bool
		responseBody, ok = respCache[responseSize]
		respCacheMutex.RUnlock()
		if !ok {
			newBody := createRespBodyCont(responseSize)
			respCacheMutex.Lock()
			defer respCacheMutex.Unlock()
			if _, ok := respCache[responseSize]; !ok {
				respCache[responseSize] = newBody
			}
			responseBody = newBody
		}
	} else {
		responseBody = createRespBodyCont(responseSize)
	}
	return responseBody
}

func serverHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	serveHeaderWithDelay()

	traceID := getTraceID(r)
	method := r.Method
	host := r.Host
	url := r.URL.String()

	responseSize := serverGetRespSize(r)
	responseBody := genRespBody(responseSize)

	ae := r.Header.Get("Accept-Encoding")
	var encoding string
	if ae != "" {
		if bytes.Contains([]byte(ae), []byte("br")) {
			encoding = "br"
		} else if bytes.Contains([]byte(ae), []byte("gzip")) {
			encoding = "gzip"
		}
	}

	// 处理 X-Mock-304 请求头 - 返回 304 响应但保持响应头不变
	if r.Header.Get("X-Mock-304") != "" {
		handle304Response(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		return
	}

	if method == "HEAD" {
		handleHeadResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		return
	}

	if config.preCompress && encoding != "" {
		if r.Header.Get("Range") != "" {
			handlePreCompressedRange(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		} else {
			handlePreCompressedResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		}
		return
	}

	if r.Header.Get("Range") != "" {
		handleRangeRequest(w, r, responseBody, traceID, method, host, url, startTime)
		return
	}

	handleNormalResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
}

func startServer() {
	initAccessLog()
	defer closeAccessLog()

	var addr string
	if config.listenIP != "" {
		addr = fmt.Sprintf("%s:%d", config.listenIP, config.port)
	} else {
		addr = fmt.Sprintf(":%d", config.port)
	}
	fmt.Printf("启动服务器在 %s\n", addr)
	fmt.Printf("服务器将根据请求头 x-press-size 的值返回对应大小的响应体\n")

	if config.logDir != "" {
		fmt.Printf("访问日志文件: %s\n", config.logDir)
	}

	http.HandleFunc("/", serverHandler)

	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("服务器监听地址: %s\n", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
