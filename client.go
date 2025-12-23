package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// 结束信号
var done = make(chan bool)
var totalRequests, successRequests, failedRequests int64
var totalBytes int64

func getBaseURL() string {
	if config.addr != "" {
		if !strings.HasPrefix(config.addr, "http") {
			return fmt.Sprintf("http://%s", config.addr)
		}
		return config.addr
	}
	return fmt.Sprintf("http://%s:%d", config.host, config.port)
}

func getRespSize() int {
	if len(config.respSizeRange) == 1 {
		return config.respSizeRange[0]
	}
	minSize, maxSize := config.respSizeRange[0], config.respSizeRange[1]
	if rand.Float64() <= config.diskRatio {
		return minSize
	}
	return maxSize
}

// 添加随机字符串生成函数
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func runClient() {
	// 构建目标URL基础
	var baseURL = getBaseURL()

	limiter := rate.NewLimiter(rate.Limit(config.qps), config.qps)

	var wg sync.WaitGroup

	// 控制并发连接数
	semaphore := make(chan struct{}, config.conns)

	// 统计变量

	var cacheHits int64
	startTime := time.Now()

	clientStat()
	// 创建多个goroutine模拟并发请求
	for i := 0; i < config.conns; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: transport,
			}

			for {
				// 检查是否到达结束时间
				if time.Since(startTime) >= config.duration {
					break
				}

				// 限制QPS
				limiter.Wait(context.Background())

				// 获取信号量控制并发数
				semaphore <- struct{}{}

				// 生成随机URL
				url := generateRandomURL(baseURL, config.urlCount, config.hitRatio)

				// 创建请求
				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					<-semaphore
					continue
				}

				// 设置请求头 - 包括x-press-size头
				req.Header.Set("x-press-size", strconv.Itoa(getRespSize()))
				req.Header.Set("User-Agent", fmt.Sprintf("PressureTestClient-%d", connID))
				req.Header.Set("Trace-ID", fmt.Sprintf("PressureTestClient-%d-%d-%s", connID, time.Now().UnixNano(), randString(6)))

				req.URL.Host = config.addr
				req.Host = config.host
				// 记录请求开始时间
				requestStartTime := time.Now()
				errFunc := func(err error) {
					fmt.Println(req.URL.RequestURI(), err)
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					<-semaphore
					if !config.ignoreErr {
						os.Exit(1)
					}
				}
				// 发送请求
				resp, err := client.Do(req)
				if err != nil {
					// 记录失败请求
					errFunc(err)
					continue
				}
				defer resp.Body.Close()
				// 记录首包时间（收到响应头的时间）
				firstByteTime := time.Since(requestStartTime)

				if resp.StatusCode > 300 {
					errFunc(fmt.Errorf("请求失败: %s", resp.StatusCode))
					continue
				}

				//fmt.Println(resp.Status, resp.Header)
				// 检查 X-Cache 头判断是否命中缓存
				cacheHit := false
				xCacheHeader := resp.Header.Get("X-Cache")
				if strings.Contains(xCacheHeader, "HIT") {
					cacheHit = true
				}

				// 读取响应体
				body, err := io.ReadAll(resp.Body)

				// 记录完整响应时间（收到完整响应体的时间）
				responseTime := time.Since(requestStartTime)

				select {
				case reqStatCh <- reqStatInfo{
					firstByteTime: firstByteTime,
					respTime:      responseTime,
					cacheHit:      cacheHit,
				}:
				default:
					// Channel 满时丢弃数据，防止阻塞
					fmt.Println("！！！丢弃数据，统计通道已满！！！")
				}

				if err != nil {
					// 记录失败请求
					fmt.Println("read body err :",
						err, req.URL.Path, len(body), time.Now().Format("2006-01-02 15:04:05.000"), req.Header.Get("Trace-ID"))
					if !config.ignoreErr {
						os.Exit(1)
					}
					failedRequests++
				} else {

					// 记录成功请求
					atomic.AddInt64(&successRequests, 1)
					totalBytes += int64(len(body))
				}

				atomic.AddInt64(&totalRequests, 1)
				<-semaphore
			}
		}(i)
	}

	// 等待所有协程完成
	wg.Wait()

	// 停止监控
	done <- true

	// 输出最终统计
	elapsed := time.Since(startTime).Seconds()
	finalTotal := atomic.LoadInt64(&totalRequests)
	finalHits := atomic.LoadInt64(&cacheHits)

	hitRate := 0.0
	if finalTotal > 0 {
		hitRate = float64(finalHits) / float64(finalTotal) * 100
	}

	fmt.Printf("\n=== 最终统计 ===\n")
	fmt.Printf("目标地址: %s\n", baseURL)
	fmt.Printf("响应大小范围: %v, 小响应体比例: %.2f\n", config.respSizeRange, config.diskRatio)
	fmt.Printf("总请求数: %d\n", totalRequests)
	fmt.Printf("成功请求数: %d\n", successRequests)
	fmt.Printf("失败请求数: %d\n", failedRequests)
	fmt.Printf("缓存命中数: %d\n", finalHits)
	fmt.Printf("缓存命中率: %.2f%%\n", hitRate)
	fmt.Printf("总传输字节数: %d\n", totalBytes)
	fmt.Printf("平均QPS: %.2f\n", float64(totalRequests)/elapsed)
	fmt.Printf("成功率: %.2f%%\n", float64(successRequests)/float64(totalRequests)*100)
	fmt.Printf("总耗时: %.2fs\n", elapsed)
}
