package main

import (
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

func printFinalStats(baseURL string, startTime time.Time) {
	elapsed := time.Since(startTime).Seconds()
	finalTotal := atomic.LoadInt64(&totalRequests)
	finalSuccess := atomic.LoadInt64(&successRequests)
	finalFailed := atomic.LoadInt64(&failedRequests)
	finalBytes := atomic.LoadInt64(&totalBytes)
	finalHits := atomic.LoadInt64(&cacheHits)

	hitRate := 0.0
	if finalTotal > 0 {
		hitRate = float64(finalHits) / float64(finalTotal) * 100
	}

	successRate := 0.0
	if finalTotal > 0 {
		successRate = float64(finalSuccess) / float64(finalTotal) * 100
	}

	fmt.Printf("\n=== 最终统计 ===\n")
	fmt.Printf("目标地址: %s\n", baseURL)
	fmt.Printf("响应大小范围: %v, 小响应体比例: %.2f\n", config.respSizeRange, config.diskRatio)
	fmt.Printf("总请求数: %d\n", finalTotal)
	fmt.Printf("成功请求数: %d\n", finalSuccess)
	fmt.Printf("失败请求数: %d\n", finalFailed)
	fmt.Printf("缓存命中数: %d\n", finalHits)
	fmt.Printf("缓存命中率: %.2f%%\n", hitRate)
	fmt.Printf("总传输字节数: %d\n", finalBytes)
	fmt.Printf("平均QPS: %.2f\n", float64(finalTotal)/elapsed)
	fmt.Printf("成功率: %.2f%%\n", successRate)
	fmt.Printf("总耗时: %.2fs\n", elapsed)
}

func recordRequestResult(resp *http.Response, req *http.Request, result responseResult, requestStartTime time.Time) {
	if result.err != nil {
		fmt.Println("read body err :", result.err, req.URL.Path, result.readBytes, time.Now().Format("2006-01-02 15:04:05.000"), req.Header.Get(config.ReqIDHdrName))
		if resp != nil {
			fmt.Println("respHeader:", resp.Header)
		}
		fmt.Println("reqHeader:", req.Header)

		if !config.ignoreErr {
			os.Exit(1)
		}
		atomic.AddInt64(&failedRequests, 1)
	} else {
		atomic.AddInt64(&successRequests, 1)
		atomic.AddInt64(&totalBytes, result.readBytes)
	}

	select {
	case reqStatCh <- reqStatInfo{
		firstByteTime: result.firstByteTime,
		respTime:      result.responseTime,
		cacheHit:      result.cacheHit,
		traceID:       req.Header.Get(config.ReqIDHdrName) + req.URL.RequestURI(),
	}:
	default:
		fmt.Println("！！！丢弃数据，统计通道已满！！！")
	}

	atomic.AddInt64(&totalRequests, 1)
}
