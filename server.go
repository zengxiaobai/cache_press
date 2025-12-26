package main

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
)

// 响应体缓存 - 键为响应大小，值为预生成的响应体
var (
	respCache      = make(map[int][]byte)
	respCacheMutex sync.RWMutex
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
	traceID := r.Header.Get(config.ReqIDHdrName)
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
						rangeSize := int(end-start+1)
						rangeBodyPtr := buffer.GetBytes(1024)
						defer buffer.PutBytes(rangeBodyPtr)
						rangeBody := *rangeBodyPtr

						// 如果需要的长度超过池中切片的容量，创建一个新的
						if rangeSize > cap(rangeBody) {
							rangeBody = make([]byte, rangeSize)
						} else {
							rangeBody = rangeBody[:rangeSize]
						}

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
	var responseBody []byte
	if config.cacheResp {
		// 使用响应体缓存
		respCacheMutex.RLock()
		var ok bool
		responseBody, ok = respCache[responseSize]
		respCacheMutex.RUnlock()

		if !ok {
			// 缓存中不存在，生成并添加到缓存
			newBody := bytes.Repeat([]byte("x"), responseSize)
			respCacheMutex.Lock()
			// 再次检查，避免竞态条件
			if _, ok := respCache[responseSize]; !ok {
				respCache[responseSize] = newBody
			}
			respCacheMutex.Unlock()
			responseBody = newBody
		}
	} else {
		// 不使用缓存，临时生成
		responseBody = bytes.Repeat([]byte("x"), responseSize)
	}

	// 根据客户端 Accept-Encoding 决定是否压缩（支持 gzip 和 br）
	ae := r.Header.Get("Accept-Encoding")
	var compressedBody []byte
	encoding := ""

	// 生成响应体（未压缩）
	// responseBody 已生成上方

	if config.delayRespBody > 0 {
		delay := config.delayRespBody
		if config.delayRespBodyRandom > 0 {
			delay += rand.Intn(config.delayRespBodyRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	// 设置基础响应头
	w.Header().Set("Content-Type", "application/octet-stream")

	// 如果启用MD5校验，计算响应内容的MD5并添加到响应头
	if config.enableMD5 {
		var dataToHash []byte
		if encoding != "" {
			// 如果使用压缩，计算压缩后数据的MD5
			dataToHash = compressedBody
		} else {
			// 否则计算原始响应体的MD5
			dataToHash = responseBody
		}

		hasher := md5.New()
		hasher.Write(dataToHash)
		md5Sum := hex.EncodeToString(hasher.Sum(nil))
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("MD5校验已启用，响应大小: %d, MD5: %s\n", len(dataToHash), md5Sum)
	}

	// 根据keepAliveProb设置Connection头
	if config.keepAliveProb > 0 && rand.Float64() <= config.keepAliveProb {
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Connection", "close")
	}

	// 选择压缩算法（优先顺序： br -> gzip ）
	if ae != "" {
		// 简单判断是否包含子串
		if bytes.Contains([]byte(ae), []byte("br")) {
			// brotli
			var buf bytes.Buffer
			bw := brotli.NewWriter(&buf)
			_, _ = bw.Write(responseBody)
			_ = bw.Close()
			compressedBody = buf.Bytes()
			encoding = "br"
		} else if bytes.Contains([]byte(ae), []byte("gzip")) {
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			_, _ = gw.Write(responseBody)
			_ = gw.Close()
			compressedBody = buf.Bytes()
			encoding = "gzip"
		}
	}

	// 设置 Content-Length 和 Content-Encoding（如果压缩）
	if encoding != "" {
		// 告知客户端缓存变体：基于 Accept-Encoding
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Encoding", encoding)
		w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(responseSize))
	}

	// 记录头部发送时间
	headerSendTime := time.Now()

	// 发送响应（压缩或未压缩）
	if encoding != "" {
		_, _ = w.Write(compressedBody)
	} else {
		_, _ = w.Write(responseBody)
	}

	// 根据closeConnAfterBodyProb决定是否主动关闭连接
	if config.closeConnAfterBodyProb > 0 && rand.Float64() <= config.closeConnAfterBodyProb {
		// 尝试获取底层连接并关闭
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}

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
