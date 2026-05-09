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

				if config.enableRange && config.compareAddr != "" && req.Header.Get("Range") != "" {
					compareHash(req, resp, result, requestStartTime)
				}

				recordRequestResult(resp, req, result, requestStartTime)

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

	_ = readResponseBody(compareResp, requestStartTime)

	clientMD5 := result.calculatedMD5
	compareMD5 := compareResp.Header.Get("X-Content-MD5")

	if clientMD5 != "" && compareMD5 != "" {
		traceID := resp.Header.Get(config.ReqIDHdrName)
		if traceID == "" {
			traceID = "unknown"
		}
		dataLen := result.readBytes

		if clientMD5 == compareMD5 {
			fmt.Printf("Hash 对比成功 - URI: %s, Trace-ID: %s, Client MD5: %s, Compare Server MD5: %s, 数据长度: %d\n", req.URL.RequestURI(), traceID, clientMD5, compareMD5, dataLen)
		} else {
			fmt.Printf("Hash 对比失败 - URI: %s, Trace-ID: %s, Client MD5: %s, Compare Server MD5: %s, 数据长度: %d\n", req.URL.RequestURI(), traceID, clientMD5, compareMD5, dataLen)
			os.Exit(1)
		}
	}
}
