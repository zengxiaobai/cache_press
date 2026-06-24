package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"cache_press/pkg/buffer"

	"go.uber.org/ratelimit"
)

var (
	done            = make(chan bool)
	totalRequests   int64
	successRequests int64
	failedRequests  int64
	totalBytes      int64
	cacheHits       int64
)

func bytesToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

func stringToBytes(s string) []byte {
	return *(*[]byte)(unsafe.Pointer(
		&struct {
			string
			Cap int
		}{s, len(s)},
	))
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bPtr := buffer.GetBytes(1024)
	defer buffer.PutBytes(bPtr)
	b := *bPtr

	if n > cap(b) {
		b = make([]byte, n)
	} else {
		b = b[:n]
	}

	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}

	return bytesToString(b)
}

func runClient() {
	baseURL := getBaseURL()

	initAccessLog()
	defer closeAccessLog()

	limiter := ratelimit.New(config.qps)

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, config.conns)

	startTime := time.Now()

	clientStat()

	for i := 0; i < config.conns; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			client := &http.Client{
				Timeout:   config.clientTimeout,
				Transport: transport,
			}

			for {
				if time.Since(startTime) >= config.duration {
					break
				}

				if config.maxRequests > 0 && atomic.LoadInt64(&totalRequests) >= int64(config.maxRequests) {
					break
				}

				limiter.Take()
				semaphore <- struct{}{}

				req, err := createRequest(connID, baseURL)
				if err != nil {
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					<-semaphore
					continue
				}

				requestStartTime := time.Now()

				resp, err := client.Do(req)
				if err != nil {
					fmt.Println(req.URL.RequestURI(), err)
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					<-semaphore
					if !config.ignoreErr {
						os.Exit(1)
					}
					continue
				}
				defer resp.Body.Close()

				result := readResponseBody(resp, requestStartTime)

				if result.cacheHit {
					atomic.AddInt64(&cacheHits, 1)
				}

				if config.compareAddr != "" {
					isRangeReq := req.Header.Get("Range") != ""
					if (isRangeReq && config.enableRange) || !isRangeReq {
						compareHash(req, resp, result, requestStartTime)
					}
				}

				recordRequestResult(resp, req, result, requestStartTime)

				// 如果配置了随机 PURGE 概率，则随机发送 PURGE 请求
				if config.randomPurgeProb > 0 && rand.Float64() < config.randomPurgeProb {
					purgeURL := fmt.Sprintf("%s://%s%s", req.URL.Scheme, config.addr, req.URL.RequestURI())
					sendPurgeRequest(client, purgeURL)
				}

				<-semaphore
			}
		}(i)
	}

	wg.Wait()
	done <- true

	printFinalStats(baseURL, startTime)
}

func compareHash(req *http.Request, resp *http.Response, result responseResult, requestStartTime time.Time) {
	compareClient := &http.Client{
		Timeout:   config.clientTimeout,
		Transport: transport,
	}

	compareURL := *req.URL
	compareURL.Host = config.compareAddr

	compareReq, err := http.NewRequest(req.Method, compareURL.String(), nil)
	if err != nil {
		fmt.Printf("创建对比请求失败: %v\n", err)
		return
	}
	if config.host != "" {
		compareReq.Host = config.host
	}

	for key, values := range req.Header {
		for _, value := range values {
			compareReq.Header.Add(key, value)
		}
	}

	compareResp, err := compareClient.Do(compareReq)
	if err != nil {
		fmt.Printf("对比请求失败: %v\n", err)
		return
	}
	defer compareResp.Body.Close()

	compareResult := readResponseBody(compareResp, requestStartTime)

	isRangeReq := req.Header.Get("Range") != ""
	clientMD5 := result.calculatedMD5
	var compareMD5 string
	if isRangeReq {
		// range 请求: 使用 compare server 响应头中的 hash
		compareMD5 = compareResp.Header.Get("X-Content-MD5")
	} else {
		// 非range 请求: 直接对比两边 body 的 hash (均由客户端计算, 不信任响应头)
		compareMD5 = compareResult.calculatedMD5
	}

	if clientMD5 != "" && compareMD5 != "" {
		// 从请求头读取 Trace-ID，如果为空则从响应头读取，再为空则设为 unknown
		traceID := req.Header.Get(config.ReqIDHdrName)
		if traceID == "" {
			traceID = resp.Header.Get(config.ReqIDHdrName)
		}
		if traceID == "" {
			traceID = "unknown"
		}
		clientDataLen := result.readBytes
		compareDataLen := compareResult.readBytes

		mode := "body-hash"
		if isRangeReq {
			mode = "range-hdr"
		}

		if clientMD5 == compareMD5 {
			fmt.Printf("Hash 对比成功 - URI: %s, Trace-ID: %s %s %s, 模式: %s, Client MD5: %s, Compare Server MD5: %s, Client 数据长度: %d, Compare Server 数据长度: %d\n",
				req.URL.RequestURI(), traceID, req.Header.Get(config.ReqIDHdrName), resp.Header.Get(config.ReqIDHdrName), mode, clientMD5, compareMD5, clientDataLen, compareDataLen)
		} else {
			fmt.Printf("Hash 对比失败 - URI: %s, Trace-ID: %s %s %s, 模式: %s, Client MD5: %s, Compare Server MD5: %s, Client 数据长度: %d, Compare Server 数据长度: %d\n",
				req.URL.RequestURI(), traceID, req.Header.Get(config.ReqIDHdrName), resp.Header.Get(config.ReqIDHdrName), mode, clientMD5, compareMD5, clientDataLen, compareDataLen)
			// 打印 Client 读取内容的前 20 字节
			if len(result.bodyFirst20Bytes) > 0 {
				fmt.Printf("Hash 对比失败 - Client 前 20 字节(hex): %x\n", result.bodyFirst20Bytes)
				fmt.Printf("Hash 对比失败 - Client 前 20 字节(string): %s\n", string(result.bodyFirst20Bytes))
			}
			// 打印 Compare Server 读取内容的前 20 字节
			if len(compareResult.bodyFirst20Bytes) > 0 {
				fmt.Printf("Hash 对比失败 - Compare Server 前 20 字节(hex): %x\n", compareResult.bodyFirst20Bytes)
				fmt.Printf("Hash 对比失败 - Compare Server 前 20 字节(string): %s\n", string(compareResult.bodyFirst20Bytes))
			}
			os.Exit(1)
		}
	}
}
