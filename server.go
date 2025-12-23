package main

import (
	"bytes"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

func serverGetRespSize(r *http.Request) int {
	// 获取请求头中的 x-press-size 值
	sizeHeader := r.Header.Get("x-press-size")

	var responseSize int
	if sizeHeader != "" {
		parsedSize, err := strconv.Atoi(sizeHeader)
		if err == nil {
			responseSize = parsedSize
		} else {
			responseSize = 1024 // 默认值
		}
	} else {
		// 如果没有头信息，使用默认值
		responseSize = 1024
	}
	return responseSize
}

func serverHandler(w http.ResponseWriter, r *http.Request) {
	// 记录请求开始时间
	startTime := time.Now()
	if config.delayRespHdr > 0 {
		delay := config.delayRespHdr
		if config.delayRespHdrRandom > 0 {
			delay += rand.Intn(config.delayRespHdrRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	// 获取Trace-ID
	traceID := r.Header.Get("Trace-ID")
	if traceID == "" {
		traceID = "unknown"
	}

	// 获取请求信息
	method := r.Method
	host := r.Host
	url := r.URL.String()
	contentLength := r.ContentLength

	responseSize := serverGetRespSize(r)
	/*
		// 检查是否为 Range 请求
		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			// 解析 Range 头部，格式如 "bytes=0-499"
			if strings.HasPrefix(rangeHeader, "bytes=") {
				rangeValue := strings.TrimPrefix(rangeHeader, "bytes=")
				if strings.Contains(rangeValue, "-") {
					parts := strings.Split(rangeValue, "-")
					start, err1 := strconv.ParseInt(parts[0], 10, 64)
					end := int64(responseSize - 1)
					if parts[1] != "" {
						end, err2 := strconv.ParseInt(parts[1], 10, 64)
						if err2 != nil {
							end = int64(responseSize - 1)
						}
						if end >= int64(responseSize) {
							end = int64(responseSize - 1)
						}
					}

					if err1 == nil && start <= end && start < int64(responseSize) {
						// 有效 Range 请求
						rangeBody := make([]byte, end-start+1)
						for i := range rangeBody {
							rangeBody[i] = 'x'
						}

						// 记录头部发送时间
						headerSendTime := time.Now()

						// 设置 Range 响应头
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, responseSize))
						w.Header().Set("Content-Length", strconv.Itoa(len(rangeBody)))
						w.Header().Set("Content-Type", "application/octet-stream")
						w.WriteHeader(http.StatusPartialContent)

						// 发送部分响应
						w.Write(rangeBody)

						// 记录body完成时间
						bodyCompleteTime := time.Now()

						fmt.Printf("Range响应 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Content-Length: %d, Start: %s, HeaderSent: %s, BodyComplete: %s, Range: %s, BodyLength: %d\n",
							traceID, host, url, method, contentLength,
							startTime.Format("2006-01-02 15:04:05.000"),
							headerSendTime.Format("2006-01-02 15:04:05.000"),
							bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
							rangeHeader, len(rangeBody))
						return
					}
				}
			}
		}
	*/
	// 生成响应体
	responseBody := bytes.Repeat([]byte("x"), responseSize)

	// 记录头部发送时间
	headerSendTime := time.Now()

	// 设置响应头
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(responseSize))

	if config.delayRespBody > 0 {
		delay := config.delayRespBody
		if config.delayRespBodyRandom > 0 {
			delay += rand.Intn(config.delayRespBodyRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	// 发送响应
	w.Write(responseBody)

	// 记录body完成时间
	bodyCompleteTime := time.Now()

	fmt.Printf("响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Content-Length: %d, Start: %s, HeaderSent: %s, BodyComplete: %s, BodyLength: %d\n",
		traceID, host, url, method, contentLength,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(responseBody))
}

func startServer() {
	addr := fmt.Sprintf(":%d", config.port)
	fmt.Printf("启动服务器在端口 %s\n", addr)
	fmt.Printf("服务器将根据请求头 x-press-size 的值返回对应大小的响应体\n")

	http.HandleFunc("/", serverHandler)
	log.Fatal(http.ListenAndServe(addr, nil))
}
